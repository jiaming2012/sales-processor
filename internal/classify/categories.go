package classify

import (
	"fmt"
	"sort"

	"jiaming2012/sales-processor/service/external"
)

// CanonicalCategory is one entry in the curated list of expense categories
// sales-processor seeds into Mercury and asks Claude to classify against.
// The Description is included in the prompt to Claude so it knows what kinds
// of merchants belong in each bucket.
type CanonicalCategory struct {
	Name        string
	Description string
}

// CanonicalCategories is the v1 chart of accounts for the classifier.
//
// **Names match Mercury's pre-existing default categories where possible**
// (COGS, Rent & Utilities, Software & Subscriptions, etc.) so the classify
// pipeline tags rows under the same buckets Mercury's auto-categorization
// would use — avoiding duplicate-ish categories in the chart of accounts.
// Two entries (Owner Personal, Other / Needs Review) are still custom
// because Mercury has no equivalent.
//
// Edits to this list:
//   - Renaming: Mercury treats the new name as a separate category (the old
//     one stays). Either also rename in Mercury, or delete the old one after
//     re-running the classifier so transactions migrate.
//   - Adding: next classify run will POST /categories to create it; existing
//     transactions don't move until reclassified.
//   - Removing: classifier may still propose the removed name; Apply will
//     reject (categoryId not in pulled list). Remove from Mercury too.
//
// Keep the list short. Ten buckets covers a small restaurant cleanly; more
// granularity belongs in QuickBooks/Xero, not bank-level tags.
var CanonicalCategories = []CanonicalCategory{
	{
		Name:        "COGS",
		Description: "Wholesale and retail food vendors — anything destined for the menu. Examples: Restaurant Depot, Save A Lot, Shoppers Food Warehouse, US Foods, Sysco, Costco (when buying food), local farm purchases.",
	},
	{
		Name:        "Rent & Utilities",
		Description: "Storage units, kitchen/commissary rent, real-estate leases, utility bills, phone/internet for the business. Examples: CubeSmart, Public Storage, commercial kitchen rental, parking lot rent, Mint Mobile, Comcast Business.",
	},
	{
		Name:        "Payroll",
		Description: "Wages and contractor payments — typically ACH out to payroll providers or direct wires to people. Examples: OnPay, Gusto, ADP, direct wire to an employee, contractor 1099 payments.",
	},
	{
		Name:        "Travel & Transportation",
		Description: "Gas, vehicle maintenance, vehicle insurance, tolls, parking, plus business travel (flights, hotels, rideshare). Examples: Shell, BP, Wawa fuel, Jiffy Lube, Geico (vehicle line), E-ZPass, Delta, Uber, Airbnb (business trip).",
	},
	{
		Name:        "Software & Subscriptions",
		Description: "Software subscriptions and SaaS tools — typically recurring small charges. Examples: Toast POS, Sling, Mercury fees, GitHub, Anthropic, OpenAI, Slack, Google Workspace, QuickBooks, Xero.",
	},
	{
		Name:        "Office Supplies & Equipment",
		Description: "Non-food operating supplies — packaging, cleaning, office goods, kitchen equipment. Examples: Amazon non-food orders, Office Depot, Staples, Uline packaging, cleaning supplies, small kitchen equipment.",
	},
	{
		Name:        "Marketing & Advertising",
		Description: "Advertising spend and marketing materials. Examples: Meta Ads, Google Ads, print signage, business cards, sponsored posts, influencer payments.",
	},
	{
		Name:        "Legal & Professional Services",
		Description: "Lawyers, accountants, insurance, business consultants. Examples: bookkeeper, CPA, business attorney, liability insurance (non-vehicle), business license fees.",
	},
	{
		Name:        "Owner Personal",
		Description: "Anything that hit the business card by accident and is for personal use — to be reimbursed or excluded from business expenses. Examples: personal restaurant meals, personal Amazon orders, family travel.",
	},
	{
		Name:        "Revenue",
		Description: "Money coming IN from customers or sales channels. Examples: Toast batch deposits (TOAST; DEP …), DoorDash/Grubhub/Uber Eats payouts, customer Stripe payments, refunds received from a vendor.",
	},
	{
		Name:        "Transfer",
		Description: "Money moving between accounts you own — neither income nor expense. Examples: Mercury internal transfers (Operations → Savings), percentage-based auto-transfers, transfers between business and savings.",
	},
	{
		Name:        "Other / Needs Review",
		Description: "Fallback when the transaction genuinely doesn't fit any other category, or when there isn't enough context to decide. Prefer this over guessing — it surfaces to the operator for manual review.",
	},
}

// EnsureCategories guarantees every CanonicalCategory exists in Mercury and
// returns a name→MercuryCategory lookup (including any non-canonical
// categories the operator has manually created — those are preserved, not
// deleted). Idempotent: re-running creates nothing.
func EnsureCategories(client *external.MercuryClient) (map[string]external.MercuryCategory, error) {
	existing, err := client.ListCategories()
	if err != nil {
		return nil, fmt.Errorf("EnsureCategories: list existing: %w", err)
	}

	byName := make(map[string]external.MercuryCategory, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}

	for _, want := range CanonicalCategories {
		if _, ok := byName[want.Name]; ok {
			continue
		}
		created, err := client.CreateCategory(want.Name)
		if err != nil {
			return nil, fmt.Errorf("EnsureCategories: create %q: %w", want.Name, err)
		}
		byName[created.Name] = *created
	}
	return byName, nil
}

// SortedCategories returns the categories sorted by name. Used to produce a
// deterministic snapshot in the transactions cache file (so diffs are stable).
func SortedCategories(byName map[string]external.MercuryCategory) []external.MercuryCategory {
	out := make([]external.MercuryCategory, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
