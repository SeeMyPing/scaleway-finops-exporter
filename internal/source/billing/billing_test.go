package billing

import (
	"context"
	"errors"
	"log/slog"
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

// fakeAPI serves canned reports per billing period. It is a hand-written fake
// of the consumer-side API interface: no HTTP, no SDK.
type fakeAPI struct {
	consumptions map[string]*ConsumptionReport
	taxes        map[string]*TaxReport
	err          error
	calls        []string
}

func (f *fakeAPI) Consumptions(_ context.Context, organizationID, period string) (*ConsumptionReport, error) {
	f.calls = append(f.calls, "consumptions "+organizationID+" "+period)
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.consumptions[period]; ok {
		return r, nil
	}
	return &ConsumptionReport{}, nil
}

func (f *fakeAPI) Taxes(_ context.Context, organizationID, period string) (*TaxReport, error) {
	f.calls = append(f.calls, "taxes "+organizationID+" "+period)
	if r, ok := f.taxes[period]; ok {
		return r, nil
	}
	return &TaxReport{}, nil
}

func eur(v float64) Money { return Money{Amount: v, Currency: "EUR"} }

func rate(v float64) *float64 { return &v }

func consumption(project, projectName, category, product, sku string, value Money) Consumption {
	return Consumption{
		OrganizationID: org, ProjectID: project, ProjectName: projectName,
		Category: category, Product: product, SKU: sku, Unit: "hour", Value: value,
	}
}

func newSource(t *testing.T, api API, configure func(*Options)) (*Source, prometheus.Counter) {
	t.Helper()
	skipped := prometheus.NewCounter(prometheus.CounterOpts{Name: "skipped_rows_total", Help: "test"})
	opts := Options{
		OrganizationID:  org,
		LookbackPeriods: 1,
		Now:             func() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC) },
		SkippedRows:     skipped,
		Logger:          slog.New(slog.DiscardHandler),
	}
	if configure != nil {
		configure(&opts)
	}
	s, err := New(api, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return s, skipped
}

func TestFetchAggregates(t *testing.T) {
	t.Parallel()

	sept := time.Date(2026, 9, 24, 6, 0, 0, 0, time.UTC)
	api := &fakeAPI{
		consumptions: map[string]*ConsumptionReport{
			"2026-09": {
				Consumptions: []Consumption{
					// Two SKUs of the same product are summed into one series.
					consumption(prod, "production", "Compute", "Instances DEV1-S", "/compute/dev1_s/par1", eur(12.34)),
					consumption(prod, "production", "Compute", "Instances DEV1-S", "/compute/dev1_s/ams1", eur(3.5)),
					consumption(staging, "staging", "Storage", "Object Storage", "/storage/object/par", eur(0.87)),
					// Credits are negative and reduce the series.
					consumption(staging, "staging", "Storage", "Object Storage", "/storage/credit", eur(-0.5)),
				},
				TotalDiscount: -2.5,
				UpdatedAt:     sept,
			},
			"2026-08": {
				Consumptions: []Consumption{
					// The same SKU with an older product name: the current name wins in the SKU list.
					consumption(prod, "production", "Compute", "Instances DEV1-S (old)", "/compute/dev1_s/par1", eur(40)),
					consumption(prod, "production", "Compute", "Instances GP1-XS", "/compute/gp1_xs/par1", eur(0)),
				},
			},
		},
		taxes: map[string]*TaxReport{
			"2026-09": {
				Taxes: []Tax{
					{Description: "VAT FR", Rate: rate(0.2), Value: eur(3)},
					{Description: "VAT FR", Rate: rate(0.2), Value: eur(0.09)},
					{Description: "Other tax", Value: eur(0.12)},
				},
				UpdatedAt: sept.Add(5 * time.Minute),
			},
		},
	}
	s, skipped := newSource(t, api, nil)

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	want := &Snapshot{
		OrganizationID: org,
		Periods: []Period{
			{
				BillingPeriod: "2026-09",
				UpdatedAt:     sept.Add(5 * time.Minute), // the most recent of consumptions and taxes
				Consumption: []ConsumptionSeries{
					{org, prod, "production", "Compute", "Instances DEV1-S", 15.84},
					{org, staging, "staging", "Storage", "Object Storage", 0.37},
				},
				DiscountEuros: -2.5,
				Taxes: []TaxSeries{
					{Description: "Other tax", Euros: 0.12},
					{Description: "VAT FR", Euros: 3.09, Rate: rate(0.2)},
				},
			},
			{
				BillingPeriod: "2026-08",
				Consumption: []ConsumptionSeries{
					{org, prod, "production", "Compute", "Instances DEV1-S (old)", 40},
					{org, prod, "production", "Compute", "Instances GP1-XS", 0},
				},
			},
		},
		SKUs: []SKU{
			{"/compute/dev1_s/ams1", "Compute", "Instances DEV1-S", "hour"},
			{"/compute/dev1_s/par1", "Compute", "Instances DEV1-S", "hour"},
			{"/compute/gp1_xs/par1", "Compute", "Instances GP1-XS", "hour"},
			{"/storage/credit", "Storage", "Object Storage", "hour"},
			{"/storage/object/par", "Storage", "Object Storage", "hour"},
		},
	}
	approx := cmp.Comparer(func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 })
	if diff := cmp.Diff(want, got, approx); diff != "" {
		t.Errorf("Fetch() mismatch (-want +got):\n%s", diff)
	}
	if n := testutil.ToFloat64(skipped); n != 0 {
		t.Errorf("skipped rows = %v, want 0", n)
	}
	wantCalls := []string{
		"consumptions " + org + " 2026-09", "taxes " + org + " 2026-09",
		"consumptions " + org + " 2026-08", "taxes " + org + " 2026-08",
	}
	if diff := cmp.Diff(wantCalls, api.calls); diff != "" {
		t.Errorf("API calls mismatch (-want +got):\n%s", diff)
	}
}

func TestFetchFiltersProjects(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{
		consumptions: map[string]*ConsumptionReport{"2026-09": {
			Consumptions: []Consumption{
				consumption(prod, "production", "Compute", "Instances", "/a", eur(1)),
				consumption(staging, "staging", "Compute", "Instances", "/b", eur(2)),
			},
			TotalDiscount: -1,
		}},
		taxes: map[string]*TaxReport{"2026-09": {Taxes: []Tax{{Description: "VAT", Value: eur(0.6)}}}},
	}
	s, _ := newSource(t, api, func(o *Options) {
		o.ProjectIDs = []string{staging}
		o.LookbackPeriods = 0
	})

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := got.Periods[0]
	if len(p.Consumption) != 1 || p.Consumption[0].ProjectID != staging {
		t.Errorf("consumption = %+v, want only the staging project", p.Consumption)
	}
	if len(got.SKUs) != 1 || got.SKUs[0].SKU != "/b" {
		t.Errorf("SKUs = %+v, want only the SKUs of the kept rows", got.SKUs)
	}
	// Taxes and discounts are organization-wide and cannot be filtered.
	if len(p.Taxes) != 1 || p.DiscountEuros != -1 {
		t.Errorf("taxes = %+v, discount = %v, want them unfiltered", p.Taxes, p.DiscountEuros)
	}
}

func TestFetchSkipsUnexpectedCurrencies(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	api := &fakeAPI{
		consumptions: map[string]*ConsumptionReport{"2026-09": {Consumptions: []Consumption{
			consumption(prod, "production", "Compute", "Instances", "/a", eur(1)),
			consumption(prod, "production", "Compute", "Instances", "/b", Money{Amount: 5, Currency: "USD"}),
			consumption(prod, "production", "Compute", "Instances", "/c", Money{}), // free usage: no value, kept
		}}},
		taxes: map[string]*TaxReport{"2026-09": {Taxes: []Tax{
			{Description: "VAT", Value: Money{Amount: 1, Currency: "GBP"}},
		}}},
	}
	s, skipped := newSource(t, api, func(o *Options) {
		o.LookbackPeriods = 0
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	got, err := s.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Periods[0].Consumption; len(c) != 1 || c[0].Euros != 1 {
		t.Errorf("consumption = %+v, want the EUR row plus the zero one in a single series", c)
	}
	if len(got.SKUs) != 2 {
		t.Errorf("SKUs = %+v, want /a and /c (the USD row is dropped)", got.SKUs)
	}
	if len(got.Periods[0].Taxes) != 0 {
		t.Errorf("taxes = %+v, want none", got.Periods[0].Taxes)
	}
	if n := testutil.ToFloat64(skipped); n != 2 {
		t.Errorf("skipped rows = %v, want 2 (USD and GBP)", n)
	}
	if !strings.Contains(logs.String(), "currency=USD") {
		t.Errorf("logs do not mention the skipped currency: %s", logs.String())
	}
}

func TestFetchFailsAsAWhole(t *testing.T) {
	t.Parallel()

	errAPI := errors.New("api down")
	s, _ := newSource(t, &fakeAPI{err: errAPI}, nil)

	snap, err := s.Fetch(t.Context())
	if !errors.Is(err, errAPI) {
		t.Fatalf("Fetch() error = %v, want it to wrap the API error", err)
	}
	if snap != nil {
		t.Errorf("Fetch() returned a partial snapshot: %+v", snap)
	}
	if !strings.Contains(err.Error(), "2026-09") {
		t.Errorf("error %q does not name the failing period", err)
	}
}

func TestFetchTaxErrorFailsAsAWhole(t *testing.T) {
	t.Parallel()

	errAPI := errors.New("taxes down")
	s, _ := newSource(t, &failingTaxes{fakeAPI: &fakeAPI{}, err: errAPI}, nil)
	if _, err := s.Fetch(t.Context()); !errors.Is(err, errAPI) {
		t.Fatalf("Fetch() error = %v, want the taxes error", err)
	}
}

type failingTaxes struct {
	*fakeAPI
	err error
}

func (f *failingTaxes) Taxes(context.Context, string, string) (*TaxReport, error) { return nil, f.err }

func TestPeriods(t *testing.T) {
	t.Parallel()

	paris := time.FixedZone("CEST", 2*60*60)
	tests := []struct {
		name     string
		now      time.Time
		lookback int
		want     []string
	}{
		{"current only", time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), 0, []string{"2026-09"}},
		{"across a year", time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC), 2, []string{"2026-01", "2025-12", "2025-11"}},
		{"31st of a month", time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC), 1, []string{"2026-03", "2026-02"}},
		{"converted to UTC", time.Date(2026, 10, 1, 1, 0, 0, 0, paris), 0, []string{"2026-09"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.want, Periods(tt.now, tt.lookback)); diff != "" {
				t.Errorf("Periods() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewValidates(t *testing.T) {
	t.Parallel()

	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "c_total", Help: "c"})
	logger := slog.New(slog.DiscardHandler)
	tests := []struct {
		name string
		api  API
		opts Options
	}{
		{"no api", nil, Options{OrganizationID: org, SkippedRows: counter, Logger: logger}},
		{"no organization", &fakeAPI{}, Options{SkippedRows: counter, Logger: logger}},
		{"no counter", &fakeAPI{}, Options{OrganizationID: org, Logger: logger}},
		{"no logger", &fakeAPI{}, Options{OrganizationID: org, SkippedRows: counter}},
	}
	for _, tt := range tests {
		if _, err := New(tt.api, tt.opts); err == nil {
			t.Errorf("%s: New() succeeded, want an error", tt.name)
		}
	}

	s, err := New(&fakeAPI{}, Options{OrganizationID: org, SkippedRows: counter, Logger: logger})
	if err != nil || s.opts.Now == nil {
		t.Errorf("New() = %v, %v; want a source with a default clock", s, err)
	}
}
