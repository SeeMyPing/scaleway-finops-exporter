// Package billing fetches the invoice-aligned consumption and taxes of an
// organization and normalises them into an immutable Snapshot.
package billing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Currency is the only currency exported. Rows in any other currency are
	// skipped and counted, because metric names carry the "_euros" unit.
	Currency = "EUR"
	// ReasonUnexpectedCurrency is the error reason of skipped rows.
	ReasonUnexpectedCurrency = "unexpected_currency"
)

// Money is an amount in a given ISO 4217 currency.
type Money struct {
	Amount   float64
	Currency string
}

// Consumption is one consumption row as returned by the API.
type Consumption struct {
	OrganizationID string
	ProjectID      string
	ProjectName    string
	Category       string
	Product        string
	SKU            string
	Unit           string
	Value          Money
}

// ConsumptionReport is the consumption of one billing period.
type ConsumptionReport struct {
	Consumptions []Consumption
	// TotalDiscount is the sum of all discounts of the period, untaxed, in the
	// organization currency, as reported by the API.
	TotalDiscount float64
	// UpdatedAt is the last time the API updated the data. Zero if unknown.
	UpdatedAt time.Time
}

// Tax is one tax line of a billing period.
type Tax struct {
	Description string
	// Rate is the tax rate (0.2 for 20 %). Nil when the API does not report it.
	Rate  *float64
	Value Money
}

// TaxReport is the taxes of one billing period.
type TaxReport struct {
	Taxes     []Tax
	UpdatedAt time.Time
}

// API is the subset of the Scaleway billing API used by this source.
type API interface {
	// Consumptions returns all consumption rows of an organization for a
	// billing period in the YYYY-MM format.
	Consumptions(ctx context.Context, organizationID, period string) (*ConsumptionReport, error)
	// Taxes returns all taxes of an organization for a billing period.
	Taxes(ctx context.Context, organizationID, period string) (*TaxReport, error)
}

// Snapshot is the normalised billing data, ready to be exposed.
// It is immutable once returned by Fetch.
type Snapshot struct {
	OrganizationID string
	// Periods are sorted from the most recent to the oldest.
	Periods []Period
	// SKUs describe every SKU seen in any period, sorted by SKU.
	SKUs []SKU
}

// Period holds the aggregated data of one billing period.
type Period struct {
	// BillingPeriod is the period in the YYYY-MM format.
	BillingPeriod string
	// UpdatedAt is the most recent update time reported by the API. Zero if unknown.
	UpdatedAt   time.Time
	Consumption []ConsumptionSeries
	// DiscountEuros is the organization-wide discount, as reported by the API.
	DiscountEuros float64
	Taxes         []TaxSeries
}

// ConsumptionSeries is the consumption aggregated by project, category and product.
type ConsumptionSeries struct {
	OrganizationID string
	ProjectID      string
	ProjectName    string
	Category       string
	Product        string
	Euros          float64
}

// SKU maps a SKU to its category, product and billing unit.
type SKU struct {
	SKU      string
	Category string
	Product  string
	Unit     string
}

// TaxSeries is a tax line aggregated by description.
type TaxSeries struct {
	Description string
	Euros       float64
	Rate        *float64
}

// Options configures a Source.
type Options struct {
	// OrganizationID is the organization to query. Required.
	OrganizationID string
	// ProjectIDs restricts consumption to these projects. Empty means all.
	// Taxes and discounts are organization-wide and are never filtered.
	ProjectIDs []string
	// LookbackPeriods is the number of previous billing periods to fetch.
	LookbackPeriods int
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// SkippedRows counts rows dropped because of an unexpected currency. Required.
	SkippedRows prometheus.Counter
	// Logger is required.
	Logger *slog.Logger
}

// Source fetches billing snapshots.
type Source struct {
	api      API
	opts     Options
	projects map[string]bool
}

// New returns a billing Source.
func New(api API, opts Options) (*Source, error) {
	if api == nil || opts.OrganizationID == "" || opts.SkippedRows == nil || opts.Logger == nil {
		return nil, errors.New("billing: api, organization ID, skipped rows counter and logger are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	projects := make(map[string]bool, len(opts.ProjectIDs))
	for _, id := range opts.ProjectIDs {
		projects[id] = true
	}
	return &Source{api: api, opts: opts, projects: projects}, nil
}

// Fetch queries the current and previous billing periods. It fails as a whole
// if any call fails, so that a partial snapshot never replaces a complete one.
func (s *Source) Fetch(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{OrganizationID: s.opts.OrganizationID}
	skus := map[string]SKU{}

	for _, period := range Periods(s.opts.Now(), s.opts.LookbackPeriods) {
		consumption, err := s.api.Consumptions(ctx, s.opts.OrganizationID, period)
		if err != nil {
			return nil, fmt.Errorf("billing: listing consumptions for %s: %w", period, err)
		}
		taxes, err := s.api.Taxes(ctx, s.opts.OrganizationID, period)
		if err != nil {
			return nil, fmt.Errorf("billing: listing taxes for %s: %w", period, err)
		}
		snap.Periods = append(snap.Periods, s.aggregate(period, consumption, taxes, skus))
	}

	for _, sku := range skus {
		snap.SKUs = append(snap.SKUs, sku)
	}
	slices.SortFunc(snap.SKUs, func(a, b SKU) int { return cmp.Compare(a.SKU, b.SKU) })
	return snap, nil
}

// aggregate turns the raw rows of one period into series. It records the SKUs
// it sees in skus, keeping the first description found: periods are visited
// from the most recent, so current names win.
func (s *Source) aggregate(period string, c *ConsumptionReport, t *TaxReport, skus map[string]SKU) Period {
	p := Period{BillingPeriod: period, DiscountEuros: c.TotalDiscount, UpdatedAt: c.UpdatedAt}
	if t.UpdatedAt.After(p.UpdatedAt) {
		p.UpdatedAt = t.UpdatedAt
	}

	type key struct{ org, project, projectName, category, product string }
	sums := map[key]float64{}
	for i := range c.Consumptions {
		row := &c.Consumptions[i]
		if len(s.projects) > 0 && !s.projects[row.ProjectID] {
			continue
		}
		if !s.acceptCurrency(row.Value, period, "consumption") {
			continue
		}
		org := row.OrganizationID
		if org == "" {
			org = s.opts.OrganizationID
		}
		sums[key{org, row.ProjectID, row.ProjectName, row.Category, row.Product}] += row.Value.Amount
		if _, seen := skus[row.SKU]; !seen && row.SKU != "" {
			skus[row.SKU] = SKU{SKU: row.SKU, Category: row.Category, Product: row.Product, Unit: row.Unit}
		}
	}
	for k, v := range sums {
		p.Consumption = append(p.Consumption, ConsumptionSeries{
			OrganizationID: k.org, ProjectID: k.project, ProjectName: k.projectName,
			Category: k.category, Product: k.product, Euros: v,
		})
	}
	slices.SortFunc(p.Consumption, func(a, b ConsumptionSeries) int {
		return cmp.Or(
			cmp.Compare(a.OrganizationID, b.OrganizationID),
			cmp.Compare(a.ProjectID, b.ProjectID),
			cmp.Compare(a.ProjectName, b.ProjectName),
			cmp.Compare(a.Category, b.Category),
			cmp.Compare(a.Product, b.Product),
		)
	})

	taxes := map[string]*TaxSeries{}
	for _, tax := range t.Taxes {
		if !s.acceptCurrency(tax.Value, period, "tax") {
			continue
		}
		ts, ok := taxes[tax.Description]
		if !ok {
			ts = &TaxSeries{Description: tax.Description, Rate: tax.Rate}
			taxes[tax.Description] = ts
		}
		ts.Euros += tax.Value.Amount
	}
	for _, ts := range taxes {
		p.Taxes = append(p.Taxes, *ts)
	}
	slices.SortFunc(p.Taxes, func(a, b TaxSeries) int { return cmp.Compare(a.Description, b.Description) })

	return p
}

// acceptCurrency reports whether an amount can be added to euro series.
// A zero amount is always accepted: the API omits the value (and thus the
// currency) of free usage, and a zero changes no sum whatever its currency.
func (s *Source) acceptCurrency(m Money, period, kind string) bool {
	if m.Currency == Currency || m.Amount == 0 {
		return true
	}
	s.opts.SkippedRows.Inc()
	s.opts.Logger.Warn("skipping row with unexpected currency",
		"kind", kind, "billing_period", period, "currency", m.Currency, "expected", Currency)
	return false
}

// Periods returns the billing period of now (in UTC) followed by the lookback
// previous ones, in the YYYY-MM format.
func Periods(now time.Time, lookback int) []string {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periods := make([]string, 0, lookback+1)
	for i := range lookback + 1 {
		periods = append(periods, first.AddDate(0, -i, 0).Format("2006-01"))
	}
	return periods
}
