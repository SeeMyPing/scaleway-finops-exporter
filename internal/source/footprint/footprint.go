// Package footprint fetches the estimated environmental impact (carbon and
// water) of an organization, for whole months and for the last complete day.
package footprint

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"
)

// mismatchTolerance is the relative gap between the sum of the SKU values and
// the project total above which a warning is logged.
const mismatchTolerance = 0.01

// Row is the impact of one SKU in one location.
type Row struct {
	ProjectID string
	Region    string
	// Zone is empty for regional products.
	Zone             string
	ServiceCategory  string
	ProductCategory  string
	CarbonGrams      float64
	WaterCubicMeters float64
}

// Report is the impact of an organization over a time range.
type Report struct {
	Rows []Row
	// ProjectCarbonGrams is the total carbon reported by the API per project,
	// used to check that the rows add up.
	ProjectCarbonGrams map[string]float64
	// Start (inclusive) and End (exclusive) are the range actually covered,
	// as reported by the API. Zero if not reported.
	Start, End time.Time
}

// Query selects impact data.
type Query struct {
	OrganizationID string
	ProjectIDs     []string
	// Start is inclusive, End exclusive.
	Start, End time.Time
}

// API is the subset of the Scaleway Environmental Footprint API used by this source.
type API interface {
	ImpactData(ctx context.Context, q Query) (*Report, error)
}

// Snapshot is the aggregated impact data. Immutable once returned.
type Snapshot struct {
	OrganizationID string
	// Months are sorted from the most recent to the oldest. The current month
	// covers the complete days elapsed so far.
	Months []Window
	// Day is the last complete day selected by DailyOffsetDays.
	Day Window
}

// Window is the impact over a time range.
type Window struct {
	// Label is the billing period (YYYY-MM) of a month, or the date (YYYY-MM-DD) of a day.
	Label string
	// Start and End bound the data covered. End is exclusive.
	Start, End time.Time
	Series     []Series
}

// Series is the impact aggregated by location and category.
type Series struct {
	ProjectID        string
	Region           string
	Zone             string
	ServiceCategory  string
	ProductCategory  string
	CarbonGrams      float64
	WaterCubicMeters float64
}

// Options configures a Source.
type Options struct {
	OrganizationID  string
	ProjectIDs      []string
	LookbackPeriods int
	// DailyOffsetDays selects the day of the daily window: 1 is yesterday.
	DailyOffsetDays int
	// Now returns the current time. Defaults to time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Source fetches footprint snapshots.
type Source struct {
	api  API
	opts Options
}

// New returns a footprint Source.
func New(api API, opts Options) (*Source, error) {
	if api == nil || opts.OrganizationID == "" || opts.Logger == nil {
		return nil, errors.New("footprint: api, organization ID and logger are required")
	}
	if opts.DailyOffsetDays < 1 {
		return nil, errors.New("footprint: daily offset must be at least one day")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Source{api: api, opts: opts}, nil
}

// Fetch queries every month window and the daily window.
func (s *Source) Fetch(ctx context.Context) (*Snapshot, error) {
	now := s.opts.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	snap := &Snapshot{OrganizationID: s.opts.OrganizationID}
	for _, w := range monthWindows(today, s.opts.LookbackPeriods) {
		if !w.End.After(w.Start) {
			// First day of the month: no complete day yet, nothing to query.
			snap.Months = append(snap.Months, w)
			continue
		}
		filled, err := s.fetchWindow(ctx, w)
		if err != nil {
			return nil, err
		}
		snap.Months = append(snap.Months, filled)
	}

	dayStart := today.AddDate(0, 0, -s.opts.DailyOffsetDays)
	day, err := s.fetchWindow(ctx, Window{
		Label: dayStart.Format(time.DateOnly),
		Start: dayStart,
		End:   dayStart.AddDate(0, 0, 1),
	})
	if err != nil {
		return nil, err
	}
	snap.Day = day
	return snap, nil
}

func (s *Source) fetchWindow(ctx context.Context, w Window) (Window, error) {
	report, err := s.api.ImpactData(ctx, Query{
		OrganizationID: s.opts.OrganizationID,
		ProjectIDs:     s.opts.ProjectIDs,
		Start:          w.Start,
		End:            w.End,
	})
	if err != nil {
		return Window{}, fmt.Errorf("footprint: querying impact data for %s: %w", w.Label, err)
	}
	if !report.Start.IsZero() {
		w.Start = report.Start
	}
	if !report.End.IsZero() {
		w.End = report.End
	}
	w.Series = aggregate(report.Rows)
	s.checkTotals(w.Label, report)
	return w, nil
}

func aggregate(rows []Row) []Series {
	type key struct{ project, region, zone, service, product string }
	sums := map[key]*Series{}
	for i := range rows {
		r := &rows[i]
		k := key{r.ProjectID, r.Region, r.Zone, r.ServiceCategory, r.ProductCategory}
		s, ok := sums[k]
		if !ok {
			s = &Series{
				ProjectID: r.ProjectID, Region: r.Region, Zone: r.Zone,
				ServiceCategory: r.ServiceCategory, ProductCategory: r.ProductCategory,
			}
			sums[k] = s
		}
		s.CarbonGrams += r.CarbonGrams
		s.WaterCubicMeters += r.WaterCubicMeters
	}

	series := make([]Series, 0, len(sums))
	for _, s := range sums {
		series = append(series, *s)
	}
	slices.SortFunc(series, func(a, b Series) int {
		return cmp.Or(
			cmp.Compare(a.ProjectID, b.ProjectID),
			cmp.Compare(a.Region, b.Region),
			cmp.Compare(a.Zone, b.Zone),
			cmp.Compare(a.ServiceCategory, b.ServiceCategory),
			cmp.Compare(a.ProductCategory, b.ProductCategory),
		)
	})
	return series
}

// checkTotals warns when the SKU rows of a project do not add up to the total
// the API reports for it, which would reveal missing or double-counted SKUs.
func (s *Source) checkTotals(label string, report *Report) {
	sums := map[string]float64{}
	for i := range report.Rows {
		sums[report.Rows[i].ProjectID] += report.Rows[i].CarbonGrams
	}
	for project, total := range report.ProjectCarbonGrams {
		sum := sums[project]
		if math.Abs(sum-total) > mismatchTolerance*math.Max(math.Abs(total), 1) {
			s.opts.Logger.Warn("footprint SKU values do not add up to the project total",
				"window", label, "project_id", project, "sku_sum_grams", sum, "project_total_grams", total)
		}
	}
}

// monthWindows returns the current month up to today (exclusive) followed by
// the lookback previous complete months.
func monthWindows(today time.Time, lookback int) []Window {
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := []Window{{Label: first.Format("2006-01"), Start: first, End: today}}
	for i := 1; i <= lookback; i++ {
		start := first.AddDate(0, -i, 0)
		out = append(out, Window{Label: start.Format("2006-01"), Start: start, End: start.AddDate(0, 1, 0)})
	}
	return out
}
