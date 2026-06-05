package transferledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Status string

const (
	StatusSent   Status = "sent"
	StatusFailed Status = "failed"
)

type Kind string

const (
	KindSalesTax      Kind = "sales_tax"
	KindDeferredTaxes Kind = "deferred_taxes"
	KindRentHold      Kind = "rent_hold"
)

type Entry struct {
	Status         Status    `json:"status"`
	Amount         float64   `json:"amount"`
	Method         string    `json:"method"`
	Destination    string    `json:"destination"`
	SentAt         time.Time `json:"sentAt,omitempty"`
	Error          string    `json:"error,omitempty"`
	IdempotencyKey string    `json:"idempotencyKey"`
}

type Ledger struct {
	PayPeriod string         `json:"payPeriod"`
	Transfers map[Kind]Entry `json:"transfers"`

	path string
}

func pathFor(dir, payPeriod string) string {
	return filepath.Join(dir, fmt.Sprintf("transfers_%s.json", payPeriod))
}

func Load(dir, payPeriod string) (*Ledger, error) {
	path := pathFor(dir, payPeriod)
	l := &Ledger{
		PayPeriod: payPeriod,
		Transfers: map[Kind]Entry{},
		path:      path,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("read transfer ledger %s: %w", path, err)
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("parse transfer ledger %s: %w", path, err)
	}
	if l.Transfers == nil {
		l.Transfers = map[Kind]Entry{}
	}
	l.path = path
	return l, nil
}

func (l *Ledger) Path() string { return l.path }

func (l *Ledger) Save() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0755); err != nil {
		return fmt.Errorf("create transfer ledger dir: %w", err)
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transfer ledger: %w", err)
	}
	if err := os.WriteFile(l.path, data, 0644); err != nil {
		return fmt.Errorf("write transfer ledger %s: %w", l.path, err)
	}
	return nil
}

// Sent reports whether a transfer of the given kind has already been
// successfully sent.
func (l *Ledger) Sent(kind Kind) (Entry, bool) {
	e, ok := l.Transfers[kind]
	return e, ok && e.Status == StatusSent
}

func (l *Ledger) Record(kind Kind, entry Entry) {
	l.Transfers[kind] = entry
}

func (l *Ledger) Clear(kinds ...Kind) {
	for _, k := range kinds {
		delete(l.Transfers, k)
	}
}

// IdempotencyKey returns the Mercury idempotency key for a transfer. retry
// appends a unix timestamp so a forced re-send is accepted as a new
// transaction.
func IdempotencyKey(payPeriod string, kind Kind, retry bool) string {
	key := fmt.Sprintf("%s-%s", payPeriod, kind)
	if retry {
		key = fmt.Sprintf("%s-retry-%d", key, time.Now().Unix())
	}
	return key
}

func AllKinds() []Kind {
	return []Kind{KindSalesTax, KindDeferredTaxes, KindRentHold}
}

// ParseKinds parses a comma-separated list (e.g. "sales_tax,rent_hold" or
// the literal "all").
func ParseKinds(s string) ([]Kind, error) {
	if s == "" {
		return nil, nil
	}
	if s == "all" {
		return AllKinds(), nil
	}
	var out []Kind
	for _, raw := range strings.Split(s, ",") {
		k := Kind(strings.TrimSpace(raw))
		if !validKind(k) {
			return nil, fmt.Errorf("invalid transfer kind %q (valid: sales_tax, deferred_taxes, rent_hold, all)", k)
		}
		out = append(out, k)
	}
	return out, nil
}

func validKind(k Kind) bool {
	for _, valid := range AllKinds() {
		if k == valid {
			return true
		}
	}
	return false
}
