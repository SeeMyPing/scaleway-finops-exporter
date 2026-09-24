package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

// Footprint exposes the environmental footprint snapshot.
type Footprint struct {
	source      Snapshotter[footprint.Snapshot]
	carbon      *prometheus.Desc
	water       *prometheus.Desc
	periodEnd   *prometheus.Desc
	dailyCarbon *prometheus.Desc
	dailyWater  *prometheus.Desc
	dayStart    *prometheus.Desc
}

var _ prometheus.Collector = (*Footprint)(nil)

// NewFootprint returns a collector reading snapshots from source.
func NewFootprint(source Snapshotter[footprint.Snapshot]) *Footprint {
	location := []string{"organization_id", "project_id", "region", "zone", "service_category", "product_category"}
	monthly := append(append([]string(nil), location...), "billing_period")

	return &Footprint{
		source: source,
		carbon: prometheus.NewDesc(
			"scaleway_footprint_carbon_grams",
			"Estimated carbon emissions of the month, in grams of CO2 equivalent. "+
				"The current month covers the complete days elapsed so far.",
			monthly, nil,
		),
		water: prometheus.NewDesc(
			"scaleway_footprint_water_cubic_meters",
			"Estimated water consumption of the month, in cubic meters. "+
				"The current month covers the complete days elapsed so far.",
			monthly, nil,
		),
		periodEnd: prometheus.NewDesc(
			"scaleway_footprint_period_end_timestamp_seconds",
			"Unix timestamp of the (exclusive) end of the data covered by the monthly footprint metrics.",
			[]string{"organization_id", "billing_period"}, nil,
		),
		dailyCarbon: prometheus.NewDesc(
			"scaleway_footprint_daily_carbon_grams",
			"Estimated carbon emissions of the last complete day selected by --footprint.daily-offset-days, "+
				"in grams of CO2 equivalent.",
			location, nil,
		),
		dailyWater: prometheus.NewDesc(
			"scaleway_footprint_daily_water_cubic_meters",
			"Estimated water consumption of the last complete day selected by --footprint.daily-offset-days, "+
				"in cubic meters.",
			location, nil,
		),
		dayStart: prometheus.NewDesc(
			"scaleway_footprint_daily_window_start_timestamp_seconds",
			"Unix timestamp of the start of the day covered by the daily footprint metrics.",
			[]string{"organization_id"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *Footprint) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.carbon
	ch <- c.water
	ch <- c.periodEnd
	ch <- c.dailyCarbon
	ch <- c.dailyWater
	ch <- c.dayStart
}

// Collect implements prometheus.Collector.
func (c *Footprint) Collect(ch chan<- prometheus.Metric) {
	snap := c.source.Snapshot()
	if snap == nil {
		return
	}
	org := snap.OrganizationID

	for i := range snap.Months {
		m := &snap.Months[i]
		for j := range m.Series {
			s := &m.Series[j]
			labels := []string{org, s.ProjectID, s.Region, s.Zone, s.ServiceCategory, s.ProductCategory, m.Label}
			ch <- prometheus.MustNewConstMetric(c.carbon, prometheus.GaugeValue, s.CarbonGrams, labels...)
			ch <- prometheus.MustNewConstMetric(c.water, prometheus.GaugeValue, s.WaterCubicMeters, labels...)
		}
		if len(m.Series) > 0 {
			ch <- prometheus.MustNewConstMetric(c.periodEnd, prometheus.GaugeValue, float64(m.End.Unix()), org, m.Label)
		}
	}

	for j := range snap.Day.Series {
		s := &snap.Day.Series[j]
		labels := []string{org, s.ProjectID, s.Region, s.Zone, s.ServiceCategory, s.ProductCategory}
		ch <- prometheus.MustNewConstMetric(c.dailyCarbon, prometheus.GaugeValue, s.CarbonGrams, labels...)
		ch <- prometheus.MustNewConstMetric(c.dailyWater, prometheus.GaugeValue, s.WaterCubicMeters, labels...)
	}
	if !snap.Day.Start.IsZero() {
		ch <- prometheus.MustNewConstMetric(c.dayStart, prometheus.GaugeValue, float64(snap.Day.Start.Unix()), org)
	}
}
