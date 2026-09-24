package refresher

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type snapshot struct{ version int }

var errAPI = errors.New("api unavailable")

// step is the scripted outcome of one Fetch call.
type step struct {
	snap *snapshot
	err  error
	// block makes Fetch wait for its context to be done and return ctx.Err().
	block bool
}

// observation records what the refresher looked like when Fetch was called.
type observation struct {
	at       time.Duration // time since the test started
	snapshot *snapshot     // Snapshot() at call time, i.e. after the previous refresh
	up       float64
	errors   float64 // errors_total summed over all reasons
}

// harness drives a Refresher through a script of Fetch outcomes. Once the
// script is exhausted, Fetch cancels the run context, which makes Run return.
type harness struct {
	t        *testing.T
	r        *Refresher[snapshot]
	metrics  *Metrics
	start    time.Time
	script   []step
	observed []observation
	cancel   context.CancelFunc
}

func newHarness(t *testing.T, script []step, configure func(*Options[snapshot])) *harness {
	t.Helper()

	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	h := &harness{t: t, metrics: metrics, start: time.Now(), script: script}

	opts := Options[snapshot]{
		Name:           "test",
		Fetch:          h.fetch,
		Interval:       time.Hour,
		Timeout:        2 * time.Minute,
		MinBackoff:     30 * time.Second,
		MaxBackoff:     4 * time.Minute,
		JitterFraction: 0.25,
		Rand:           func() float64 { return 0.5 }, // 0.5 means "no jitter"
		Classify:       func(error) string { return "api" },
		Metrics:        metrics,
		Logger:         slog.New(slog.DiscardHandler),
	}
	if configure != nil {
		configure(&opts)
	}
	h.r, err = New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h
}

func (h *harness) fetch(ctx context.Context) (*snapshot, error) {
	i := len(h.observed)
	h.observed = append(h.observed, observation{
		at:       time.Since(h.start),
		snapshot: h.r.Snapshot(),
		up:       testutil.ToFloat64(h.metrics.up.WithLabelValues("test")),
		errors:   sumCounters(h.t, h.metrics.errors),
	})
	if i >= len(h.script) {
		h.cancel()
		return nil, ctx.Err()
	}
	s := h.script[i]
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.snap, s.err
}

// run executes Run until the script is exhausted and returns the call offsets.
func (h *harness) run() []time.Duration {
	h.t.Helper()
	ctx, cancel := context.WithCancel(h.t.Context())
	defer cancel()
	h.cancel = cancel
	if err := h.r.Run(ctx); err != nil {
		h.t.Fatalf("Run() error = %v, want nil after cancellation", err)
	}
	offsets := make([]time.Duration, len(h.observed))
	for i, o := range h.observed {
		offsets[i] = o.at
	}
	return offsets
}

func sumCounters(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	go func() { c.Collect(ch); close(ch) }()
	var sum float64
	for m := range ch {
		sum += testutil.ToFloat64(constCollector{m})
	}
	return sum
}

// constCollector adapts a single metric so that testutil.ToFloat64 can read it.
type constCollector struct{ m prometheus.Metric }

func (c constCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.m.Desc() }
func (c constCollector) Collect(ch chan<- prometheus.Metric) { ch <- c.m }

func equalDurations(t *testing.T, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d calls at %v, want %d calls at %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d at %v, want %v (all calls: %v)", i, got[i], want[i], got)
		}
	}
}

func TestRunRefreshesImmediatelyThenEveryInterval(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{
			{snap: &snapshot{1}}, {snap: &snapshot{2}}, {snap: &snapshot{3}},
		}, nil)

		equalDurations(t, h.run(), []time.Duration{0, time.Hour, 2 * time.Hour, 3 * time.Hour})

		if got := h.observed[3].snapshot; got == nil || got.version != 3 {
			t.Errorf("snapshot before the last call = %+v, want version 3", got)
		}
	})
}

func TestRunAppliesJitterToTheInterval(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Successive random draws: 0 is the earliest possible refresh,
		// 0.75 is half way to the latest one.
		draws := []float64{0, 0.75, 0.5}
		h := newHarness(t, []step{
			{snap: &snapshot{1}}, {snap: &snapshot{2}}, {snap: &snapshot{3}},
		}, func(o *Options[snapshot]) {
			o.Rand = func() float64 {
				d := draws[0]
				draws = draws[1:]
				return d
			}
		})

		// Interval 1h, JitterFraction 0.25: delays are 45m, 67m30s, then 60m.
		equalDurations(t, h.run(), []time.Duration{
			0,
			45 * time.Minute,
			45*time.Minute + 67*time.Minute + 30*time.Second,
			45*time.Minute + 67*time.Minute + 30*time.Second + time.Hour,
		})
	})
}

func TestRunBacksOffExponentiallyOnErrors(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{
			{err: errAPI}, {err: errAPI}, {err: errAPI}, {err: errAPI}, {err: errAPI},
		}, nil)

		// MinBackoff 30s doubling up to MaxBackoff 4m: 30s, 1m, 2m, 4m, 4m.
		equalDurations(t, h.run(), []time.Duration{
			0,
			30 * time.Second,
			90 * time.Second,
			210 * time.Second,
			450 * time.Second,
			690 * time.Second,
		})
	})
}

func TestRunResetsBackoffAfterSuccess(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{
			{err: errAPI}, {err: errAPI}, {snap: &snapshot{1}}, {err: errAPI},
		}, nil)

		// 30s, 1m, then a full interval after the success, then back to 30s.
		equalDurations(t, h.run(), []time.Duration{
			0,
			30 * time.Second,
			90 * time.Second,
			90*time.Second + time.Hour,
			90*time.Second + time.Hour + 30*time.Second,
		})
	})
}

func TestRunKeepsLastSnapshotOnError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{
			{snap: &snapshot{1}}, {err: errAPI}, {err: errAPI}, {snap: &snapshot{2}},
		}, nil)

		if h.r.Ready() {
			t.Fatal("Ready() before the first refresh = true")
		}
		if got := testutil.ToFloat64(h.metrics.up.WithLabelValues("test")); got != 0 {
			t.Errorf("up before the first refresh = %v, want 0 (series must exist from the start)", got)
		}

		h.run()

		want := []struct {
			version int
			up      float64
			errors  float64
		}{
			{0, 0, 0}, // before the first refresh: no snapshot
			{1, 1, 0}, // after the first success
			{1, 0, 1}, // first error: snapshot kept, up drops
			{1, 0, 2}, // second error: snapshot still kept
			{2, 1, 2}, // recovered
		}
		for i, w := range want {
			o := h.observed[i]
			version := 0
			if o.snapshot != nil {
				version = o.snapshot.version
			}
			if version != w.version || o.up != w.up || o.errors != w.errors {
				t.Errorf("before call %d: version=%d up=%v errors=%v, want version=%d up=%v errors=%v",
					i, version, o.up, o.errors, w.version, w.up, w.errors)
			}
		}
		if !h.r.Ready() {
			t.Error("Ready() after a successful refresh = false")
		}
		if got := testutil.ToFloat64(h.metrics.errors.WithLabelValues("test", "api")); got != 2 {
			t.Errorf(`errors_total{reason="api"} = %v, want 2 (from Classify)`, got)
		}
	})
}

func TestRunRecordsLastSuccessTimestamp(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{{snap: &snapshot{1}}, {err: errAPI}}, nil)
		h.run()

		// The only success happened at the start; the later failure must not move it.
		want := float64(h.start.Unix())
		if got := testutil.ToFloat64(h.metrics.lastSuccess.WithLabelValues("test")); got != want {
			t.Errorf("last_success_timestamp_seconds = %v, want %v", got, want)
		}
	})
}

func TestRunTimesOutSlowFetches(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{{block: true}}, nil)

		// The blocked call is cut after Timeout (2m), then retried after MinBackoff (30s).
		equalDurations(t, h.run(), []time.Duration{0, 2*time.Minute + 30*time.Second})

		if got := testutil.ToFloat64(h.metrics.errors.WithLabelValues("test", ReasonTimeout)); got != 1 {
			t.Errorf(`errors_total{reason="timeout"} = %v, want 1`, got)
		}
	})
}

func TestRunObservesRefreshDuration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{{block: true}, {snap: &snapshot{1}}}, nil)
		h.run()

		// Two completed attempts: the 2m timeout and an instantaneous success.
		// The attempt interrupted by the final cancellation is not observed.
		want := `
# HELP scaleway_exporter_source_refresh_duration_seconds Duration of source refreshes, successful or not.
# TYPE scaleway_exporter_source_refresh_duration_seconds histogram
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="0.25"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="0.5"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="1"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="2.5"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="5"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="10"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="30"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="60"} 1
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="120"} 2
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="300"} 2
scaleway_exporter_source_refresh_duration_seconds_bucket{source="test",le="+Inf"} 2
scaleway_exporter_source_refresh_duration_seconds_sum{source="test"} 120
scaleway_exporter_source_refresh_duration_seconds_count{source="test"} 2
`
		if err := testutil.CollectAndCompare(h.metrics.duration, strings.NewReader(want)); err != nil {
			t.Error(err)
		}
	})
}

func TestRunTreatsNilSnapshotAsError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// With the default Classify, errors that cannot be classified are "unknown".
		h := newHarness(t, []step{{snap: nil, err: nil}}, func(o *Options[snapshot]) { o.Classify = nil })
		h.run()

		if h.r.Ready() {
			t.Error("a nil snapshot must not make the refresher ready")
		}
		if got := testutil.ToFloat64(h.metrics.errors.WithLabelValues("test", ReasonUnknown)); got != 1 {
			t.Errorf(`errors_total{reason="unknown"} = %v, want 1`, got)
		}
	})
}

func TestRunStopsWhileWaiting(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, []step{{snap: &snapshot{1}}}, nil)
		ctx, cancel := context.WithCancel(t.Context())
		h.cancel = cancel

		done := make(chan error, 1)
		go func() { done <- h.r.Run(ctx) }()

		// Wait until Run is idle, sleeping until the next refresh, then stop it.
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if elapsed := time.Since(h.start); elapsed != 0 {
			t.Errorf("Run() returned after %v, want immediately", elapsed)
		}
		if len(h.observed) != 1 {
			t.Errorf("Fetch called %d times, want 1", len(h.observed))
		}
	})
}

func TestRunDoesNotRecordShutdownAsError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The script is empty: the first Fetch cancels the run context and
		// returns context.Canceled, as an in-flight API call would on shutdown.
		h := newHarness(t, nil, nil)
		h.run()

		if n := testutil.CollectAndCount(h.metrics.errors); n != 0 {
			t.Errorf("errors_total has %d series, want none after a shutdown", n)
		}
		if n := testutil.CollectAndCount(h.metrics.duration); n != 0 {
			t.Errorf("refresh_duration_seconds has %d series, want none after a shutdown", n)
		}
	})
}

func TestNewAppliesDefaults(t *testing.T) {
	t.Parallel()

	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Options[snapshot]{
		Name:     "test",
		Fetch:    func(context.Context) (*snapshot, error) { return &snapshot{}, nil },
		Interval: time.Hour,
		Timeout:  time.Minute,
		Metrics:  metrics,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if r.opts.MinBackoff != DefaultMinBackoff {
		t.Errorf("MinBackoff = %v, want %v", r.opts.MinBackoff, DefaultMinBackoff)
	}
	if r.opts.MaxBackoff != time.Hour {
		t.Errorf("MaxBackoff = %v, want Interval", r.opts.MaxBackoff)
	}
	if r.opts.JitterFraction != DefaultJitterFraction {
		t.Errorf("JitterFraction = %v, want %v", r.opts.JitterFraction, DefaultJitterFraction)
	}
	if r.opts.Rand == nil {
		t.Error("Rand default not set")
	} else if v := r.opts.Rand(); v < 0 || v >= 1 {
		t.Errorf("Rand() = %v, want a value in [0, 1)", v)
	}
	if r.opts.Classify == nil {
		t.Error("Classify default not set")
	} else if got := r.opts.Classify(errAPI); got != ReasonUnknown {
		t.Errorf("default Classify() = %q, want %q", got, ReasonUnknown)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	valid := func() Options[snapshot] {
		return Options[snapshot]{
			Name:     "test",
			Fetch:    func(context.Context) (*snapshot, error) { return &snapshot{}, nil },
			Interval: time.Hour,
			Timeout:  time.Minute,
			Metrics:  metrics,
			Logger:   slog.New(slog.DiscardHandler),
		}
	}

	tests := []struct {
		name   string
		mutate func(*Options[snapshot])
	}{
		{"no name", func(o *Options[snapshot]) { o.Name = "" }},
		{"no fetch", func(o *Options[snapshot]) { o.Fetch = nil }},
		{"no metrics", func(o *Options[snapshot]) { o.Metrics = nil }},
		{"no logger", func(o *Options[snapshot]) { o.Logger = nil }},
		{"zero interval", func(o *Options[snapshot]) { o.Interval = 0 }},
		{"negative timeout", func(o *Options[snapshot]) { o.Timeout = -time.Second }},
		{"negative jitter", func(o *Options[snapshot]) { o.JitterFraction = -0.1 }},
		{"jitter of one", func(o *Options[snapshot]) { o.JitterFraction = 1 }},
		{"min above max backoff", func(o *Options[snapshot]) { o.MinBackoff, o.MaxBackoff = time.Hour, time.Minute }},
		{"default min backoff above interval", func(o *Options[snapshot]) { o.Interval, o.Timeout = 10*time.Second, time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := valid()
			tt.mutate(&opts)
			if _, err := New(opts); !errors.Is(err, ErrInvalidOptions) {
				t.Errorf("New() error = %v, want ErrInvalidOptions", err)
			}
		})
	}
}
