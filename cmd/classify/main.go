// Command classify exposes the classify pipeline phases as standalone
// subcommands for debugging. The weekly run (main.go) calls
// internal/classify.Pull and .Apply directly — this CLI exists so an
// operator can re-run a single phase in isolation when something needs
// inspection or hand-editing.
//
// Usage:
//
//	go run ./cmd/classify pull  --from YYYY-MM-DD --to YYYY-MM-DD
//	go run ./cmd/classify apply --to YYYY-MM-DD
//
// Both subcommands require MERCURY_API_KEY in the environment.
// --sandbox routes Mercury calls through api-sandbox.mercury.com.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"jiaming2012/sales-processor/internal/classify"
	"jiaming2012/sales-processor/service/external"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "pull":
		runPull(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	case "prompt":
		runPrompt(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `classify — Mercury transaction categorization pipeline

Subcommands:
  pull   --from YYYY-MM-DD --to YYYY-MM-DD    Snapshot card txs + categories
  apply  --to YYYY-MM-DD                       PATCH Mercury from proposals file
  prompt --to YYYY-MM-DD                       Print the analyze-step prompt to stdout

pull / apply require MERCURY_API_KEY. Add --sandbox to route calls
through api-sandbox.mercury.com.

Files live in output/classify/transactions_<to>.json and
output/classify/proposals_<to>.json.
`)
}

// runPrompt prints the prompt for the analyze step to stdout — no Mercury
// access required. Used by Taskfile.yml's classify:analyze target so the
// shell can pipe it into the `claude` CLI.
func runPrompt(args []string) {
	fs := flag.NewFlagSet("prompt", flag.ExitOnError)
	toStr := fs.String("to", "", "end of period (YYYY-MM-DD)")
	_ = fs.Parse(args)
	if *toStr == "" {
		fmt.Fprintln(os.Stderr, "prompt: --to is required")
		os.Exit(2)
	}
	to := mustParseDate(*toStr, "--to")
	fmt.Print(classify.PromptForPeriod(to))
}

func runPull(args []string) {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	fromStr := fs.String("from", "", "start of period (YYYY-MM-DD, inclusive)")
	toStr := fs.String("to", "", "end of period (YYYY-MM-DD, inclusive)")
	sandbox := fs.Bool("sandbox", false, "use Mercury sandbox environment")
	_ = fs.Parse(args)

	if *fromStr == "" || *toStr == "" {
		fmt.Fprintln(os.Stderr, "pull: --from and --to are required")
		os.Exit(2)
	}
	from := mustParseDate(*fromStr, "--from")
	to := mustParseDate(*toStr, "--to")

	client := mustMercuryClient(*sandbox)
	path, err := classify.Pull(client, from, to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pull: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote snapshot: %s\n", path)
}

func runApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	toStr := fs.String("to", "", "end of period (YYYY-MM-DD, inclusive) — selects which proposals file to apply")
	sandbox := fs.Bool("sandbox", false, "use Mercury sandbox environment")
	_ = fs.Parse(args)

	if *toStr == "" {
		fmt.Fprintln(os.Stderr, "apply: --to is required")
		os.Exit(2)
	}
	to := mustParseDate(*toStr, "--to")

	client := mustMercuryClient(*sandbox)
	stats, err := classify.Apply(client, to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("apply complete: %d patched, %d skipped (already correct)\n", stats.Patched, stats.Skipped)
}

func mustParseDate(s, flagName string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s must be YYYY-MM-DD, got %q: %v\n", flagName, s, err)
		os.Exit(2)
	}
	return t
}

func mustMercuryClient(sandbox bool) *external.MercuryClient {
	if sandbox {
		_ = godotenv.Load(".env.sandbox")
	} else {
		_ = godotenv.Load()
	}
	apiKey := os.Getenv("MERCURY_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "MERCURY_API_KEY is required (in environment or .env)")
		os.Exit(2)
	}
	return external.NewMercuryClient(apiKey, sandbox)
}
