package external

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func readErrorBody(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return ""
	}
	return string(body)
}

type MercuryAccount struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type mercuryListAccountsResponse struct {
	Accounts []MercuryAccount `json:"accounts"`
}

type MercuryTransferRequest struct {
	FromAccountID  string
	ToAccountID    string
	ToAccountName  string // display only — not sent to Mercury
	Amount         float64
	Note           string
	IdempotencyKey string // if empty, one is generated
}

type mercuryTransferPayload struct {
	SourceAccountID    string  `json:"sourceAccountId"`
	DestinationAccountID string  `json:"destinationAccountId"`
	Amount             float64 `json:"amount"`
	Note               string  `json:"note,omitempty"`
	IdempotencyKey     string  `json:"idempotencyKey"`
}

type MercuryRecipient struct {
	ID                      string                       `json:"id"`
	Name                    string                       `json:"name"`
	Status                  string                       `json:"status"`
	DefaultPaymentMethod    string                       `json:"defaultPaymentMethod,omitempty"`
	ElectronicRoutingInfo   *MercuryRecipientRoutingInfo `json:"electronicRoutingInfo,omitempty"`
	DomesticWireRoutingInfo *MercuryRecipientRoutingInfo `json:"domesticWireRoutingInfo,omitempty"`
}

type MercuryRecipientRoutingInfo struct {
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber"`
	BankName      string `json:"bankName,omitempty"`
}

// preferredRoutingInfo returns the recipient's electronic (ACH) routing info if
// present, falling back to domestic wire routing info. Used purely for display
// — the actual payment method is chosen separately.
func (r MercuryRecipient) preferredRoutingInfo() *MercuryRecipientRoutingInfo {
	if r.ElectronicRoutingInfo != nil {
		return r.ElectronicRoutingInfo
	}
	return r.DomesticWireRoutingInfo
}

// AccountLast4 returns the last 4 digits of the recipient's account number
// from whichever routing info is available, or an empty string if neither is set.
func (r MercuryRecipient) AccountLast4() string {
	info := r.preferredRoutingInfo()
	if info == nil {
		return ""
	}
	num := info.AccountNumber
	if len(num) < 4 {
		return num
	}
	return num[len(num)-4:]
}

// BankName returns the recipient's bank name if available.
func (r MercuryRecipient) BankName() string {
	info := r.preferredRoutingInfo()
	if info == nil {
		return ""
	}
	return info.BankName
}

// SupportedMethods returns a human-readable tag indicating which payment
// methods the recipient supports (e.g. "ACH", "Wire", "ACH+Wire"), or an
// empty string if no routing info is set.
func (r MercuryRecipient) SupportedMethods() string {
	hasACH := r.ElectronicRoutingInfo != nil
	hasWire := r.DomesticWireRoutingInfo != nil
	switch {
	case hasACH && hasWire:
		return "ACH+Wire"
	case hasACH:
		return "ACH"
	case hasWire:
		return "Wire"
	default:
		return ""
	}
}

type mercuryListRecipientsResponse struct {
	Recipients []MercuryRecipient `json:"recipients"`
}

const (
	MercuryPaymentMethodACH          = "ach"
	MercuryPaymentMethodDomesticWire = "domesticWire"

	// SendMoneyPurpose categories. Only the ones we actually use are listed;
	// see Mercury API docs for the full set.
	MercuryPurposeTransferToMyExternalAccount = "transferToMyExternalAccount"
	MercuryPurposeEmployee                    = "employee"
)

type MercurySendMoneyPurposeSimple struct {
	Category       string `json:"category"`
	AdditionalInfo string `json:"additionalInfo,omitempty"`
}

type MercurySendMoneyPurpose struct {
	Simple MercurySendMoneyPurposeSimple `json:"simple"`
}

type MercuryExternalTransferRequest struct {
	FromAccountID  string
	RecipientID    string
	Amount         float64
	Note           string
	PaymentMethod  string                   // "ach" or "domesticWire"
	Purpose        *MercurySendMoneyPurpose // required when PaymentMethod is "domesticWire"
	IdempotencyKey string                   // if empty, one is generated
}

type mercuryExternalTransferPayload struct {
	RecipientID    string                   `json:"recipientId"`
	Amount         float64                  `json:"amount"`
	PaymentMethod  string                   `json:"paymentMethod"`
	Note           string                   `json:"note,omitempty"`
	IdempotencyKey string                   `json:"idempotencyKey"`
	Purpose        *MercurySendMoneyPurpose `json:"purpose,omitempty"`
}

const (
	mercuryProductionBaseURL = "https://api.mercury.com/api/v1"
	mercurySandboxBaseURL    = "https://api-sandbox.mercury.com/api/v1"
)

type MercuryClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func NewMercuryClient(apiKey string, sandbox bool) *MercuryClient {
	baseURL := mercuryProductionBaseURL
	if sandbox {
		baseURL = mercurySandboxBaseURL
	}
	return &MercuryClient{
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *MercuryClient) ListAccounts() ([]MercuryAccount, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/accounts", nil)
	if err != nil {
		return nil, fmt.Errorf("mercury ListAccounts: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury ListAccounts: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mercury ListAccounts: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var envelope mercuryListAccountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("mercury ListAccounts: failed to decode response: %w", err)
	}

	var active []MercuryAccount
	for _, acct := range envelope.Accounts {
		if acct.Status == "active" {
			active = append(active, acct)
		}
	}

	return active, nil
}

func (c *MercuryClient) CreateInternalTransfer(transfer MercuryTransferRequest) error {
	idempotencyKey := transfer.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("%s-%s-%.2f-%d", transfer.FromAccountID, transfer.ToAccountID, transfer.Amount, time.Now().UnixNano())
	}

	payload := mercuryTransferPayload{
		SourceAccountID:    transfer.FromAccountID,
		DestinationAccountID: transfer.ToAccountID,
		Amount:         transfer.Amount,
		Note:           transfer.Note,
		IdempotencyKey: idempotencyKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mercury CreateInternalTransfer: failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/transfer", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mercury CreateInternalTransfer: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mercury CreateInternalTransfer: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("mercury CreateInternalTransfer: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

func (c *MercuryClient) ListRecipients() ([]MercuryRecipient, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/recipients", nil)
	if err != nil {
		return nil, fmt.Errorf("mercury ListRecipients: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury ListRecipients: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mercury ListRecipients: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var envelope mercuryListRecipientsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("mercury ListRecipients: failed to decode response: %w", err)
	}

	var active []MercuryRecipient
	for _, r := range envelope.Recipients {
		if r.Status == "" || r.Status == "active" {
			active = append(active, r)
		}
	}

	return active, nil
}

func (c *MercuryClient) CreateExternalTransfer(transfer MercuryExternalTransferRequest) error {
	if transfer.PaymentMethod != MercuryPaymentMethodACH && transfer.PaymentMethod != MercuryPaymentMethodDomesticWire {
		return fmt.Errorf("mercury CreateExternalTransfer: invalid paymentMethod %q (must be %q or %q)", transfer.PaymentMethod, MercuryPaymentMethodACH, MercuryPaymentMethodDomesticWire)
	}
	if transfer.PaymentMethod == MercuryPaymentMethodDomesticWire && transfer.Purpose == nil {
		return fmt.Errorf("mercury CreateExternalTransfer: purpose is required for domesticWire transfers")
	}

	idempotencyKey := transfer.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("%s-%s-%.2f-%d", transfer.FromAccountID, transfer.RecipientID, transfer.Amount, time.Now().UnixNano())
	}

	payload := mercuryExternalTransferPayload{
		RecipientID:    transfer.RecipientID,
		Amount:         transfer.Amount,
		PaymentMethod:  transfer.PaymentMethod,
		Note:           transfer.Note,
		IdempotencyKey: idempotencyKey,
		Purpose:        transfer.Purpose,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mercury CreateExternalTransfer: failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/account/%s/transactions", c.baseURL, transfer.FromAccountID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mercury CreateExternalTransfer: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mercury CreateExternalTransfer: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("mercury CreateExternalTransfer: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// -----------------------------------------------------------------------------
// Transaction listing + categorization
//
// Used by the classify pipeline (cmd/classify, internal/classify). Lists card
// transactions in a date range, lists/creates org-wide expense categories, and
// PATCHes a transaction's category + note. See docs/classification.md for the
// full flow.
// -----------------------------------------------------------------------------

const mercuryListTransactionsLimit = 1000

// MercuryTransactionLite is the subset of Mercury's transaction fields the
// classify pipeline reads. Mercury returns many more fields; unmodelled ones
// are silently dropped by the JSON decoder, which is fine — adding new fields
// later only requires updating this struct.
type MercuryTransactionLite struct {
	ID              string                 `json:"id"`
	Amount          float64                `json:"amount"`
	BankDescription string                 `json:"bankDescription"`
	Status          string                 `json:"status"`
	Kind            string                 `json:"kind"`
	Note            string                 `json:"note"`
	CreatedAt       string                 `json:"createdAt"`
	MercuryCategory string                 `json:"mercuryCategory"` // Mercury's built-in enum (Restaurants, Software, ...)
	CategoryData    *MercuryCategoryData   `json:"categoryData"`    // null when no custom category set
	Merchant        *MercuryMerchantData   `json:"merchant"`        // null for non-card transactions or where Mercury hasn't resolved a merchant
	DashboardLink   string                 `json:"dashboardLink"`
	// CardID is the id of the card behind this transaction — present on card
	// payments and refunds (debit or credit), null/empty otherwise. Join it
	// against MercuryCard.ID (from ListCards) to attribute a purchase to the
	// cardholder who made it.
	CardID string `json:"cardId"`
}

// MercuryCategoryData is the org-wide custom category currently assigned to a
// transaction. CategoryID is what /transaction/{id} PATCH expects.
type MercuryCategoryData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MercuryMerchantData is Mercury's resolved merchant identity for a card
// transaction. Useful as context for the classifier; not all transactions
// have it.
type MercuryMerchantData struct {
	Name string `json:"name"`
}

// MercuryCategory is an org-wide expense category. Returned by GET /categories
// and POST /categories.
type MercuryCategory struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	VisibleForCardSpend    bool   `json:"visibleForCardSpend"`
	VisibleForOther        bool   `json:"visibleForOther"`
	VisibleForReimbursements bool `json:"visibleForReimbursements"`
}

type mercuryListTransactionsResponse struct {
	Transactions []MercuryTransactionLite `json:"transactions"`
	Total        int                      `json:"total"`
}

type mercuryListCategoriesResponse struct {
	Categories []MercuryCategory `json:"categories"`
}

// mercuryCreateCategoryPayload is the body of POST /categories. Mercury
// requires all three visibility flags — omitting any of them returns
// `invalidApiArgs` (the spec says they're optional but the server
// disagrees). We default everything to true so the seeded categories
// show up across card spend, reimbursements, and other transaction types.
type mercuryCreateCategoryPayload struct {
	Name                     string `json:"name"`
	VisibleForCardSpend      bool   `json:"visibleForCardSpend"`
	VisibleForOther          bool   `json:"visibleForOther"`
	VisibleForReimbursements bool   `json:"visibleForReimbursements"`
}

type mercuryUpdateTransactionPayload struct {
	CategoryID *string `json:"categoryId,omitempty"`
	Note       *string `json:"note,omitempty"`
}

// IsSupportedCardKind reports whether a Mercury transaction Kind is one
// of the card/debit-card kinds HQ's receipt worker ingests into
// purchase_events / pending_purchases. The payroll gap check uses this
// to filter Mercury's transaction list before diffing against HQ's
// tracked_bank_tx_ids. Keep in sync with
// hq/backend/internal/receipt/mercury.go:isSupportedKind.
func IsSupportedCardKind(kind string) bool {
	switch kind {
	case "creditCardTransaction", "debitCardTransaction",
		"creditCardCredit", "debitCardCredit":
		return true
	}
	return false
}

// MercuryHQGap returns the Mercury transactions in txns that (a) are
// supported card kinds, (b) have status="sent", and (c) are not present
// in trackedIDs. It's the read-only diff at the core of the payroll
// gap check — Mercury knows every card swipe immediately; HQ only knows
// what its receipt worker has polled. A non-empty return means HQ has
// missed something and the payroll PDF would silently undercount COGS.
func MercuryHQGap(txns []MercuryTransactionLite, trackedIDs []string) []MercuryTransactionLite {
	trackedSet := make(map[string]struct{}, len(trackedIDs))
	for _, id := range trackedIDs {
		trackedSet[id] = struct{}{}
	}
	var gap []MercuryTransactionLite
	for _, tx := range txns {
		if tx.Status != "sent" {
			continue
		}
		if !IsSupportedCardKind(tx.Kind) {
			continue
		}
		if _, seen := trackedSet[tx.ID]; seen {
			continue
		}
		gap = append(gap, tx)
	}
	return gap
}

// CardholderByBankTx maps a Mercury transaction id (which is HQ's
// bank_tx_id) to a human label for the cardholder who made the purchase.
// It joins each card transaction's CardID against the card list: a card
// with a NameOnCard renders as that name (e.g. "Jamal Cole"); a card with
// only a last four renders as "card ••1234". Transactions with no card
// (non-card kinds, or a CardID that doesn't resolve to a labelled card)
// are omitted, so a missing key means "cardholder unknown" — callers
// should treat the map as best-effort enrichment, not a complete index.
func CardholderByBankTx(txns []MercuryTransactionLite, cards []MercuryCard) map[string]string {
	labelByCardID := make(map[string]string, len(cards))
	for _, card := range cards {
		switch {
		case card.NameOnCard != "":
			labelByCardID[card.ID] = card.NameOnCard
		case card.LastFour != "":
			labelByCardID[card.ID] = "card ••" + card.LastFour
		}
	}

	out := make(map[string]string)
	for _, tx := range txns {
		if tx.CardID == "" {
			continue
		}
		if label, ok := labelByCardID[tx.CardID]; ok {
			out[tx.ID] = label
		}
	}
	return out
}

// ListTransactionsInPeriod returns every Mercury transaction with createdAt
// in [from, to] inclusive. Both dates are formatted YYYY-MM-DD. Errors hard
// if the response hits the page limit — Mercury's /transactions endpoint
// caps each response and this code does not yet implement cursor pagination.
// Volume is well under the limit for current YumYums usage; revisit when
// transaction counts grow.
func (c *MercuryClient) ListTransactionsInPeriod(from, to time.Time) ([]MercuryTransactionLite, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/transactions", nil)
	if err != nil {
		return nil, fmt.Errorf("mercury ListTransactionsInPeriod: failed to create request: %w", err)
	}
	q := req.URL.Query()
	q.Set("start", from.Format("2006-01-02"))
	q.Set("end", to.Format("2006-01-02"))
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury ListTransactionsInPeriod: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mercury ListTransactionsInPeriod: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var envelope mercuryListTransactionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("mercury ListTransactionsInPeriod: failed to decode response: %w", err)
	}
	if len(envelope.Transactions) >= mercuryListTransactionsLimit {
		return nil, fmt.Errorf("mercury ListTransactionsInPeriod: response hit page limit (%d) — implement pagination", mercuryListTransactionsLimit)
	}
	return envelope.Transactions, nil
}

// MercuryCard is the subset of a Mercury card the payroll code needs to
// attribute a card transaction to the person who made it. Returned by
// GET /cards. NameOnCard is the cardholder printed on the card (e.g.
// "Jamal Cole"); LastFour is the card's last four PAN digits, used as a
// fallback label when NameOnCard is blank.
type MercuryCard struct {
	ID         string `json:"id"`
	NameOnCard string `json:"nameOnCard"`
	LastFour   string `json:"lastFour"`
	AccountID  string `json:"accountId"`
	Status     string `json:"status"`
}

type mercuryListCardsResponse struct {
	Cards []MercuryCard `json:"cards"`
}

// ListCards returns every card in the workspace. Used to resolve a
// transaction's CardID to the cardholder's name for operator-facing
// messages (e.g. the "who bought this unreceipted item" line in the HQ
// completeness failure banner).
//
// Only the first page is read. YumYums has a handful of cards — far under
// any page limit — so cursor pagination is intentionally not implemented;
// revisit if the card count ever grows large.
func (c *MercuryClient) ListCards() ([]MercuryCard, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/cards", nil)
	if err != nil {
		return nil, fmt.Errorf("mercury ListCards: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury ListCards: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mercury ListCards: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var envelope mercuryListCardsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("mercury ListCards: failed to decode response: %w", err)
	}
	return envelope.Cards, nil
}

// ListCategories returns every org-wide custom expense category.
func (c *MercuryClient) ListCategories() ([]MercuryCategory, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/categories", nil)
	if err != nil {
		return nil, fmt.Errorf("mercury ListCategories: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury ListCategories: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mercury ListCategories: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var envelope mercuryListCategoriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("mercury ListCategories: failed to decode response: %w", err)
	}
	return envelope.Categories, nil
}

// CreateCategory creates a new org-wide custom expense category with the
// given name. Returns the created category (Mercury assigns the id).
// All three visibility flags default to true so the category is usable
// against every transaction kind.
func (c *MercuryClient) CreateCategory(name string) (*MercuryCategory, error) {
	body, err := json.Marshal(mercuryCreateCategoryPayload{
		Name:                     name,
		VisibleForCardSpend:      true,
		VisibleForOther:          true,
		VisibleForReimbursements: true,
	})
	if err != nil {
		return nil, fmt.Errorf("mercury CreateCategory: failed to marshal payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/categories", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mercury CreateCategory: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mercury CreateCategory: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("mercury CreateCategory: %d: %s", resp.StatusCode, readErrorBody(resp))
	}

	var category MercuryCategory
	if err := json.NewDecoder(resp.Body).Decode(&category); err != nil {
		return nil, fmt.Errorf("mercury CreateCategory: failed to decode response: %w", err)
	}
	return &category, nil
}

// UpdateTransaction PATCHes a single transaction's category and note.
// Pass empty strings to leave a field unchanged (Mercury's API treats an
// omitted field as "no change"; sending an explicit null clears the field —
// neither is what we want here). Both fields are normally set together by
// the classify pipeline.
func (c *MercuryClient) UpdateTransaction(transactionID, categoryID, note string) error {
	payload := mercuryUpdateTransactionPayload{}
	if categoryID != "" {
		payload.CategoryID = &categoryID
	}
	if note != "" {
		payload.Note = &note
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mercury UpdateTransaction: failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/transaction/%s", c.baseURL, transactionID)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mercury UpdateTransaction: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mercury UpdateTransaction: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("mercury UpdateTransaction %s: %d: %s", transactionID, resp.StatusCode, readErrorBody(resp))
	}
	return nil
}
