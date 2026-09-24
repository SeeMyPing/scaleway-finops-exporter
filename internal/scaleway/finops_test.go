package scaleway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
)

// tokenPagedCharges serves the charges fixture pageSize items at a time,
// chaining pages with next_page_token like the FinOps API does.
func tokenPagedCharges(t *testing.T, pageSize int) http.HandlerFunc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "finops", "charges.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Charges []json.RawMessage `json:"charges"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if tok := r.URL.Query().Get("page_token"); tok != "" {
			start, _ = strconv.Atoi(tok[len("tok-"):])
		}
		end := min(start+pageSize, len(doc.Charges))
		page := map[string]any{"charges": doc.Charges[start:end], "next_page_token": nil}
		if end < len(doc.Charges) {
			page["next_page_token"] = "tok-" + strconv.Itoa(end)
		}
		body, _ := json.Marshal(page)
		writeJSON(w, http.StatusOK, string(body))
	}
}

func TestFinOpsChargesFollowsPageTokens(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/charges", tokenPagedCharges(t, 2))

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	got, err := NewFinOps(api.client()).Charges(t.Context(), finops.Query{
		OrganizationID: fakeOrgID,
		ProjectIDs:     []string{"aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002"},
		Start:          start,
		End:            start.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatalf("Charges() error = %v", err)
	}

	want := []finops.Charge{
		{
			OrganizationID: fakeOrgID, ProjectID: "aaaaaaaa-0000-4000-8000-000000000001", ProjectName: "production",
			SKU: "/compute/dev1_s/run_par1", ResourceID: "bbbbbbbb-0000-4000-8000-000000000001", ResourceName: "web-1",
			Amount: 0.35, Currency: "EUR", UpdatedAt: time.Date(2026, 9, 2, 3, 0, 0, 0, time.UTC),
		},
		{
			// resource_name is null: the product does not report it.
			OrganizationID: fakeOrgID, ProjectID: "aaaaaaaa-0000-4000-8000-000000000001", ProjectName: "production",
			SKU: "/compute/dev1_s/run_par1", ResourceID: "bbbbbbbb-0000-4000-8000-000000000002",
			Amount: 1.05, Currency: "EUR", UpdatedAt: time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC),
		},
		{
			// No resource_name nor updated_at at all.
			OrganizationID: fakeOrgID, ProjectID: "aaaaaaaa-0000-4000-8000-000000000002", ProjectName: "staging",
			SKU: "/storage/block/sbs_5k_par1", ResourceID: "cccccccc-0000-4000-8000-000000000001",
			Amount: 2, Currency: "EUR",
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Charges() mismatch (-want +got):\n%s", diff)
	}

	reqs := api.recorded()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 pages", len(reqs))
	}
	q := reqs[0].URL.Query()
	checks := map[string]string{
		"organization_id":     fakeOrgID,
		"start_date_after":    "2026-09-01T00:00:00Z",
		"end_date_before":     "2026-10-01T00:00:00Z",
		"clamp_to_time_range": "true",
		"order_by":            "start_date_asc",
	}
	for k, v := range checks {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if got := q["project_ids"]; len(got) != 2 {
		t.Errorf("project_ids = %v, want both projects", got)
	}
	if q.Has("page_token") || q.Has("page_size") {
		t.Errorf("first page must not send page_token nor page_size: %v", q)
	}
	if tok := reqs[1].URL.Query().Get("page_token"); tok != "tok-2" {
		t.Errorf("second page token = %q, want tok-2", tok)
	}
}

func TestFinOpsChargesPageSize(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/charges", tokenPagedCharges(t, 10))
	adapter := NewFinOps(api.client())
	adapter.pageSize = 500

	if _, err := adapter.Charges(t.Context(), finops.Query{OrganizationID: fakeOrgID}); err != nil {
		t.Fatal(err)
	}
	if got := api.recorded()[0].URL.Query().Get("page_size"); got != "500" {
		t.Errorf("page_size = %q, want 500", got)
	}
}

func TestFinOpsChargesDetectsTokenLoops(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/charges", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"charges":[],"next_page_token":"same"}`)
	})
	if _, err := NewFinOps(api.client()).Charges(t.Context(), finops.Query{OrganizationID: fakeOrgID}); err == nil {
		t.Fatal("Charges() with a repeating page token should fail")
	}
}

func TestFinOpsChargesErrors(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	calls := 0
	api.handle("GET /billing/v2beta1/charges", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(w, http.StatusOK, `{"charges":[],"next_page_token":"p2"}`)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, `{"message":"unavailable"}`)
	})

	_, err := NewFinOps(api.client()).Charges(t.Context(), finops.Query{OrganizationID: fakeOrgID})
	if got := Classify(err); got != ReasonServer {
		t.Errorf("Classify(%v) = %q, want %q (a failing later page fails the whole list)", err, got, ReasonServer)
	}
}
