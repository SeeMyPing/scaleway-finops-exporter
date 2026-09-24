package collector

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

func TestFootprintCollector(t *testing.T) {
	t.Parallel()

	day := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	snap := &footprint.Snapshot{
		OrganizationID: testOrg,
		Months: []footprint.Window{
			{
				Label: "2026-09",
				Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
				Series: []footprint.Series{
					{ProjectID: testProd, Region: "fr-par", ServiceCategory: "storage", ProductCategory: "object_storage", CarbonGrams: 123, WaterCubicMeters: 0.0011},
					{ProjectID: testProd, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 12000, WaterCubicMeters: 0.04},
				},
			},
			// The current month on its first day: no series, no end timestamp.
			{Label: "2026-10"},
		},
		Day: footprint.Window{
			Label: "2026-09-23",
			Start: day,
			End:   day.AddDate(0, 0, 1),
			Series: []footprint.Series{
				{ProjectID: testProd, Region: "fr-par", Zone: "fr-par-1", ServiceCategory: "compute", ProductCategory: "instances", CarbonGrams: 520, WaterCubicMeters: 0.0017},
			},
		},
	}
	checkGolden(t, NewFootprint(static[footprint.Snapshot]{snap}), "footprint")
}

func TestFootprintCollectorWithoutSnapshot(t *testing.T) {
	t.Parallel()

	if n := testutil.CollectAndCount(NewFootprint(static[footprint.Snapshot]{nil})); n != 0 {
		t.Errorf("collected %d metrics before the first refresh, want 0", n)
	}
	if n := testutil.CollectAndCount(NewFootprint(static[footprint.Snapshot]{&footprint.Snapshot{}})); n != 0 {
		t.Errorf("collected %d metrics from an empty snapshot, want 0", n)
	}
}
