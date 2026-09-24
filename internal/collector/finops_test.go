package collector

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
)

const (
	testOrg     = "11111111-0000-4000-8000-000000000000"
	testProd    = "aaaaaaaa-0000-4000-8000-000000000001"
	testStaging = "aaaaaaaa-0000-4000-8000-000000000002"
)

func TestFinOpsCollectorPerSKU(t *testing.T) {
	t.Parallel()

	snap := &finops.Snapshot{
		OrganizationID: testOrg,
		Periods: []finops.Period{
			{
				BillingPeriod: "2026-09",
				UpdatedAt:     time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC),
				Series: []finops.Series{
					{OrganizationID: testOrg, ProjectID: testProd, ProjectName: "production", SKU: "/compute/dev1_s/run_par1", Euros: 1.4},
					{OrganizationID: testOrg, ProjectID: testStaging, ProjectName: "staging", SKU: "/storage/block/sbs_5k_par1", Euros: 1.5},
					{OrganizationID: testOrg, SKU: finops.OtherSKU, Euros: 0.25},
				},
				FoldedSeries: 12,
			},
			{
				BillingPeriod: "2026-08",
				Series: []finops.Series{
					{OrganizationID: testOrg, ProjectID: testProd, ProjectName: "production", SKU: "/compute/dev1_s/run_par1", Euros: 10},
				},
			},
		},
	}
	checkGolden(t, NewFinOps(static[finops.Snapshot]{snap}, false), "finops_per_sku")
}

func TestFinOpsCollectorPerResource(t *testing.T) {
	t.Parallel()

	snap := &finops.Snapshot{
		OrganizationID: testOrg,
		PerResource:    true,
		Periods: []finops.Period{{
			BillingPeriod: "2026-09",
			Series: []finops.Series{
				{OrganizationID: testOrg, ProjectID: testProd, ProjectName: "production", SKU: "/compute/dev1_s/run_par1", ResourceID: "bbbbbbbb-0000-4000-8000-000000000001", ResourceName: "web-1", Euros: 0.35},
				{OrganizationID: testOrg, ProjectID: testProd, ProjectName: "production", SKU: "/compute/dev1_s/run_par1", ResourceID: "bbbbbbbb-0000-4000-8000-000000000002", Euros: 1.05},
			},
		}},
	}
	checkGolden(t, NewFinOps(static[finops.Snapshot]{snap}, true), "finops_per_resource")
}

func TestFinOpsCollectorWithoutSnapshot(t *testing.T) {
	t.Parallel()

	if n := testutil.CollectAndCount(NewFinOps(static[finops.Snapshot]{nil}, false)); n != 0 {
		t.Errorf("collected %d metrics before the first refresh, want 0", n)
	}
}
