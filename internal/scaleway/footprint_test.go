package scaleway

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

func TestFootprintImpactData(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "footprint", "impact.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := newFakeAPI(t)
	api.handle("GET /environmental-footprint/v1alpha1/data/query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, string(raw))
	})

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	const p1, p2 = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002"
	got, err := NewFootprint(api.client()).ImpactData(t.Context(), footprint.Query{
		OrganizationID: fakeOrgID,
		ProjectIDs:     []string{p1},
		Start:          start,
		End:            end,
	})
	if err != nil {
		t.Fatalf("ImpactData() error = %v", err)
	}

	want := &footprint.Report{
		Rows: []footprint.Row{
			// Regional SKU: no zone.
			{ProjectID: p1, Region: "fr-par", ServiceCategory: "storage", ProductCategory: "object_storage", CarbonGrams: 123, WaterCubicMeters: 0.0011},
			{ProjectID: p1, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 10500, WaterCubicMeters: 0.035},
			{ProjectID: p1, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 1500, WaterCubicMeters: 0.005},
			{ProjectID: p2, Region: "nl-ams", Zone: "nl-ams-1", ServiceCategory: "databases", ProductCategory: "managed_relational_databases", CarbonGrams: 600, WaterCubicMeters: 0.002},
			// The SKU without total_sku_impact is skipped.
		},
		ProjectCarbonGrams: map[string]float64{p1: 12123, p2: 600},
		Start:              start,
		End:                end,
	}
	// Values must be exact: the float32 to float64 conversion must not leak artifacts.
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ImpactData() mismatch (-want +got):\n%s", diff)
	}

	q := api.recorded()[0].URL.Query()
	checks := map[string]string{
		"organization_id": fakeOrgID,
		"start_date":      "2026-09-01T00:00:00Z",
		"end_date":        "2026-09-24T00:00:00Z",
		"project_ids":     p1,
	}
	for k, v := range checks {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestFootprintEmptyAndErrors(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /environmental-footprint/v1alpha1/data/query", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_ids") == "forbidden" {
			writeJSON(w, http.StatusForbidden, `{"type":"permissions_denied","message":"insufficient permissions","details":[{"resource":"environmental_footprint","action":"read"}]}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"projects":[{"project_id":"x","regions":[null]}, null]}`)
	})
	adapter := NewFootprint(api.client())

	got, err := adapter.ImpactData(t.Context(), footprint.Query{OrganizationID: fakeOrgID})
	if err != nil {
		t.Fatalf("ImpactData() error = %v", err)
	}
	if len(got.Rows) != 0 || !got.Start.IsZero() || !got.End.IsZero() {
		t.Errorf("ImpactData() = %+v, want an empty report", got)
	}

	_, err = adapter.ImpactData(t.Context(), footprint.Query{OrganizationID: fakeOrgID, ProjectIDs: []string{"forbidden"}})
	if Classify(err) != ReasonAuth {
		t.Errorf("Classify(%v) = %q, want %q", err, Classify(err), ReasonAuth)
	}
}

func TestScaledFloat32(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    float32
		pow10 int
		want  float64
	}{
		{0.123, 3, 123},
		{0.123, 0, 0.123},
		{12.723, 3, 12723},
		{1e-7, 3, 1e-4},
		{0, 3, 0},
		{-0.5, 3, -500},
		{3.4e38, 0, 3.4e38},
	}
	for _, tt := range tests {
		if got := scaledFloat32(tt.in, tt.pow10); got != tt.want {
			t.Errorf("scaledFloat32(%v, %d) = %v, want %v", tt.in, tt.pow10, got, tt.want)
		}
	}
	// A naive conversion shows why this function exists.
	if naive := float64(float32(0.123)) * 1000; naive == 123 {
		t.Errorf("naive conversion unexpectedly exact: %v", naive)
	}

	nan := scaledFloat32(float32(math.NaN()), 3)
	inf := scaledFloat32(float32(math.Inf(1)), 3)
	if !math.IsNaN(nan) || !math.IsInf(inf, 1) {
		t.Errorf("non-finite values must pass through, got %v and %v", nan, inf)
	}
}
