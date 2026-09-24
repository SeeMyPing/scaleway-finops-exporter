package refresher

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Error reasons recorded by the refresher itself. Other reasons come from the
// Classify function given in Options.
const (
	ReasonTimeout = "timeout"
	ReasonUnknown = "unknown"
)

// Metrics holds the self-monitoring metrics shared by all refreshers.
// Each refresher only touches the series labeled with its own source name.
type Metrics struct {
	up          *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	duration    *prometheus.HistogramVec
	errors      *prometheus.CounterVec
}

// NewMetrics creates the refresher metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "scaleway_exporter_source_up",
			Help: "Whether the last refresh of the source succeeded (1) or failed (0).",
		}, []string{"source"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "scaleway_exporter_source_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful refresh of the source.",
		}, []string{"source"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "scaleway_exporter_source_refresh_duration_seconds",
			Help:    "Duration of source refreshes, successful or not.",
			Buckets: []float64{0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"source"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scaleway_exporter_source_errors_total",
			Help: "Number of source errors, by reason.",
		}, []string{"source", "reason"}),
	}
	for _, c := range []prometheus.Collector{m.up, m.lastSuccess, m.duration, m.errors} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("registering refresher metrics: %w", err)
		}
	}
	return m, nil
}

// ErrorCounter returns the error counter of a source for the given reason.
// Sources use it to report data problems that do not fail the whole refresh,
// such as rows skipped because of an unexpected currency.
func (m *Metrics) ErrorCounter(source, reason string) prometheus.Counter {
	return m.errors.WithLabelValues(source, reason)
}
