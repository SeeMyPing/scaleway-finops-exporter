// Command scaleway-finops-exporter exports Scaleway billing and environmental
// footprint data as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/version"
	"golang.org/x/sync/errgroup"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/collector"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/config"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/refresher"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/scaleway"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/server"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	switch {
	case errors.Is(err, config.ErrHelp):
		os.Exit(0)
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run wires the exporter together and blocks until ctx is canceled or a component fails.
func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := config.Parse(args, out)
	if err != nil {
		return err
	}
	logger := promslog.New(cfg.Log)
	logger.Info("starting exporter", "version", version.Info(), "build_context", version.BuildContext())

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		versioncollector.NewCollector("scaleway_exporter"),
	)
	metrics, err := refresher.NewMetrics(reg)
	if err != nil {
		return err
	}

	client, err := scaleway.NewClient(scaleway.ClientConfig{
		OrganizationID: cfg.Scaleway.OrganizationID,
		Profile:        cfg.Scaleway.Profile,
		SecretKeyFile:  cfg.Scaleway.SecretKeyFile,
		HTTPTimeout:    cfg.Scaleway.HTTPTimeout,
		UserAgent:      config.AppName + "/" + version.Version,
	})
	if err != nil {
		return err
	}
	logger.Info("using Scaleway organization", "organization_id", client.OrganizationID,
		"project_filter", cfg.Scaleway.ProjectIDs)

	w := &wiring{reg: reg, metrics: metrics, logger: logger}

	// The periods collector only reads the clock; it lets PromQL select the
	// current billing_period for every source.
	lookback := max(cfg.Billing.LookbackPeriods, cfg.FinOps.LookbackPeriods, cfg.Footprint.LookbackPeriods)
	if err := reg.Register(collector.NewPeriods(time.Now, lookback)); err != nil {
		return fmt.Errorf("registering periods collector: %w", err)
	}

	if cfg.Billing.Enabled {
		if err := setupBilling(w, cfg, client); err != nil {
			return err
		}
	}
	if cfg.FinOps.Enabled {
		if err := setupFinOps(w, cfg, client); err != nil {
			return err
		}
	}
	if cfg.Footprint.Enabled {
		if err := setupFootprint(w, cfg, client); err != nil {
			return err
		}
	}

	handler, err := server.NewHandler(server.Options{
		TelemetryPath: cfg.Web.TelemetryPath,
		Registry:      reg,
		Ready:         w.ready,
		Version:       version.Version,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("creating HTTP handler: %w", err)
	}

	// errgroup cancels ctx as soon as one goroutine returns an error, which
	// stops every other component: a failing HTTP server stops the refreshers.
	g, ctx := errgroup.WithContext(ctx)
	for _, runner := range w.runners {
		g.Go(func() error { return runner(ctx) })
	}
	g.Go(func() error {
		return server.Run(ctx, handler, cfg.Web.Toolkit, cfg.Web.ShutdownTimeout, logger)
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("exporter stopped: %w", err)
	}
	logger.Info("exporter stopped")
	return nil
}

// wiring collects the background runners and readiness checks of the sources.
type wiring struct {
	reg        *prometheus.Registry
	metrics    *refresher.Metrics
	logger     *slog.Logger
	runners    []func(context.Context) error
	readyFuncs []func() bool
}

// ready reports whether at least one enabled source has published a snapshot.
func (w *wiring) ready() bool {
	for _, ready := range w.readyFuncs {
		if ready() {
			return true
		}
	}
	return false
}

// addSource creates the refresher of a source and schedules it.
func addSource[T any](w *wiring, name string, src config.Source, fetch func(context.Context) (*T, error)) (*refresher.Refresher[T], error) {
	r, err := refresher.New(refresher.Options[T]{
		Name:     name,
		Fetch:    fetch,
		Interval: src.Interval,
		Timeout:  src.Timeout,
		Classify: scaleway.Classify,
		Metrics:  w.metrics,
		Logger:   w.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("creating %s refresher: %w", name, err)
	}
	w.runners = append(w.runners, r.Run)
	w.readyFuncs = append(w.readyFuncs, r.Ready)
	w.logger.Info("source enabled", "data_source", name, "interval", src.Interval, "timeout", src.Timeout)
	return r, nil
}

func setupBilling(w *wiring, cfg *config.Config, client *scaleway.Client) error {
	src, err := billing.New(scaleway.NewBilling(client), billing.Options{
		OrganizationID:  client.OrganizationID,
		ProjectIDs:      cfg.Scaleway.ProjectIDs,
		LookbackPeriods: cfg.Billing.LookbackPeriods,
		SkippedRows:     w.metrics.ErrorCounter("billing", billing.ReasonUnexpectedCurrency),
		Logger:          w.logger.With("data_source", "billing"),
	})
	if err != nil {
		return err
	}
	r, err := addSource(w, "billing", cfg.Billing.Source, src.Fetch)
	if err != nil {
		return err
	}
	if err := w.reg.Register(collector.NewBilling(r)); err != nil {
		return fmt.Errorf("registering billing collector: %w", err)
	}
	return nil
}

func setupFinOps(w *wiring, cfg *config.Config, client *scaleway.Client) error {
	limitExceeded := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "scaleway_finops_series_limit_exceeded_total",
		Help: "Number of billing period refreshes whose charge series exceeded --finops.max-series.",
	})
	if err := w.reg.Register(limitExceeded); err != nil {
		return fmt.Errorf("registering FinOps limit counter: %w", err)
	}

	src, err := finops.New(scaleway.NewFinOps(client), finops.Options{
		OrganizationID:  client.OrganizationID,
		ProjectIDs:      cfg.Scaleway.ProjectIDs,
		LookbackPeriods: cfg.FinOps.LookbackPeriods,
		PerResource:     cfg.FinOps.PerResource,
		MaxSeries:       cfg.FinOps.MaxSeries,
		SkippedCharges:  w.metrics.ErrorCounter("finops", finops.ReasonUnexpectedCurrency),
		LimitExceeded:   limitExceeded,
		Logger:          w.logger.With("data_source", "finops"),
	})
	if err != nil {
		return err
	}
	r, err := addSource(w, "finops", cfg.FinOps.Source, src.Fetch)
	if err != nil {
		return err
	}
	if err := w.reg.Register(collector.NewFinOps(r, cfg.FinOps.PerResource)); err != nil {
		return fmt.Errorf("registering FinOps collector: %w", err)
	}
	return nil
}

func setupFootprint(w *wiring, cfg *config.Config, client *scaleway.Client) error {
	src, err := footprint.New(scaleway.NewFootprint(client), footprint.Options{
		OrganizationID:  client.OrganizationID,
		ProjectIDs:      cfg.Scaleway.ProjectIDs,
		LookbackPeriods: cfg.Footprint.LookbackPeriods,
		DailyOffsetDays: cfg.Footprint.DailyOffsetDays,
		Logger:          w.logger.With("data_source", "footprint"),
	})
	if err != nil {
		return err
	}
	r, err := addSource(w, "footprint", cfg.Footprint.Source, src.Fetch)
	if err != nil {
		return err
	}
	if err := w.reg.Register(collector.NewFootprint(r)); err != nil {
		return fmt.Errorf("registering footprint collector: %w", err)
	}
	return nil
}
