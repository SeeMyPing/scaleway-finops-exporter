package scaleway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
)

func ptr[T any](v T) *T { return &v }

func TestBillingConsumptionsPaginates(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/consumptions", pagedFixture(t, "billing/consumptions.json", "consumptions"))

	adapter := NewBilling(api.client())
	adapter.pageSize = 2 // 5 rows: 3 pages

	got, err := adapter.Consumptions(t.Context(), fakeOrgID, "2026-09")
	if err != nil {
		t.Fatalf("Consumptions() error = %v", err)
	}

	row := func(project, projectName, category, product, sku, unit string, value billing.Money) billing.Consumption {
		return billing.Consumption{
			OrganizationID: fakeOrgID, ProjectID: project, ProjectName: projectName,
			Category: category, Product: product, SKU: sku, Unit: unit, Value: value,
		}
	}
	const prod, staging = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002"
	want := &billing.ConsumptionReport{
		Consumptions: []billing.Consumption{
			row(prod, "production", "Compute", "Instances DEV1-S", "/compute/dev1_s/run_par1", "hour", billing.Money{Amount: 12.34, Currency: "EUR"}),
			row(prod, "production", "Compute", "Instances DEV1-S", "/compute/dev1_s/run_ams1", "hour", billing.Money{Amount: 3.5, Currency: "EUR"}),
			row(staging, "staging", "Storage", "Object Storage Standard", "/storage/object/standard_par", "GB-month", billing.Money{Amount: 0.87, Currency: "EUR"}),
			row(staging, "staging", "Other", "Credit", "/billing/credit", "unit", billing.Money{Amount: -1.25, Currency: "EUR"}),
			// Optional fields absent from the payload: no value, no unit.
			row(staging, "staging", "Network", "Public Gateway VPC-GW-S", "/network/vpc_gw_s/run_par1", "", billing.Money{}),
		},
		TotalDiscount: -2.5,
		UpdatedAt:     time.Date(2026, 9, 24, 6, 0, 0, 0, time.UTC),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Consumptions() mismatch (-want +got):\n%s", diff)
	}

	reqs := api.recorded()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3 pages", len(reqs))
	}
	for i, r := range reqs {
		q := r.URL.Query()
		checks := map[string]string{
			"organization_id": fakeOrgID,
			"billing_period":  "2026-09",
			"page_size":       "2",
			"page":            []string{"1", "2", "3"}[i],
		}
		for k, v := range checks {
			if q.Get(k) != v {
				t.Errorf("request %d: %s = %q, want %q", i, k, q.Get(k), v)
			}
		}
		if q.Has("project_id") {
			t.Errorf("request %d: project_id must not be set together with organization_id", i)
		}
	}
}

func TestBillingTaxes(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/taxes", pagedFixture(t, "billing/taxes.json", "taxes"))

	got, err := NewBilling(api.client()).Taxes(t.Context(), fakeOrgID, "2026-08")
	if err != nil {
		t.Fatalf("Taxes() error = %v", err)
	}

	want := &billing.TaxReport{
		Taxes: []billing.Tax{
			{Description: "VAT FR", Rate: ptr(0.2), Value: billing.Money{Amount: 3.09, Currency: "EUR"}},
			{Description: "Digital services tax", Value: billing.Money{Amount: 0.12, Currency: "EUR"}},
			{Description: "Pending tax", Value: billing.Money{Currency: "EUR"}},
		},
		UpdatedAt: time.Date(2026, 9, 24, 6, 5, 0, 0, time.UTC),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Taxes() mismatch (-want +got):\n%s", diff)
	}

	q := api.recorded()[0].URL.Query()
	if q.Get("organization_id") != fakeOrgID || q.Get("billing_period") != "2026-08" {
		t.Errorf("unexpected query %v", q)
	}
}

func TestBillingEmptyResponses(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/consumptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"consumptions":[],"total_count":0}`)
	})
	api.handle("GET /billing/v2beta1/taxes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"taxes":null,"total_count":0,"updated_at":null}`)
	})
	adapter := NewBilling(api.client())

	c, err := adapter.Consumptions(t.Context(), fakeOrgID, "2026-09")
	if err != nil {
		t.Fatalf("Consumptions() error = %v", err)
	}
	if len(c.Consumptions) != 0 || !c.UpdatedAt.IsZero() || c.TotalDiscount != 0 {
		t.Errorf("Consumptions() = %+v, want an empty report", c)
	}
	tx, err := adapter.Taxes(t.Context(), fakeOrgID, "2026-09")
	if err != nil {
		t.Fatalf("Taxes() error = %v", err)
	}
	if len(tx.Taxes) != 0 || !tx.UpdatedAt.IsZero() {
		t.Errorf("Taxes() = %+v, want an empty report", tx)
	}
}

func TestBillingErrors(t *testing.T) {
	t.Parallel()

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancelExpired)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		status     int
		body       string
		wantReason string
	}{
		{"denied authentication", nil, http.StatusUnauthorized, `{"type":"denied_authentication","method":"api_key","reason":"expired","message":"authentication is denied"}`, ReasonAuth},
		{"permissions denied", nil, http.StatusForbidden, `{"type":"permissions_denied","message":"insufficient permissions","details":[{"resource":"billing","action":"read"}]}`, ReasonAuth},
		{"plain forbidden", nil, http.StatusForbidden, `{"message":"forbidden"}`, ReasonAuth},
		{"rate limited", nil, http.StatusTooManyRequests, `{"message":"too many requests"}`, ReasonRateLimited},
		{"invalid arguments", nil, http.StatusBadRequest, `{"type":"invalid_arguments","message":"invalid argument(s)","details":[{"argument_name":"billing_period","reason":"constraint","help_message":"must match YYYY-MM"}]}`, ReasonClient},
		{"not found", nil, http.StatusNotFound, `{"type":"not_found","resource":"organization","resource_id":"x","message":"resource is not found"}`, ReasonClient},
		{"server error without JSON", nil, http.StatusBadGateway, ``, ReasonServer},
		{"invalid JSON", nil, http.StatusOK, `{"consumptions": [`, ReasonDecode},
		{"wrong JSON type", nil, http.StatusOK, `{"consumptions": "nope"}`, ReasonDecode},
		{"deadline exceeded", expired, http.StatusOK, `{}`, ReasonTimeout},
		{"canceled", canceled, http.StatusOK, `{}`, ReasonCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI(t)
			api.handle("GET /billing/v2beta1/consumptions", func(w http.ResponseWriter, _ *http.Request) {
				if tt.body == "" {
					w.WriteHeader(tt.status)
					return
				}
				writeJSON(w, tt.status, tt.body)
			})
			ctx := tt.ctx
			if ctx == nil {
				ctx = t.Context()
			}

			_, err := NewBilling(api.client()).Consumptions(ctx, fakeOrgID, "2026-09")
			if err == nil {
				t.Fatal("Consumptions() succeeded, want an error")
			}
			if got := Classify(err); got != tt.wantReason {
				t.Errorf("Classify(%v) = %q, want %q", err, got, tt.wantReason)
			}
		})
	}
}

func TestBillingNetworkError(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	client := api.client()
	api.server.Close() // connections are now refused

	_, err := NewBilling(client).Taxes(t.Context(), fakeOrgID, "2026-09")
	if got := Classify(err); got != ReasonNetwork {
		t.Errorf("Classify(%v) = %q, want %q", err, got, ReasonNetwork)
	}
}
