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
	FromAccountID string
	ToAccountID   string
	Amount        float64
	Note          string
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
)

type MercurySendMoneyPurposeSimple struct {
	Category       string `json:"category"`
	AdditionalInfo string `json:"additionalInfo,omitempty"`
}

type MercurySendMoneyPurpose struct {
	Simple MercurySendMoneyPurposeSimple `json:"simple"`
}

type MercuryExternalTransferRequest struct {
	FromAccountID string
	RecipientID   string
	Amount        float64
	Note          string
	PaymentMethod string                   // "ach" or "domesticWire"
	Purpose       *MercurySendMoneyPurpose // required when PaymentMethod is "domesticWire"
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
	idempotencyKey := fmt.Sprintf("%s-%s-%.2f-%d", transfer.FromAccountID, transfer.ToAccountID, transfer.Amount, time.Now().UnixNano())

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

	idempotencyKey := fmt.Sprintf("%s-%s-%.2f-%d", transfer.FromAccountID, transfer.RecipientID, transfer.Amount, time.Now().UnixNano())

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
