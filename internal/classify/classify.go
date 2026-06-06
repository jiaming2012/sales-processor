// Package classify implements the Mercury transaction classification pipeline.
//
// Flow: Pull → (Claude analyzes via claude CLI) → Apply.
//
//	Pull writes  output/classify/transactions_<toDate>.json
//	             (snapshot of card txs in period + the category list)
//	Analyze: caller invokes  claude "<prompt referencing the cache file>"
//	             which writes output/classify/proposals_<toDate>.json
//	Apply reads  proposals_<toDate>.json,  PATCHes Mercury,
//	             renames the file  proposals_<toDate>.applied.json.
//
// The snapshot file is the only source of truth Apply consults for the
// "current" categoryId, so a stale snapshot can cause spurious re-PATCHes
// or skip-checks. In normal operation Pull and Apply run minutes apart in
// the same main.go invocation; staleness isn't a real concern.
package classify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jiaming2012/sales-processor/service/external"
)

// CacheDir is where transactions_/proposals_ files live. Relative to the
// process working directory — sales-processor runs from the repo root.
const CacheDir = "output/classify"

// Period is the YYYY-MM-DD inclusive date window covered by a snapshot.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// TransactionsFile is the snapshot Pull writes for Claude (and Apply) to read.
type TransactionsFile struct {
	Period       Period                            `json:"period"`
	Categories   []external.MercuryCategory        `json:"categories"`
	Transactions []external.MercuryTransactionLite `json:"transactions"`
}

// ProposalsFile is what Claude writes during the analyze step.
type ProposalsFile struct {
	Period    Period     `json:"period"`
	Proposals []Proposal `json:"proposals"`
}

// Proposal is Claude's classification verdict for one transaction.
//
// CategoryID must be drawn from TransactionsFile.Categories (Apply rejects
// otherwise). Confidence is informational — Apply does not gate on it.
// Reasoning is appended to the note written back to Mercury.
type Proposal struct {
	TransactionID string  `json:"transactionId"`
	CategoryID    string  `json:"categoryId"`
	CategoryName  string  `json:"categoryName"`
	Confidence    float64 `json:"confidence"`
	Reasoning     string  `json:"reasoning"`
}

// ApplyStats summarises what Apply did.
type ApplyStats struct {
	Patched int
	Skipped int // tx already had this categoryId — no-op
}

// Pull fetches every card transaction in [from, to] from Mercury, snapshots
// the current category state, ensures all canonical categories exist, and
// writes the snapshot file. Returns the file path.
//
// No filter on existing categoryData — Claude re-classifies every transaction
// because Mercury sometimes auto-classifies wrong and we want Claude's view
// to win. Apply still skips PATCHes where current == proposed, so this is
// efficient in the common case.
func Pull(client *external.MercuryClient, from, to time.Time) (string, error) {
	byName, err := EnsureCategories(client)
	if err != nil {
		return "", err
	}

	rawTxs, err := client.ListTransactionsInPeriod(from, to)
	if err != nil {
		return "", fmt.Errorf("classify Pull: list transactions: %w", err)
	}

	txs := filterSentTxs(rawTxs)

	snapshot := TransactionsFile{
		Period: Period{
			From: from.Format("2006-01-02"),
			To:   to.Format("2006-01-02"),
		},
		Categories:   SortedCategories(byName),
		Transactions: txs,
	}

	if err := os.MkdirAll(CacheDir, 0o755); err != nil {
		return "", fmt.Errorf("classify Pull: mkdir cache: %w", err)
	}
	path := transactionsPath(to)
	if err := writeJSON(path, snapshot); err != nil {
		return "", fmt.Errorf("classify Pull: write snapshot: %w", err)
	}
	return path, nil
}

// Apply reads the snapshot + proposals files for the given pay period,
// validates every proposal against the snapshot's category list, PATCHes
// Mercury for proposals whose categoryId differs from the snapshot's
// currently-assigned one, and renames the proposals file to ...applied.json
// on success.
//
// Fails hard on the first PATCH error and on any proposal that references
// an unknown transactionId or categoryId. Snapshot transactions that have
// no matching proposal are also fatal — Claude is the authority and must
// produce a verdict for every snapshot transaction (use "Other / Needs
// Review" when uncertain).
func Apply(client *external.MercuryClient, to time.Time) (ApplyStats, error) {
	var stats ApplyStats

	var txsFile TransactionsFile
	if err := readJSON(transactionsPath(to), &txsFile); err != nil {
		return stats, fmt.Errorf("classify Apply: read snapshot: %w", err)
	}

	txIndex := make(map[string]external.MercuryTransactionLite, len(txsFile.Transactions))
	for _, t := range txsFile.Transactions {
		txIndex[t.ID] = t
	}
	validCats := make(map[string]string, len(txsFile.Categories))
	for _, c := range txsFile.Categories {
		validCats[c.ID] = c.Name
	}

	propPath := proposalsPath(to)
	var propFile ProposalsFile
	if err := readJSON(propPath, &propFile); err != nil {
		return stats, fmt.Errorf("classify Apply: read proposals at %s: %w", propPath, err)
	}

	// Every snapshot tx must have exactly one proposal — Claude has to pick
	// a bucket (or "Other / Needs Review") for everything.
	proposalsByTxID := make(map[string]Proposal, len(propFile.Proposals))
	for _, p := range propFile.Proposals {
		if _, dup := proposalsByTxID[p.TransactionID]; dup {
			return stats, fmt.Errorf("classify Apply: duplicate proposal for tx %s", p.TransactionID)
		}
		proposalsByTxID[p.TransactionID] = p
	}
	for _, t := range txsFile.Transactions {
		if _, ok := proposalsByTxID[t.ID]; !ok {
			return stats, fmt.Errorf("classify Apply: tx %s (%s) has no proposal — Claude must classify every snapshot transaction (use 'Other / Needs Review' if unsure)", t.ID, t.BankDescription)
		}
	}

	for _, p := range propFile.Proposals {
		tx, ok := txIndex[p.TransactionID]
		if !ok {
			return stats, fmt.Errorf("classify Apply: proposal references unknown tx %s — not in snapshot", p.TransactionID)
		}
		if _, ok := validCats[p.CategoryID]; !ok {
			return stats, fmt.Errorf("classify Apply: tx %s proposal uses unknown categoryId %s — not in snapshot's categories list", p.TransactionID, p.CategoryID)
		}

		// Idempotency: Mercury already has this category, no PATCH needed.
		if tx.CategoryData != nil && tx.CategoryData.ID == p.CategoryID {
			stats.Skipped++
			continue
		}

		note := formatNote(p)
		if err := client.UpdateTransaction(p.TransactionID, p.CategoryID, note); err != nil {
			return stats, fmt.Errorf("classify Apply: PATCH tx %s (%s): %w", p.TransactionID, tx.BankDescription, err)
		}
		stats.Patched++
	}

	appliedPath := strings.TrimSuffix(propPath, ".json") + ".applied.json"
	if err := os.Rename(propPath, appliedPath); err != nil {
		return stats, fmt.Errorf("classify Apply: archive proposals file: %w", err)
	}
	return stats, nil
}

// filterSentTxs keeps every sent transaction regardless of kind — cards,
// wires, ACH, internal transfers, and "other" (Toast deposits). The
// canonical categories include Revenue and Transfer so Claude can label
// the non-expense rows correctly. Pending/failed rows are excluded
// because they aren't real money movement yet.
func filterSentTxs(txs []external.MercuryTransactionLite) []external.MercuryTransactionLite {
	out := make([]external.MercuryTransactionLite, 0, len(txs))
	for _, t := range txs {
		if t.Status != "sent" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func formatNote(p Proposal) string {
	base := fmt.Sprintf("auto-classified by Claude (conf: %.2f)", p.Confidence)
	if r := strings.TrimSpace(p.Reasoning); r != "" {
		return base + " — " + r
	}
	return base
}

func transactionsPath(to time.Time) string {
	return filepath.Join(CacheDir, fmt.Sprintf("transactions_%s.json", to.Format("2006-01-02")))
}

func proposalsPath(to time.Time) string {
	return filepath.Join(CacheDir, fmt.Sprintf("proposals_%s.json", to.Format("2006-01-02")))
}

// TransactionsPath and ProposalsPath are exported so the orchestrator
// (main.go) can verify the Claude analyze step actually wrote the file.
func TransactionsPath(to time.Time) string { return transactionsPath(to) }
func ProposalsPath(to time.Time) string    { return proposalsPath(to) }

// PromptForPeriod returns the prompt to pass to `claude` for the analyze step.
// The prompt is fully self-contained: each `claude` invocation is a fresh
// session with no prior context, so file paths, output schema, and the
// category list all live here.
func PromptForPeriod(to time.Time) string {
	var b strings.Builder
	b.WriteString("You are classifying Mercury bank transactions for a small food truck business.\n\n")
	fmt.Fprintf(&b, "Read this JSON file: %s\n", transactionsPath(to))
	b.WriteString("It has shape: { period, categories[], transactions[] }.\n\n")
	fmt.Fprintf(&b, "Write classifications to: %s\n", proposalsPath(to))
	b.WriteString("Use this exact shape:\n")
	b.WriteString("{\n")
	b.WriteString(`  "period": {"from": "<copy from input>", "to": "<copy from input>"},` + "\n")
	b.WriteString(`  "proposals": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "transactionId": "<tx.id from snapshot>",` + "\n")
	b.WriteString(`      "categoryId": "<one of categories[].id from snapshot>",` + "\n")
	b.WriteString(`      "categoryName": "<matching categories[].name>",` + "\n")
	b.WriteString(`      "confidence": 0.0,` + "  // float 0–1\n")
	b.WriteString(`      "reasoning": "<one sentence>"` + "\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")

	b.WriteString("RULES:\n")
	b.WriteString("1. Produce EXACTLY ONE proposal per transaction in the snapshot. No more, no fewer.\n")
	b.WriteString("2. categoryId MUST be drawn from the snapshot's categories[] list — don't invent ids.\n")
	b.WriteString("3. Use \"Other / Needs Review\" when you're uncertain. Don't guess.\n")
	b.WriteString("4. Classify based on bankDescription, merchant.name, amount, and mercuryCategory.\n")
	b.WriteString("5. Output VALID JSON only — no markdown fences, no commentary, no trailing text.\n\n")

	b.WriteString("Category meanings:\n")
	for _, c := range CanonicalCategories {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Description)
	}
	return b.String()
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
