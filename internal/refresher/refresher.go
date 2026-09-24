package refresher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"
)

// Defaults applied by New when the corresponding option is zero.
const (
	DefaultMinBackoff     = 30 * time.Second
	DefaultJitterFraction = 0.1
)

// Options configures a Refresher.
type Options[T any] struct {
	// Name is the source name, used as the "source" label and in logs.
	Name string
	// Fetch retrieves a new snapshot. It must honor ctx cancellation and must
	// not modify a snapshot after returning it.
	Fetch func(ctx context.Context) (*T, error)
	// Interval is the nominal delay between two successful refreshes.
	Interval time.Duration
	// Timeout bounds a single call to Fetch.
	Timeout time.Duration
	// MinBackoff is the delay after the first consecutive failure. It doubles
	// with each further failure, up to MaxBackoff. Defaults to DefaultMinBackoff.
	MinBackoff time.Duration
	// MaxBackoff caps the backoff. Defaults to Interval.
	MaxBackoff time.Duration
	// JitterFraction spreads successful refreshes over
	// [Interval*(1-JitterFraction), Interval*(1+JitterFraction)).
	// Defaults to DefaultJitterFraction. Must be in [0, 1).
	JitterFraction float64
	// Rand returns a pseudo-random number in [0, 1). Defaults to math/rand/v2.Float64.
	Rand func() float64
	// Classify maps a Fetch error to a low-cardinality reason label.
	// Defaults to a function returning ReasonUnknown.
	Classify func(error) string
	// Metrics receives the refresh outcome. Required.
	Metrics *Metrics
	// Logger receives refresh outcomes. Required.
	Logger *slog.Logger
}

// Refresher runs Fetch in the background and keeps the last good snapshot.
// Snapshot and Ready are safe for concurrent use with Run.
type Refresher[T any] struct {
	opts     Options[T]
	snapshot atomic.Pointer[T]
}

// ErrInvalidOptions is wrapped by the errors New returns.
var ErrInvalidOptions = errors.New("refresher: invalid options")

// New validates opts, applies defaults and returns a Refresher.
func New[T any](opts Options[T]) (*Refresher[T], error) {
	var errs []error
	if opts.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if opts.Fetch == nil {
		errs = append(errs, errors.New("fetch is required"))
	}
	if opts.Metrics == nil {
		errs = append(errs, errors.New("metrics are required"))
	}
	if opts.Logger == nil {
		errs = append(errs, errors.New("logger is required"))
	}
	if opts.Interval <= 0 {
		errs = append(errs, errors.New("interval must be positive"))
	}
	if opts.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be positive"))
	}
	if opts.JitterFraction < 0 || opts.JitterFraction >= 1 {
		errs = append(errs, errors.New("jitter fraction must be in [0, 1)"))
	}

	if opts.MinBackoff == 0 {
		opts.MinBackoff = DefaultMinBackoff
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = opts.Interval
	}
	if opts.MinBackoff > opts.MaxBackoff {
		errs = append(errs, fmt.Errorf("min backoff %s exceeds max backoff %s", opts.MinBackoff, opts.MaxBackoff))
	}
	if opts.JitterFraction == 0 {
		opts.JitterFraction = DefaultJitterFraction
	}
	if opts.Rand == nil {
		opts.Rand = rand.Float64
	}
	if opts.Classify == nil {
		opts.Classify = func(error) string { return ReasonUnknown }
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOptions, errors.Join(errs...))
	}

	// Create the series up front so that "up" reads 0 before the first refresh
	// instead of being absent.
	opts.Metrics.up.WithLabelValues(opts.Name).Set(0)

	return &Refresher[T]{opts: opts}, nil
}

// Snapshot returns the last successfully fetched snapshot, or nil if no
// refresh has succeeded yet. Callers must not modify it.
func (r *Refresher[T]) Snapshot() *T {
	return r.snapshot.Load()
}

// Ready reports whether at least one refresh has succeeded.
func (r *Refresher[T]) Ready() bool {
	return r.snapshot.Load() != nil
}

// Run refreshes immediately, then keeps refreshing until ctx is canceled.
// It returns nil when ctx is canceled: a refresh interrupted by shutdown is
// neither an error nor recorded in the metrics.
func (r *Refresher[T]) Run(ctx context.Context) error {
	failures := 0
	for {
		err := r.refresh(ctx)
		if stopping(ctx) {
			return nil
		}

		var delay time.Duration
		if err == nil {
			failures = 0
			delay = r.jitter(r.opts.Interval)
		} else {
			failures++
			delay = r.jitter(r.backoff(failures))
			r.opts.Logger.Warn("refresh failed", "source", r.opts.Name, "err", err, "retry_in", delay)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// refresh performs one Fetch and records its outcome.
func (r *Refresher[T]) refresh(ctx context.Context) error {
	attemptCtx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	start := time.Now()
	snap, err := r.opts.Fetch(attemptCtx)
	if err == nil && snap == nil {
		err = errors.New("fetch returned a nil snapshot")
	}
	if stopping(ctx) {
		// Shutting down: this attempt is neither a success nor a failure.
		return context.Cause(ctx)
	}

	m := r.opts.Metrics
	m.duration.WithLabelValues(r.opts.Name).Observe(time.Since(start).Seconds())
	if err != nil {
		reason := r.opts.Classify(err)
		if errors.Is(err, context.DeadlineExceeded) {
			reason = ReasonTimeout
		}
		m.up.WithLabelValues(r.opts.Name).Set(0)
		m.errors.WithLabelValues(r.opts.Name, reason).Inc()
		return err
	}

	r.snapshot.Store(snap)
	m.up.WithLabelValues(r.opts.Name).Set(1)
	m.lastSuccess.WithLabelValues(r.opts.Name).Set(float64(time.Now().Unix()))
	r.opts.Logger.Debug("refresh succeeded", "source", r.opts.Name, "duration", time.Since(start))
	return nil
}

// backoff returns the delay after the given number of consecutive failures.
func (r *Refresher[T]) backoff(failures int) time.Duration {
	d := r.opts.MinBackoff
	for i := 1; i < failures && d < r.opts.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, r.opts.MaxBackoff)
}

// jitter spreads d uniformly over [d*(1-JitterFraction), d*(1+JitterFraction)).
func (r *Refresher[T]) jitter(d time.Duration) time.Duration {
	factor := 1 + r.opts.JitterFraction*(2*r.opts.Rand()-1)
	return time.Duration(float64(d) * factor)
}

// stopping reports whether ctx has been canceled.
func stopping(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
