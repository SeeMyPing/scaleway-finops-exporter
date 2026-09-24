package collector

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
)

func billingSnapshot() *billing.Snapshot {
	const (
		org     = "11111111-0000-4000-8000-000000000000"
		prod    = "aaaaaaaa-0000-4000-8000-000000000001"
		staging = "aaaaaaaa-0000-4000-8000-000000000002"
	)
	vat := 0.2
	return &billing.Snapshot{
		OrganizationID: org,
		Periods: []billing.Period{
			{
				BillingPeriod: "2026-09",
				UpdatedAt:     time.Date(2026, 9, 24, 6, 5, 0, 0, time.UTC),
				Consumption: []billing.ConsumptionSeries{
					{OrganizationID: org, ProjectID: prod, ProjectName: "production", Category: "Compute", Product: "Instances DEV1-S", Euros: 15.84},
					{OrganizationID: org, ProjectID: staging, ProjectName: "staging", Category: "Storage", Product: "Object Storage", Euros: 0.37},
					{OrganizationID: org, ProjectID: staging, ProjectName: "staging", Category: "Other", Product: "Credit", Euros: -1.25},
				},
				DiscountEuros: -2.5,
				Taxes: []billing.TaxSeries{
					{Description: "Other tax", Euros: 0.12},
					{Description: "VAT FR", Euros: 3.09, Rate: &vat},
				},
			},
			{
				// A period without update time, discount or taxes.
				BillingPeriod: "2026-08",
				Consumption: []billing.ConsumptionSeries{
					{OrganizationID: org, ProjectID: prod, ProjectName: "production", Category: "Compute", Product: "Instances DEV1-S", Euros: 40},
				},
			},
		},
		SKUs: []billing.SKU{
			{SKU: "/billing/credit", Category: "Other", Product: "Credit", Unit: "unit"},
			{SKU: "/compute/dev1_s/par1", Category: "Compute", Product: "Instances DEV1-S", Unit: "hour"},
			{SKU: "/storage/object/par", Category: "Storage", Product: "Object Storage", Unit: "GB-month"},
		},
	}
}

func TestBillingCollector(t *testing.T) {
	t.Parallel()
	checkGolden(t, NewBilling(static[billing.Snapshot]{billingSnapshot()}), "billing")
}

func TestBillingCollectorWithoutSnapshot(t *testing.T) {
	t.Parallel()

	c := NewBilling(static[billing.Snapshot]{nil})
	if n := testutil.CollectAndCount(c); n != 0 {
		t.Errorf("collected %d metrics before the first refresh, want 0", n)
	}
}
