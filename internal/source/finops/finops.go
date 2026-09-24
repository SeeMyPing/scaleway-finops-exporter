// Package finops fetches the raw charges of the FinOps API, aggregates them by
// SKU (or by resource) and bounds the number of resulting series.
package finops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Currency is the only currency exported, see the billing source.
	Currency = "EUR"
	// ReasonUnexpectedCurrency is the error reason of skipped charges.
	ReasonUnexpectedCurrency = "unexpected_currency"
	// OtherSKU labels the series that aggregates everything above the series limit.
	OtherSKU = "other"
)

// Charge is one raw charge.
type Charge struct {
	OrganizationID string
	ProjectID      string
	ProjectName    string
	SKU            string
	ResourceID     string
	// ResourceName is empty when the product does not report it.
	ResourceName string
	Amount       float64
	Currency     string
	// UpdatedAt is the last update of the charge. Zero if unknown.
	UpdatedAt time.Time
}

// Query selects charges.
type Query struct {
	OrganizationID string
	// ProjectIDs restricts the charges to these projects. Empty means all.
	ProjectIDs []string
	// Start (inclusive) and End (exclusive) bound the charges. Charges that
	// overlap a bound are clamped to the requested range.
	Start, End time.Time
}

// API is the subset of the Scaleway FinOps API used by this source.
type API interface {
	// Charges returns every charge matching q, across all pages.
	Charges(ctx context.Context, q Query) ([]Charge, error)
}

// Snapshot is the aggregated charges, ready to be exposed. Immutable once returned.
type Snapshot struct {
	OrganizationID string
	// PerResource tells whether the series carry resource labels.
	PerResource bool
	// Periods are sorted from the most recent to the oldest.
	Periods []Period
}

// Period holds the charges of one billing period.
type Period struct {
	BillingPeriod string
	// UpdatedAt is the most recent charge update. Zero if unknown.
	UpdatedAt time.Time
	// Series are sorted by labels, with the OtherSKU series last if present.
	Series []Series
	// FoldedSeries is the number of series aggregated into the OtherSKU series.
	FoldedSeries int
}

// Series is the sum of the charges sharing the same labels.
type Series struct {
	OrganizationID string
	ProjectID      string
	ProjectName    string
	SKU            string
	// ResourceID and ResourceName are only set in per-resource mode.
	ResourceID   string
	ResourceName string
	Euros        float64
}

// Options configures a Source.
type Options struct {
	OrganizationID  string
	ProjectIDs      []string
	LookbackPeriods int
	// PerResource keeps resource_id and resource_name instead of aggregating by SKU.
	PerResource bool
	// MaxSeries bounds the number of series per billing period.
	MaxSeries int
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// SkippedCharges counts charges dropped because of an unexpected currency.
	SkippedCharges prometheus.Counter
	// LimitExceeded counts the periods for which the series limit was hit.
	LimitExceeded prometheus.Counter
	Logger        *slog.Logger
}

// Source fetches FinOps snapshots.
type Source struct {
	api  API
	opts Options
}

// New returns a FinOps Source.
func New(api API, opts Options) (*Source, error) {
	if api == nil || opts.OrganizationID == "" || opts.SkippedCharges == nil ||
		opts.LimitExceeded == nil || opts.Logger == nil {
		return nil, errors.New("finops: api, organization ID, counters and logger are required")
	}
	if opts.MaxSeries < 1 {
		return nil, errors.New("finops: max series must be at least 1")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Source{api: api, opts: opts}, nil
}

// Fetch queries the charges of the current and previous billing periods.
func (s *Source) Fetch(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{OrganizationID: s.opts.OrganizationID, PerResource: s.opts.PerResource}
	for _, b := range periodBounds(s.opts.Now(), s.opts.LookbackPeriods) {
		charges, err := s.api.Charges(ctx, Query{
			OrganizationID: s.opts.OrganizationID,
			ProjectIDs:     s.opts.ProjectIDs,
			Start:          b.start,
			End:            b.end,
		})
		if err != nil {
			return nil, fmt.Errorf("finops: listing charges for %s: %w", b.period, err)
		}
		snap.Periods = append(snap.Periods, s.aggregate(b.period, charges))
	}
	return snap, nil
}

func (s *Source) aggregate(period string, charges []Charge) Period {
	p := Period{BillingPeriod: period}

	type key struct{ org, project, projectName, sku, resourceID, resourceName string }
	sums := map[key]float64{}
	for i := range charges {
		c := &charges[i]
		if c.UpdatedAt.After(p.UpdatedAt) {
			p.UpdatedAt = c.UpdatedAt
		}
		if c.Currency != Currency {
			s.opts.SkippedCharges.Inc()
			s.opts.Logger.Warn("skipping charge with unexpected currency",
				"billing_period", period, "currency", c.Currency, "expected", Currency)
			continue
		}
		k := key{org: c.OrganizationID, project: c.ProjectID, projectName: c.ProjectName, sku: c.SKU}
		if k.org == "" {
			k.org = s.opts.OrganizationID
		}
		if s.opts.PerResource {
			k.resourceID, k.resourceName = c.ResourceID, c.ResourceName
		}
		sums[k] += c.Amount
	}

	series := make([]Series, 0, len(sums))
	for k, v := range sums {
		series = append(series, Series{
			OrganizationID: k.org, ProjectID: k.project, ProjectName: k.projectName,
			SKU: k.sku, ResourceID: k.resourceID, ResourceName: k.resourceName, Euros: v,
		})
	}

	if len(series) > s.opts.MaxSeries {
		series, p.FoldedSeries = s.fold(series)
		s.opts.LimitExceeded.Inc()
		s.opts.Logger.Warn("too many FinOps series, aggregating the smallest ones under sku=\"other\"",
			"billing_period", period, "series", len(sums), "max_series", s.opts.MaxSeries,
			"folded_series", p.FoldedSeries)
	} else {
		sortSeries(series)
	}
	p.Series = series
	return p
}

// fold keeps the MaxSeries-1 series with the largest absolute amounts and
// sums all the others into a single OtherSKU series, so that the total stays
// exact. Ties are broken on labels to keep the result deterministic.
func (s *Source) fold(series []Series) (kept []Series, folded int) {
	slices.SortFunc(series, func(a, b Series) int {
		return cmp.Or(cmp.Compare(math.Abs(b.Euros), math.Abs(a.Euros)), compareLabels(a, b))
	})
	keep := s.opts.MaxSeries - 1
	other := Series{OrganizationID: s.opts.OrganizationID, SKU: OtherSKU}
	for _, x := range series[keep:] {
		other.Euros += x.Euros
	}
	kept = slices.Clone(series[:keep])
	sortSeries(kept)
	return append(kept, other), len(series) - keep
}

func sortSeries(series []Series) { slices.SortFunc(series, compareLabels) }

func compareLabels(a, b Series) int {
	return cmp.Or(
		cmp.Compare(a.OrganizationID, b.OrganizationID),
		cmp.Compare(a.ProjectID, b.ProjectID),
		cmp.Compare(a.ProjectName, b.ProjectName),
		cmp.Compare(a.SKU, b.SKU),
		cmp.Compare(a.ResourceID, b.ResourceID),
		cmp.Compare(a.ResourceName, b.ResourceName),
	)
}

type bounds struct {
	period     string
	start, end time.Time
}

// periodBounds returns the UTC month of now and the lookback previous ones,
// most recent first, as [start, end) ranges.
func periodBounds(now time.Time, lookback int) []bounds {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := make([]bounds, 0, lookback+1)
	for i := range lookback + 1 {
		start := first.AddDate(0, -i, 0)
		out = append(out, bounds{period: start.Format("2006-01"), start: start, end: start.AddDate(0, 1, 0)})
	}
	return out
}
