package footprint

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	org  = "11111111-0000-4000-8000-000000000000"
	prod = "aaaaaaaa-0000-4000-8000-000000000001"
)

type fakeAPI struct {
	reports map[string]*Report // by query start date, YYYY-MM-DD
	err     error
	queries []Query
}

func (f *fakeAPI) ImpactData(_ context.Context, q Query) (*Report, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.reports[q.Start.Format(time.DateOnly)]; ok {
		return r, nil
	}
	return &Report{}, nil
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func newSource(t *testing.T, api API, now time.Time, configure func(*Options)) *Source {
	t.Helper()
	opts := Options{
		OrganizationID:  org,
		ProjectIDs:      []string{prod},
		LookbackPeriods: 1,
		DailyOffsetDays: 1,
		Now:             func() time.Time { return now },
		Logger:          slog.New(slog.DiscardHandler),
	}
	if configure != nil {
		configure(&opts)
	}
	s, err := New(api, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s
}

func TestFetchQueriesMonthsAndDay(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{}
	s := newSource(t, api, time.Date(2026, 9, 24, 15, 30, 0, 0, time.UTC), nil)
	if _, err := s.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := []Query{
		// Current month: complete days only, today excluded.
		{OrganizationID: org, ProjectIDs: []string{prod}, Start: date(2026, 9, 1), End: date(2026, 9, 24)},
		// Previous month: complete.
		{OrganizationID: org, ProjectIDs: []string{prod}, Start: date(2026, 8, 1), End: date(2026, 9, 1)},
		// Yesterday.
		{OrganizationID: org, ProjectIDs: []string{prod}, Start: date(2026, 9, 23), End: date(2026, 9, 24)},
	}
	if diff := cmp.Diff(want, api.queries); diff != "" {
		t.Errorf("queries mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchOnTheFirstDayOfTheMonth(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{}
	s := newSource(t, api, time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), func(o *Options) { o.DailyOffsetDays = 2 })
	snap, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// No complete day in October yet: the current month is not queried.
	if len(api.queries) != 2 {
		t.Fatalf("got %d queries, want 2 (September and the daily window): %+v", len(api.queries), api.queries)
	}
	if got := snap.Months[0]; got.Label != "2026-10" || len(got.Series) != 0 {
		t.Errorf("current month = %+v, want an empty 2026-10 window", got)
	}
	if got := snap.Day.Label; got != "2026-09-29" {
		t.Errorf("daily window = %s, want 2026-09-29 with a 2-day offset", got)
	}
}

func TestFetchAggregatesAndUsesReportedRange(t *testing.T) {
	t.Parallel()

	rows := []Row{
		{ProjectID: prod, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 10500, WaterCubicMeters: 0.035},
		{ProjectID: prod, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 1500, WaterCubicMeters: 0.005},
		{ProjectID: prod, Region: "fr-par", ServiceCategory: "storage", ProductCategory: "object_storage", CarbonGrams: 123, WaterCubicMeters: 0.0011},
	}
	api := &fakeAPI{reports: map[string]*Report{
		"2026-09-01": {
			Rows:               rows,
			ProjectCarbonGrams: map[string]float64{prod: 12123},
			Start:              date(2026, 9, 1),
			End:                date(2026, 9, 22), // the API has less data than asked
		},
		"2026-09-23": {Rows: rows[:1], ProjectCarbonGrams: map[string]float64{prod: 10500}},
	}}
	s := newSource(t, api, time.Date(2026, 9, 24, 0, 0, 1, 0, time.UTC), func(o *Options) { o.LookbackPeriods = 0 })

	snap, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := &Snapshot{
		OrganizationID: org,
		Months: []Window{{
			Label: "2026-09",
			Start: date(2026, 9, 1),
			End:   date(2026, 9, 22),
			Series: []Series{
				{ProjectID: prod, Region: "fr-par", ServiceCategory: "storage", ProductCategory: "object_storage", CarbonGrams: 123, WaterCubicMeters: 0.0011},
				{ProjectID: prod, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 12000, WaterCubicMeters: 0.04},
			},
		}},
		Day: Window{
			Label: "2026-09-23",
			Start: date(2026, 9, 23),
			End:   date(2026, 9, 24),
			Series: []Series{
				{ProjectID: prod, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 10500, WaterCubicMeters: 0.035},
			},
		},
	}
	approx := cmp.Comparer(func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 })
	if diff := cmp.Diff(want, snap, approx); diff != "" {
		t.Errorf("Fetch() mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchWarnsWhenTotalsDoNotAddUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rows     []Row
		total    float64
		wantWarn bool
	}{
		{"exact", []Row{{ProjectID: prod, CarbonGrams: 100}}, 100, false},
		{"within tolerance", []Row{{ProjectID: prod, CarbonGrams: 100.5}}, 100, false},
		{"double counted", []Row{{ProjectID: prod, CarbonGrams: 100}, {ProjectID: prod, CarbonGrams: 100}}, 100, true},
		{"missing rows", nil, 100, true},
		{"tiny values", []Row{{ProjectID: prod, CarbonGrams: 0.001}}, 0.002, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs strings.Builder
			api := &fakeAPI{reports: map[string]*Report{
				"2026-09-01": {Rows: tt.rows, ProjectCarbonGrams: map[string]float64{prod: tt.total}},
			}}
			s := newSource(t, api, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), func(o *Options) {
				o.LookbackPeriods = 0
				o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			if _, err := s.Fetch(t.Context()); err != nil {
				t.Fatal(err)
			}
			if warned := strings.Contains(logs.String(), "do not add up"); warned != tt.wantWarn {
				t.Errorf("warned = %v, want %v; logs: %s", warned, tt.wantWarn, logs.String())
			}
		})
	}
}

func TestFetchFailsAsAWhole(t *testing.T) {
	t.Parallel()

	errAPI := errors.New("footprint down")
	s := newSource(t, &fakeAPI{err: errAPI}, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), nil)
	snap, err := s.Fetch(t.Context())
	if !errors.Is(err, errAPI) || snap != nil {
		t.Fatalf("Fetch() = %v, %v; want nil and the API error", snap, err)
	}
	if !strings.Contains(err.Error(), "2026-09") {
		t.Errorf("error %q does not name the window", err)
	}
}

// failingDay fails only the daily window query.
type failingDay struct{ fakeAPI }

func (f *failingDay) ImpactData(ctx context.Context, q Query) (*Report, error) {
	if q.End.Sub(q.Start) == 24*time.Hour {
		return nil, errors.New("day unavailable")
	}
	return f.fakeAPI.ImpactData(ctx, q)
}

func TestFetchFailsWhenTheDayFails(t *testing.T) {
	t.Parallel()

	s := newSource(t, &failingDay{}, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), nil)
	if _, err := s.Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), "2026-09-23") {
		t.Fatalf("Fetch() error = %v, want the daily window error", err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	tests := map[string]struct {
		api  API
		opts Options
	}{
		"no api":          {nil, Options{OrganizationID: org, DailyOffsetDays: 1, Logger: logger}},
		"no organization": {&fakeAPI{}, Options{DailyOffsetDays: 1, Logger: logger}},
		"no logger":       {&fakeAPI{}, Options{OrganizationID: org, DailyOffsetDays: 1}},
		"zero offset":     {&fakeAPI{}, Options{OrganizationID: org, Logger: logger}},
	}
	for name, tt := range tests {
		if _, err := New(tt.api, tt.opts); err == nil {
			t.Errorf("%s: New() succeeded, want an error", name)
		}
	}
	s, err := New(&fakeAPI{}, Options{OrganizationID: org, DailyOffsetDays: 1, Logger: logger})
	if err != nil || s.opts.Now == nil {
		t.Errorf("New() = %v, %v; want a source with a default clock", s, err)
	}
}
