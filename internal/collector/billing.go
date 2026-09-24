package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
)

// Billing exposes the billing snapshot.
type Billing struct {
	source      Snapshotter[billing.Snapshot]
	consumption *prometheus.Desc
	skuInfo     *prometheus.Desc
	discount    *prometheus.Desc
	tax         *prometheus.Desc
	taxRate     *prometheus.Desc
	lastUpdate  *prometheus.Desc
}

var _ prometheus.Collector = (*Billing)(nil)

// NewBilling returns a collector reading snapshots from source.
func NewBilling(source Snapshotter[billing.Snapshot]) *Billing {
	return &Billing{
		source: source,
		consumption: prometheus.NewDesc(
			"scaleway_billing_consumption_euros",
			"Untaxed consumption of the billing period so far, as shown on the invoice, in euros.",
			[]string{"organization_id", "project_id", "project_name", "category", "product", "billing_period"}, nil,
		),
		skuInfo: prometheus.NewDesc(
			"scaleway_billing_sku_info",
			"Category, product and billing unit of a SKU. Join on sku to enrich FinOps charges.",
			[]string{"sku", "category", "product", "unit"}, nil,
		),
		discount: prometheus.NewDesc(
			"scaleway_billing_discount_euros",
			"Organization-wide untaxed discounts of the billing period, in euros, as reported by the API.",
			[]string{"organization_id", "billing_period"}, nil,
		),
		tax: prometheus.NewDesc(
			"scaleway_billing_tax_euros",
			"Taxes of the billing period so far, in euros.",
			[]string{"organization_id", "billing_period", "description"}, nil,
		),
		taxRate: prometheus.NewDesc(
			"scaleway_billing_tax_rate_ratio",
			"Rate of a tax (0.2 means 20 %).",
			[]string{"organization_id", "billing_period", "description"}, nil,
		),
		lastUpdate: prometheus.NewDesc(
			"scaleway_billing_last_update_timestamp_seconds",
			"Unix timestamp of the last update of the billing period data by Scaleway.",
			[]string{"organization_id", "billing_period"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *Billing) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.consumption
	ch <- c.skuInfo
	ch <- c.discount
	ch <- c.tax
	ch <- c.taxRate
	ch <- c.lastUpdate
}

// Collect implements prometheus.Collector.
func (c *Billing) Collect(ch chan<- prometheus.Metric) {
	snap := c.source.Snapshot()
	if snap == nil {
		return
	}

	for _, sku := range snap.SKUs {
		ch <- prometheus.MustNewConstMetric(c.skuInfo, prometheus.GaugeValue, 1,
			sku.SKU, sku.Category, sku.Product, sku.Unit)
	}

	org := snap.OrganizationID
	for i := range snap.Periods {
		p := &snap.Periods[i]
		for _, s := range p.Consumption {
			ch <- prometheus.MustNewConstMetric(c.consumption, prometheus.GaugeValue, s.Euros,
				s.OrganizationID, s.ProjectID, s.ProjectName, s.Category, s.Product, p.BillingPeriod)
		}
		ch <- prometheus.MustNewConstMetric(c.discount, prometheus.GaugeValue, p.DiscountEuros,
			org, p.BillingPeriod)
		for _, t := range p.Taxes {
			ch <- prometheus.MustNewConstMetric(c.tax, prometheus.GaugeValue, t.Euros,
				org, p.BillingPeriod, t.Description)
			if t.Rate != nil {
				ch <- prometheus.MustNewConstMetric(c.taxRate, prometheus.GaugeValue, *t.Rate,
					org, p.BillingPeriod, t.Description)
			}
		}
		if !p.UpdatedAt.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.lastUpdate, prometheus.GaugeValue,
				float64(p.UpdatedAt.Unix()), org, p.BillingPeriod)
		}
	}
}
