// Command scaleway-finops-exporter exports Scaleway billing and environmental
// footprint data as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/version"
	"golang.org/x/sync/errgroup"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/config"
	"github.com/SeeMyPing/scaleway-finops-exporter/internal/server"
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

	handler, err := server.NewHandler(server.Options{
		TelemetryPath: cfg.Web.TelemetryPath,
		Registry:      reg,
		Ready:         func() bool { return false },
		Version:       version.Version,
		Logger:        logger,
	})
	if err != nil {
		return fmt.Errorf("creating HTTP handler: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return server.Run(ctx, handler, cfg.Web.Toolkit, cfg.Web.ShutdownTimeout, logger)
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("exporter stopped: %w", err)
	}
	logger.Info("exporter stopped")
	return nil
}
