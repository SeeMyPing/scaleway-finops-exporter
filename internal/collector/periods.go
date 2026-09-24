package collector

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Periods exposes the billing periods (UTC calendar months) used as the
// billing_period label, so that PromQL can select the current period and
// compute how much of it has elapsed. It is computed from the clock at scrape
// time and never calls the Scaleway API.
type Periods struct {
	now      func() time.Time
	lookback int
	info     *prometheus.Desc
	start    *prometheus.Desc
	end      *prometheus.Desc
}

var _ prometheus.Collector = (*Periods)(nil)

// NewPeriods returns a collector for the current period and the lookback
// previous ones. now defaults to time.Now.
func NewPeriods(now func() time.Time, lookback int) *Periods {
	if now == nil {
		now = time.Now
	}
	return &Periods{
		now:      now,
		lookback: lookback,
		info: prometheus.NewDesc(
			"scaleway_billing_period_info",
			`Billing periods exported by this exporter. offset is 0 for the current period, 1 for the previous one, and so on. `+
				`Select the current period with: and on(billing_period) scaleway_billing_period_info{offset="0"}`,
			[]string{"billing_period", "offset"}, nil,
		),
		start: prometheus.NewDesc(
			"scaleway_billing_period_start_timestamp_seconds",
			"Unix timestamp of the start of the billing period (inclusive, UTC).",
			[]string{"billing_period"}, nil,
		),
		end: prometheus.NewDesc(
			"scaleway_billing_period_end_timestamp_seconds",
			"Unix timestamp of the end of the billing period (exclusive, UTC).",
			[]string{"billing_period"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *Periods) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.info
	ch <- c.start
	ch <- c.end
}

// Collect implements prometheus.Collector.
func (c *Periods) Collect(ch chan<- prometheus.Metric) {
	now := c.now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := range c.lookback + 1 {
		start := first.AddDate(0, -i, 0)
		period := start.Format("2006-01")
		ch <- prometheus.MustNewConstMetric(c.info, prometheus.GaugeValue, 1, period, strconv.Itoa(i))
		ch <- prometheus.MustNewConstMetric(c.start, prometheus.GaugeValue, float64(start.Unix()), period)
		ch <- prometheus.MustNewConstMetric(c.end, prometheus.GaugeValue, float64(start.AddDate(0, 1, 0).Unix()), period)
	}
}
