package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
)

// FinOps exposes the FinOps charges snapshot.
type FinOps struct {
	source      Snapshotter[finops.Snapshot]
	perResource bool
	charge      *prometheus.Desc
	folded      *prometheus.Desc
	lastUpdate  *prometheus.Desc
}

var _ prometheus.Collector = (*FinOps)(nil)

// NewFinOps returns a collector reading snapshots from source. perResource
// must match the source configuration: it selects the label set.
func NewFinOps(source Snapshotter[finops.Snapshot], perResource bool) *FinOps {
	labels := []string{"organization_id", "project_id", "project_name", "sku"}
	if perResource {
		labels = append(labels, "resource_id", "resource_name")
	}
	labels = append(labels, "billing_period")

	return &FinOps{
		source:      source,
		perResource: perResource,
		charge: prometheus.NewDesc(
			"scaleway_finops_charge_euros",
			"Raw charges of the billing period so far from the FinOps API, in euros. "+
				`Series above --finops.max-series are aggregated under sku="other".`,
			labels, nil,
		),
		folded: prometheus.NewDesc(
			"scaleway_finops_folded_series",
			`Number of series aggregated under sku="other" at the last refresh because of --finops.max-series.`,
			[]string{"organization_id", "billing_period"}, nil,
		),
		lastUpdate: prometheus.NewDesc(
			"scaleway_finops_last_update_timestamp_seconds",
			"Unix timestamp of the most recent charge update of the billing period.",
			[]string{"organization_id", "billing_period"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *FinOps) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.charge
	ch <- c.folded
	ch <- c.lastUpdate
}

// Collect implements prometheus.Collector.
func (c *FinOps) Collect(ch chan<- prometheus.Metric) {
	snap := c.source.Snapshot()
	if snap == nil {
		return
	}

	org := snap.OrganizationID
	for i := range snap.Periods {
		p := &snap.Periods[i]
		for j := range p.Series {
			s := &p.Series[j]
			values := []string{s.OrganizationID, s.ProjectID, s.ProjectName, s.SKU}
			if c.perResource {
				values = append(values, s.ResourceID, s.ResourceName)
			}
			values = append(values, p.BillingPeriod)
			ch <- prometheus.MustNewConstMetric(c.charge, prometheus.GaugeValue, s.Euros, values...)
		}
		ch <- prometheus.MustNewConstMetric(c.folded, prometheus.GaugeValue, float64(p.FoldedSeries),
			org, p.BillingPeriod)
		if !p.UpdatedAt.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.lastUpdate, prometheus.GaugeValue,
				float64(p.UpdatedAt.Unix()), org, p.BillingPeriod)
		}
	}
}
