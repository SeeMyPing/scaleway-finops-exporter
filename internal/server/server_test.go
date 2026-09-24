package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/exporter-toolkit/web"
)

func newTestHandler(t *testing.T, ready func() bool) http.Handler {
	t.Helper()

	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_answer", Help: "A test value."})
	g.Set(42)
	reg.MustRegister(g)

	h, err := NewHandler(Options{
		TelemetryPath: "/metrics",
		Registry:      reg,
		Ready:         ready,
		Version:       "test",
		Logger:        promslog.NewNopLogger(),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return rec.Code, string(body)
}

func TestHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		setReady   bool
		wantStatus int
		wantBody   string
	}{
		{"metrics", http.MethodGet, "/metrics", false, http.StatusOK, "test_answer 42"},
		{"metrics handler instrumentation", http.MethodGet, "/metrics", false, http.StatusOK, "promhttp_metric_handler_requests_total"},
		{"healthz", http.MethodGet, "/healthz", false, http.StatusOK, "ok"},
		{"readyz before first refresh", http.MethodGet, "/readyz", false, http.StatusServiceUnavailable, "not ready"},
		{"readyz after first refresh", http.MethodGet, "/readyz", true, http.StatusOK, "ready"},
		{"landing page", http.MethodGet, "/", false, http.StatusOK, "Scaleway FinOps Exporter"},
		{"unknown path", http.MethodGet, "/nope", false, http.StatusNotFound, ""},
		{"wrong method", http.MethodPost, "/metrics", false, http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHandler(t, func() bool { return tt.setReady })
			status, body := get(t, h, tt.method, tt.path)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if !strings.Contains(body, tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", body, tt.wantBody)
			}
		})
	}
}

func TestNewHandlerRequiresDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewHandler(Options{TelemetryPath: "/metrics"}); err == nil {
		t.Fatal("NewHandler() without dependencies should fail")
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	lc := net.ListenConfig{}
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func flagConfig(addr string) *web.FlagConfig {
	systemd := false
	configFile := ""
	return &web.FlagConfig{
		WebListenAddresses: &[]string{addr},
		WebSystemdSocket:   &systemd,
		WebConfigFile:      &configFile,
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	t.Parallel()

	addr := freeAddress(t)
	h := newTestHandler(t, func() bool { return true })
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, h, flagConfig(addr), time.Second, slog.New(slog.DiscardHandler))
	}()

	// Poll until the listener is up. Connection attempts to a closed port fail
	// immediately, so this loop only spins while the server goroutine starts.
	client := &http.Client{Timeout: time.Second}
	var resp *http.Response
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); runtime.Gosched() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err = client.Do(req); err == nil {
			break
		}
	}
	if resp == nil {
		t.Fatal("server never became reachable")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunReportsListenErrors(t *testing.T) {
	t.Parallel()

	err := Run(t.Context(), http.NotFoundHandler(), flagConfig("256.0.0.1:bad"), time.Second, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("Run() with an invalid address should fail")
	}
}
