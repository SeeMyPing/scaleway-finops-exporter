package finops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	org     = "11111111-0000-4000-8000-000000000000"
	prod    = "aaaaaaaa-0000-4000-8000-000000000001"
	staging = "aaaaaaaa-0000-4000-8000-000000000002"
)

type fakeAPI struct {
	charges map[string][]Charge // by period start, YYYY-MM
	err     error
	queries []Query
}

func (f *fakeAPI) Charges(_ context.Context, q Query) ([]Charge, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.charges[q.Start.Format("2006-01")], nil
}

func charge(project, sku, resource string, eur float64) Charge {
	return Charge{
		OrganizationID: org, ProjectID: project, ProjectName: map[string]string{prod: "production", staging: "staging"}[project],
		SKU: sku, ResourceID: resource, ResourceName: "name-" + resource, Amount: eur, Currency: "EUR",
	}
}

type counters struct{ skipped, exceeded prometheus.Counter }

func newSource(t *testing.T, api API, configure func(*Options)) (*Source, counters) {
	t.Helper()
	c := counters{
		skipped:  prometheus.NewCounter(prometheus.CounterOpts{Name: "skipped_total", Help: "test"}),
		exceeded: prometheus.NewCounter(prometheus.CounterOpts{Name: "exceeded_total", Help: "test"}),
	}
	opts := Options{
		OrganizationID: org,
		MaxSeries:      100,
		Now:            func() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC) },
		SkippedCharges: c.skipped,
		LimitExceeded:  c.exceeded,
		Logger:         slog.New(slog.DiscardHandler),
	}
	if configure != nil {
		configure(&opts)
	}
	s, err := New(api, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s, c
}

var approx = cmp.Comparer(func(a, b float64) bool { return math.Abs(a-b) < 1e-9 })

func TestFetchAggregatesBySKU(t *testing.T) {
	t.Parallel()

	updated := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	later := charge(prod, "/compute/a", "r2", 1.05)
	later.UpdatedAt = updated
	api := &fakeAPI{charges: map[string][]Charge{
		"2026-09": {
			charge(prod, "/compute/a", "r1", 0.35),
			later,
			charge(staging, "/storage/b", "r3", 2),
			charge(staging, "/storage/b", "r3", -0.5), // a refund on the same resource
		},
		"2026-08": {charge(prod, "/compute/a", "r1", 10)},
	}}
	s, c := newSource(t, api, func(o *Options) {
		o.LookbackPeriods = 1
		o.ProjectIDs = []string{prod, staging}
	})

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	want := &Snapshot{
		OrganizationID: org,
		Periods: []Period{
			{
				BillingPeriod: "2026-09",
				UpdatedAt:     updated,
				Series: []Series{
					{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/compute/a", Euros: 1.4},
					{OrganizationID: org, ProjectID: staging, ProjectName: "staging", SKU: "/storage/b", Euros: 1.5},
				},
			},
			{
				BillingPeriod: "2026-08",
				Series:        []Series{{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/compute/a", Euros: 10}},
			},
		},
	}
	if diff := cmp.Diff(want, got, approx); diff != "" {
		t.Errorf("Fetch() mismatch (-want +got):\n%s", diff)
	}
	if testutil.ToFloat64(c.exceeded) != 0 || testutil.ToFloat64(c.skipped) != 0 {
		t.Error("no counter should move")
	}

	// Each period is queried with its own clamped [start, end) range and the project filter.
	sept := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wantQueries := []Query{
		{OrganizationID: org, ProjectIDs: []string{prod, staging}, Start: sept, End: sept.AddDate(0, 1, 0)},
		{OrganizationID: org, ProjectIDs: []string{prod, staging}, Start: sept.AddDate(0, -1, 0), End: sept},
	}
	if diff := cmp.Diff(wantQueries, api.queries); diff != "" {
		t.Errorf("queries mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchPerResource(t *testing.T) {
	t.Parallel()

	noOrg := charge(staging, "/storage/b", "r3", 2)
	noOrg.OrganizationID = "" // falls back to the configured organization
	noOrg.ResourceName = ""   // not reported by every product
	api := &fakeAPI{charges: map[string][]Charge{"2026-09": {
		charge(prod, "/compute/a", "r1", 0.35),
		charge(prod, "/compute/a", "r1", 0.35),
		charge(prod, "/compute/a", "r2", 1),
		noOrg,
	}}}
	s, _ := newSource(t, api, func(o *Options) { o.PerResource = true })

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []Series{
		{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/compute/a", ResourceID: "r1", ResourceName: "name-r1", Euros: 0.7},
		{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/compute/a", ResourceID: "r2", ResourceName: "name-r2", Euros: 1},
		{OrganizationID: org, ProjectID: staging, ProjectName: "staging", SKU: "/storage/b", ResourceID: "r3", Euros: 2},
	}
	if diff := cmp.Diff(want, got.Periods[0].Series, approx); diff != "" {
		t.Errorf("series mismatch (-want +got):\n%s", diff)
	}
	if !got.PerResource {
		t.Error("snapshot should record the per-resource mode")
	}
}

func TestFetchFoldsSeriesAboveTheLimit(t *testing.T) {
	t.Parallel()

	// Five SKUs; the two smallest by absolute value must be folded.
	api := &fakeAPI{charges: map[string][]Charge{"2026-09": {
		charge(prod, "/a", "", 10),
		charge(prod, "/b", "", -8), // a large credit is kept: order is by absolute value
		charge(prod, "/c", "", 5),
		charge(prod, "/d", "", 1),
		charge(prod, "/e", "", 0.5),
	}}}
	var logs strings.Builder
	s, c := newSource(t, api, func(o *Options) {
		o.MaxSeries = 4
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := got.Periods[0]
	want := []Series{
		{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/a", Euros: 10},
		{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/b", Euros: -8},
		{OrganizationID: org, ProjectID: prod, ProjectName: "production", SKU: "/c", Euros: 5},
		{OrganizationID: org, SKU: OtherSKU, Euros: 1.5},
	}
	if diff := cmp.Diff(want, p.Series, approx); diff != "" {
		t.Errorf("series mismatch (-want +got):\n%s", diff)
	}
	if len(p.Series) != 4 || p.FoldedSeries != 2 {
		t.Errorf("got %d series with %d folded, want 4 series with 2 folded", len(p.Series), p.FoldedSeries)
	}
	if n := testutil.ToFloat64(c.exceeded); n != 1 {
		t.Errorf("limit exceeded counter = %v, want 1", n)
	}
	if !strings.Contains(logs.String(), "too many FinOps series") {
		t.Errorf("no warning logged: %s", logs.String())
	}

	// The total is preserved exactly by the folding.
	var total float64
	for _, s := range p.Series {
		total += s.Euros
	}
	if math.Abs(total-8.5) > 1e-9 {
		t.Errorf("total after folding = %v, want 8.5", total)
	}
}

func TestFoldingEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		series     int
		maxSeries  int
		wantSeries int
		wantFolded int
	}{
		{"exactly at the limit", 3, 3, 3, 0},
		{"one above the limit", 4, 3, 3, 2},
		{"limit of one", 3, 1, 1, 3},
		{"no charges", 0, 1, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			charges := make([]Charge, tt.series)
			for i := range charges {
				charges[i] = charge(prod, fmt.Sprintf("/sku/%d", i), "", float64(i+1))
			}
			s, _ := newSource(t, &fakeAPI{charges: map[string][]Charge{"2026-09": charges}}, func(o *Options) {
				o.MaxSeries = tt.maxSeries
			})
			got, err := s.Fetch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p := got.Periods[0]
			if len(p.Series) != tt.wantSeries || p.FoldedSeries != tt.wantFolded {
				t.Errorf("got %d series, %d folded; want %d series, %d folded",
					len(p.Series), p.FoldedSeries, tt.wantSeries, tt.wantFolded)
			}
		})
	}
}

func TestFoldingIsDeterministic(t *testing.T) {
	t.Parallel()

	// Equal amounts: ties must be broken on labels, not on map iteration order.
	var charges []Charge
	for i := range 20 {
		charges = append(charges, charge(prod, fmt.Sprintf("/sku/%02d", i), "", 1))
	}
	s, _ := newSource(t, &fakeAPI{charges: map[string][]Charge{"2026-09": charges}}, func(o *Options) { o.MaxSeries = 5 })

	first, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		again, err := s.Fetch(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(first, again); diff != "" {
			t.Fatalf("folding is not deterministic (-first +again):\n%s", diff)
		}
	}
	if got := first.Periods[0].Series[0].SKU; got != "/sku/00" {
		t.Errorf("first kept SKU = %q, want /sku/00", got)
	}
}

func TestFetchSkipsUnexpectedCurrencies(t *testing.T) {
	t.Parallel()

	usd := charge(prod, "/a", "", 3)
	usd.Currency = "USD"
	gbp := charge(prod, "/a", "", -2)
	gbp.Currency = "GBP"
	free := charge(prod, "/a", "", 0)
	free.Currency = "" // free usage has no price at all: accepted
	s, c := newSource(t, &fakeAPI{charges: map[string][]Charge{"2026-09": {charge(prod, "/a", "", 1), usd, gbp, free}}}, nil)

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if series := got.Periods[0].Series; len(series) != 1 || series[0].Euros != 1 {
		t.Errorf("series = %+v, want only the EUR charge", series)
	}
	if n := testutil.ToFloat64(c.skipped); n != 2 {
		t.Errorf("skipped = %v, want 2", n)
	}
}

func TestFetchFailsAsAWhole(t *testing.T) {
	t.Parallel()

	errAPI := errors.New("finops down")
	s, _ := newSource(t, &fakeAPI{err: errAPI}, nil)
	snap, err := s.Fetch(t.Context())
	if !errors.Is(err, errAPI) || snap != nil {
		t.Fatalf("Fetch() = %v, %v; want nil and the API error", snap, err)
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()

	ctr := prometheus.NewCounter(prometheus.CounterOpts{Name: "x_total", Help: "x"})
	logger := slog.New(slog.DiscardHandler)
	valid := Options{OrganizationID: org, MaxSeries: 1, SkippedCharges: ctr, LimitExceeded: ctr, Logger: logger}

	tests := map[string]func(*Options){
		"no organization":    func(o *Options) { o.OrganizationID = "" },
		"no skipped counter": func(o *Options) { o.SkippedCharges = nil },
		"no limit counter":   func(o *Options) { o.LimitExceeded = nil },
		"no logger":          func(o *Options) { o.Logger = nil },
		"zero max series":    func(o *Options) { o.MaxSeries = 0 },
	}
	for name, mutate := range tests {
		opts := valid
		mutate(&opts)
		if _, err := New(&fakeAPI{}, opts); err == nil {
			t.Errorf("%s: New() succeeded, want an error", name)
		}
	}
	if _, err := New(nil, valid); err == nil {
		t.Error("New() without API succeeded")
	}
	if s, err := New(&fakeAPI{}, valid); err != nil || s.opts.Now == nil {
		t.Errorf("New() = %v, %v; want a source with a default clock", s, err)
	}
}
