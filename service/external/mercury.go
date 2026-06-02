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
