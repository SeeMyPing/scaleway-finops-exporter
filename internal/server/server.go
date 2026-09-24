// Package server exposes the exporter HTTP endpoints: metrics, health and readiness.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
)

const readHeaderTimeout = 10 * time.Second

// Options configures the HTTP handler.
type Options struct {
	// TelemetryPath is where metrics are served, for example "/metrics".
	TelemetryPath string
	// Registry provides the metrics and receives the handler's own instrumentation.
	Registry *prometheus.Registry
	// Ready reports whether the exporter has data to serve. It must be safe for concurrent use.
	Ready func() bool
	// Version is displayed on the landing page.
	Version string
	// Logger receives errors encountered while gathering metrics.
	Logger *slog.Logger
}

// NewHandler returns the exporter HTTP handler.
func NewHandler(opts Options) (http.Handler, error) {
	if opts.Registry == nil || opts.Ready == nil || opts.Logger == nil {
		return nil, errors.New("server: Registry, Ready and Logger are required")
	}

	mux := http.NewServeMux()

	metrics := promhttp.HandlerFor(opts.Registry, promhttp.HandlerOpts{
		ErrorLog:      slog.NewLogLogger(opts.Logger.Handler(), slog.LevelError),
		ErrorHandling: promhttp.ContinueOnError,
		Registry:      opts.Registry,
	})
	mux.Handle("GET "+opts.TelemetryPath, promhttp.InstrumentMetricHandler(opts.Registry, metrics))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if opts.Ready() {
			writeText(w, http.StatusOK, "ready")
			return
		}
		writeText(w, http.StatusServiceUnavailable, "not ready: no source has completed a successful refresh yet")
	})

	landing, err := web.NewLandingPage(web.LandingConfig{
		Name:        "Scaleway FinOps Exporter",
		Description: "Prometheus exporter for Scaleway billing and environmental footprint data",
		Version:     opts.Version,
		Links: []web.LandingLinks{
			{Address: opts.TelemetryPath, Text: "Metrics"},
			{Address: "/healthz", Text: "Health"},
			{Address: "/readyz", Text: "Readiness"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating landing page: %w", err)
	}
	mux.Handle("GET /{$}", landing)

	return mux, nil
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

// Run serves handler until ctx is canceled, then shuts down gracefully,
// waiting at most shutdownTimeout for in-flight requests.
func Run(ctx context.Context, handler http.Handler, flags *web.FlagConfig, shutdownTimeout time.Duration, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- web.ListenAndServe(srv, flags, logger)
	}()

	select {
	case err := <-errCh:
		// The server stopped on its own: listening failed or the TLS config is invalid.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// The parent context is already canceled, so derive a fresh deadline from it
	// without inheriting the cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}
