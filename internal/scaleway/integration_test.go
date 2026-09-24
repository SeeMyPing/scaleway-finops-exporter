//go:build integration

// Integration tests against the real Scaleway API. They never run by default
// nor in CI. Run them with real, read-only credentials:
//
//	SCW_ACCESS_KEY=... SCW_SECRET_KEY=... SCW_DEFAULT_ORGANIZATION_ID=... \
//	  go test -tags integration -run Integration -v ./internal/scaleway/
//
// They only check that each call succeeds and that the response parses into
// plausible values; they never print amounts or identifiers.
package scaleway

import (
	"os"
	"testing"
	"time"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("SCW_ACCESS_KEY") == "" && os.Getenv("SCW_PROFILE") == "" {
		t.Skip("set SCW_ACCESS_KEY (or SCW_PROFILE) to run integration tests")
	}
	c, err := NewClient(ClientConfig{HTTPTimeout: time.Minute, UserAgent: "scaleway-finops-exporter/integration-test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return c
}

func TestIntegrationBilling(t *testing.T) {
	c := integrationClient(t)
	period := billing.Periods(time.Now(), 0)[0]
	b := NewBilling(c)

	consumptions, err := b.Consumptions(t.Context(), c.OrganizationID, period)
	if err != nil {
		t.Fatalf("Consumptions() error = %v (reason %s)", err, Classify(err))
	}
	t.Logf("consumptions: %d rows, updated at %s", len(consumptions.Consumptions), consumptions.UpdatedAt)
	for _, row := range consumptions.Consumptions {
		if row.Value.Currency != "" && row.Value.Currency != billing.Currency {
			t.Errorf("unexpected currency %q", row.Value.Currency)
		}
	}

	taxes, err := b.Taxes(t.Context(), c.OrganizationID, period)
	if err != nil {
		t.Fatalf("Taxes() error = %v (reason %s)", err, Classify(err))
	}
	t.Logf("taxes: %d rows", len(taxes.Taxes))
}

func TestIntegrationFinOps(t *testing.T) {
	c := integrationClient(t)
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	charges, err := NewFinOps(c).Charges(t.Context(), finops.Query{
		OrganizationID: c.OrganizationID,
		Start:          start,
		End:            start.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatalf("Charges() error = %v (reason %s)", err, Classify(err))
	}
	withName := 0
	for _, ch := range charges {
		if ch.ResourceName != "" {
			withName++
		}
	}
	t.Logf("charges: %d rows, %d with a resource name", len(charges), withName)
}

func TestIntegrationFootprint(t *testing.T) {
	c := integrationClient(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)

	report, err := NewFootprint(c).ImpactData(t.Context(), footprint.Query{
		OrganizationID: c.OrganizationID,
		Start:          today.AddDate(0, 0, -7),
		End:            today,
	})
	if err != nil {
		t.Fatalf("ImpactData() error = %v (reason %s)", err, Classify(err))
	}
	regional := 0
	for _, r := range report.Rows {
		if r.Zone == "" {
			regional++
		}
		if r.CarbonGrams < 0 || r.WaterCubicMeters < 0 {
			t.Errorf("negative impact for %s/%s", r.Region, r.ProductCategory)
		}
	}
	t.Logf("footprint: %d rows (%d regional), covering %s to %s", len(report.Rows), regional, report.Start, report.End)
}
