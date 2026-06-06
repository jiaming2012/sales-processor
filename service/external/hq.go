package external

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	hqDefaultBaseURL = "http://localhost:8080"
)

// HQPeriodSummary mirrors the JSON returned by
// GET /api/v1/inventory/period-summary on the Yumyums HQ backend.
//
// ByVendor is optional — older HQ deployments don't return it. The COGS
// section in the payroll report degrades to the aggregate-only view when
// the slice is nil/empty.
//
// TrackedBankTxIDs lists every Mercury bank_tx_id HQ has touched for the
// period across all states (confirmed/pending/discarded). The payroll
// gap check in fetchHQPeriodSummary diffs Mercury's transaction list
// against this set; nil (omitted by older HQ) drops the check to a
// warning-only degraded mode so deploy ordering doesn't break runs.
type HQPeriodSummary struct {
	From               string              `json:"from"`
	To                 string              `json:"to"`
	COGSExclTax        float64             `json:"cogs_excl_tax"`
	COGSInclTax        float64             `json:"cogs_incl_tax"`
	PurchaseEventCount int                 `json:"purchase_event_count"`
	ByVendor           []HQVendorCOGS      `json:"by_vendor,omitempty"`
	TrackedBankTxIDs   []string            `json:"tracked_bank_tx_ids,omitempty"`
	Completeness       HQCompletenessBlock `json:"completeness"`
}

type HQVendorCOGS struct {
	VendorID     string  `json:"vendor_id"`
	VendorName   string  `json:"vendor_name"`
	TotalExclTax float64 `json:"total_excl_tax"`
	TotalInclTax float64 `json:"total_incl_tax"`
	TripCount    int     `json:"trip_count"`
}

// HQCompletenessBlock describes whether HQ has fully ingested + reviewed
// + catalog-linked every receipt that falls in the period. Ready is true
// iff both ID slices are empty.
//
// PendingReviewDetails is parallel to PendingReviewIDs but each entry
// carries vendor / date / amount so the fail-fast banner can render
// operator-friendly rows. Older HQ deployments (pre cogs-hq-pending-details)
// omit it — consumers should fall back to PendingReviewIDs in that case.
type HQCompletenessBlock struct {
	Ready                bool                    `json:"ready"`
	PendingReviewIDs     []string                `json:"pending_review_ids"`
	PendingReviewDetails []HQPendingReviewDetail `json:"pending_review_details,omitempty"`
	UnlinkedLineItemIDs  []string                `json:"unlinked_line_item_ids"`
}

// HQPendingReviewDetail mirrors HQ's pending_purchases row, scoped to
// the fields the sales-processor failure banner needs to render a
// human-readable row.
type HQPendingReviewDetail struct {
	ID        string  `json:"id"`
	BankTxID  string  `json:"bank_tx_id"`
	Vendor    string  `json:"vendor"`     // "" when the receipt parser couldn't extract one
	EventDate string  `json:"event_date"` // YYYY-MM-DD; HQ falls back to created_at::date
	BankTotal float64 `json:"bank_total"`
	Reason    *string `json:"reason,omitempty"`
}

type HQClient struct {
	apiToken   string
	baseURL    string
	httpClient *http.Client
}

// BaseURL returns the HQ base URL the client was configured with
// (HQ_BASE_URL or the default). Used by callers that need to construct
// operator-facing UI links (e.g. /inventory.html) alongside API calls.
func (c *HQClient) BaseURL() string { return c.baseURL }

// NewHQClientFromEnv constructs an HQClient using HQ_INVENTORY_SERVICE_TOKEN
// and HQ_BASE_URL from the environment. Returns (nil, nil) when the token
// is unset — callers should treat that as "COGS integration not configured"
// and skip the related work without erroring.
func NewHQClientFromEnv() (*HQClient, error) {
	token := strings.TrimSpace(os.Getenv("HQ_INVENTORY_SERVICE_TOKEN"))
	if token == "" {
		return nil, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("HQ_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = hqDefaultBaseURL
	}
	return &HQClient{
		apiToken:   token,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// GetPeriodSummary calls GET /api/v1/inventory/period-summary?from&to.
// Both dates are formatted as YYYY-MM-DD and treated as inclusive by HQ.
func (c *HQClient) GetPeriodSummary(from, to time.Time) (*HQPeriodSummary, error) {
	q := url.Values{}
	q.Set("from", from.Format("2006-01-02"))
	q.Set("to", to.Format("2006-01-02"))

	endpoint := fmt.Sprintf("%s/api/v1/inventory/period-summary?%s", c.baseURL, q.Encode())
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("hq GetPeriodSummary: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hq GetPeriodSummary: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hq GetPeriodSummary: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var summary HQPeriodSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return nil, fmt.Errorf("hq GetPeriodSummary: failed to decode response: %w", err)
	}
	return &summary, nil
}
