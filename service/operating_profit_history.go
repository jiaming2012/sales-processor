package service

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"jiaming2012/sales-processor/models"
	"jiaming2012/sales-processor/sftp"
)

// OperatingProfitHistoryFileName is the constant filename used on both the
// SFTP side and the local cache. Kept stable so re-runs and backfills find
// the same file across environments.
const OperatingProfitHistoryFileName = "operating_profit_history.json"

// OperatingProfitHistoryStore handles the SFTP-first / local-fallback I/O
// for the operating-profit history document. The shape mirrors the way
// fetchToastCSVReports treats OrderDetails.csv: attempt remote, fall back
// to a local cache, and write any successful remote read back to the
// cache so subsequent runs survive offline / SFTP-degraded conditions.
type OperatingProfitHistoryStore struct {
	// Client is the (already-connected) SFTP client. May be nil — in that
	// case Load/Save operate purely against the local cache and the
	// returned status describes the missing cloud sync.
	Client *sftp.Client
	// RemotePath is the absolute SFTP path of the history file, e.g.
	// /113866/operating_profit_history.json.
	RemotePath string
	// LocalPath is the on-disk cache mirror of the remote file.
	LocalPath string
}

// Load returns the current history together with a human-readable status
// string describing where the data came from. The status is empty on the
// happy path (remote download succeeded) and populated whenever a
// degraded path was taken so the caller can surface it in the report.
//
// A history file that does not exist on either side is treated as an
// empty history (not an error) — this is the first-run case.
func (s *OperatingProfitHistoryStore) Load() (*models.OperatingProfitHistory, string, error) {
	var (
		data       []byte
		status     string
		fromRemote bool
	)

	if s.Client != nil {
		remote, err := s.Client.Download(s.RemotePath)
		if err == nil {
			defer remote.Close()
			data, err = io.ReadAll(remote)
			if err != nil {
				return nil, "", fmt.Errorf("read remote history %s: %w", s.RemotePath, err)
			}
			fromRemote = true
		} else {
			status = fmt.Sprintf("could not read history from SFTP (%v) — using local cache", err)
		}
	} else {
		status = "SFTP unavailable — using local cache"
	}

	if !fromRemote {
		local, err := os.ReadFile(s.LocalPath)
		if err != nil {
			if os.IsNotExist(err) {
				return &models.OperatingProfitHistory{}, status, nil
			}
			return nil, status, fmt.Errorf("read local history %s: %w", s.LocalPath, err)
		}
		data = local
	}

	h, err := models.LoadOperatingProfitHistory(bytes.NewReader(data))
	if err != nil {
		return nil, status, err
	}

	if fromRemote {
		// Keep the local cache fresh so a future SFTP outage degrades to the
		// most recent known state.
		if err := os.MkdirAll(filepath.Dir(s.LocalPath), 0755); err != nil {
			return h, fmt.Sprintf("cached SFTP history could not be written locally: %v", err), nil
		}
		if err := os.WriteFile(s.LocalPath, data, 0644); err != nil {
			return h, fmt.Sprintf("cached SFTP history could not be written locally: %v", err), nil
		}
	}

	return h, status, nil
}

// Save serialises the history to the local cache and then attempts to
// mirror it to SFTP. The local write is the source of truth — a remote
// failure is reported as a status string but does not bubble up as an
// error, so the report still ships even when the SFTP user is read-only.
func (s *OperatingProfitHistoryStore) Save(h *models.OperatingProfitHistory) (string, error) {
	buf := &bytes.Buffer{}
	if err := h.Save(buf); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(s.LocalPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir for local history %s: %w", s.LocalPath, err)
	}
	if err := os.WriteFile(s.LocalPath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write local history %s: %w", s.LocalPath, err)
	}

	if s.Client == nil {
		return "SFTP unavailable — history saved locally only", nil
	}

	// CreateWriteOnly (not Create) because AWS Transfer Family's S3
	// backend rejects O_RDWR — the standard Create returns
	// SSH_FX_OP_UNSUPPORTED here.
	remote, err := s.Client.CreateWriteOnly(s.RemotePath)
	if err != nil {
		return fmt.Sprintf("SFTP upload skipped (%v) — history saved locally only", err), nil
	}
	defer remote.Close()
	if _, err := remote.Write(buf.Bytes()); err != nil {
		return fmt.Sprintf("SFTP upload failed (%v) — history saved locally only", err), nil
	}
	return "", nil
}

// BackfillOperatingProfitFromPDFs seeds the history by parsing the most
// recent n PDFs in payrollDir (typically output/payroll/). Returns the
// number of entries added together with a status message describing any
// degraded path (missing pdftotext, partial parse, etc.). Existing
// entries are preserved — Upsert replaces by week-ending date so the
// most authoritative source wins.
//
// We shell out to `pdftotext -layout` because the PDFs are written with
// gofpdf and have no embedded text logic of our own — the layout flag
// preserves the label/value alignment we render with.
func BackfillOperatingProfitFromPDFs(h *models.OperatingProfitHistory, payrollDir string, n int) (int, string) {
	if n <= 0 {
		return 0, ""
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return 0, "backfill skipped: pdftotext not found in PATH"
	}

	entries, err := os.ReadDir(payrollDir)
	if err != nil {
		return 0, fmt.Sprintf("backfill skipped: cannot read %s: %v", payrollDir, err)
	}

	pdfRe := regexp.MustCompile(`^payroll_(\d{4}-\d{2}-\d{2})\.pdf$`)
	type pdfCandidate struct {
		path       string
		weekEnding string
	}
	var pdfs []pdfCandidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := pdfRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		pdfs = append(pdfs, pdfCandidate{
			path:       filepath.Join(payrollDir, e.Name()),
			weekEnding: m[1],
		})
	}
	sort.Slice(pdfs, func(i, j int) bool {
		return pdfs[i].weekEnding > pdfs[j].weekEnding // newest first
	})
	if len(pdfs) > n {
		pdfs = pdfs[:n]
	}
	if len(pdfs) == 0 {
		return 0, fmt.Sprintf("backfill skipped: no PDFs found in %s", payrollDir)
	}

	added := 0
	var skipped []string
	for _, p := range pdfs {
		// Skip work if this week is already recorded — avoids the cost of
		// shelling out to pdftotext when we already have the entry.
		if h.HasEntry(p.weekEnding) {
			continue
		}
		entry, err := parseOperatingProfitFromPDF(p.path, p.weekEnding)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", filepath.Base(p.path), err))
			continue
		}
		h.Upsert(entry)
		added++
	}

	status := ""
	if len(skipped) > 0 {
		status = "backfill partial: skipped " + strings.Join(skipped, "; ")
	}
	return added, status
}

// pdfFieldRe matches a "Label: $value" line as pdftotext -layout emits
// it. The label column is right-padded with spaces, the dollar sign is
// always present, and the value may carry a leading minus.
var pdfFieldRe = map[string]*regexp.Regexp{
	"net_sales":     regexp.MustCompile(`Net Sales:\s+\$(-?[\d,.]+)`),
	"cogs_excl_tax": regexp.MustCompile(`Cost of Goods Sold:\s+\$(-?[\d,.]+)`),
	"total_labor":   regexp.MustCompile(`Total Labor:\s+\$(-?[\d,.]+)`),
	"cc_fees_summary": regexp.MustCompile(
		// Match the Credit Card Fees line that appears in the Summary
		// section — the daily blocks above carry per-day CC fees we want
		// to ignore. The Summary line is always preceded (within a few
		// lines) by "Net Sales:", but a simpler heuristic is: pick the
		// LAST match in the text — Summary is the final section to use
		// the label.
		`Credit Card Fees:\s+\$(-?[\d,.]+)`,
	),
	"operating_profit": regexp.MustCompile(`Operating Profit\*?:\s+\$(-?[\d,.]+)`),
}

func parseOperatingProfitFromPDF(path, weekEnding string) (models.OperatingProfitEntry, error) {
	cmd := exec.Command("pdftotext", "-layout", path, "-")
	out, err := cmd.Output()
	if err != nil {
		return models.OperatingProfitEntry{}, fmt.Errorf("pdftotext: %w", err)
	}
	text := string(out)

	netSales, ok := extractFloat(pdfFieldRe["net_sales"], text, false)
	if !ok {
		return models.OperatingProfitEntry{}, fmt.Errorf("no Net Sales line")
	}
	totalLabor, ok := extractFloat(pdfFieldRe["total_labor"], text, false)
	if !ok {
		return models.OperatingProfitEntry{}, fmt.Errorf("no Total Labor line")
	}
	// COGS is only printed when HQ data was available — when missing the
	// Operating Profit line is suppressed too, so an entry without COGS
	// is not a valid history row.
	cogs, ok := extractFloat(pdfFieldRe["cogs_excl_tax"], text, false)
	if !ok {
		return models.OperatingProfitEntry{}, fmt.Errorf("no COGS line — older PDF without HQ integration")
	}
	// Take the LAST Credit Card Fees match (Summary section); per-day
	// blocks above also carry this label.
	ccFees, ok := extractFloat(pdfFieldRe["cc_fees_summary"], text, true)
	if !ok {
		return models.OperatingProfitEntry{}, fmt.Errorf("no Credit Card Fees line")
	}
	// Prefer the printed Operating Profit when present; fall back to
	// recomputing from the components. This way a future change to the
	// formula doesn't silently desync the backfill from past reports.
	opProfit, hasOp := extractFloat(pdfFieldRe["operating_profit"], text, false)
	if !hasOp {
		opProfit = netSales - cogs - totalLabor - ccFees
	}

	wkEnd, err := time.Parse("2006-01-02", weekEnding)
	if err != nil {
		return models.OperatingProfitEntry{}, fmt.Errorf("parse week ending %q: %w", weekEnding, err)
	}
	entry := models.MakeOperatingProfitEntry(wkEnd, netSales, cogs, totalLabor, ccFees)
	// Use the value the PDF printed so the backfill matches what the
	// operator already saw (rounding may differ from the recomputation).
	entry.OperatingProfit = opProfit
	return entry, nil
}

func extractFloat(re *regexp.Regexp, text string, last bool) (float64, bool) {
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	m := matches[0]
	if last {
		m = matches[len(matches)-1]
	}
	raw := strings.ReplaceAll(m[1], ",", "")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
