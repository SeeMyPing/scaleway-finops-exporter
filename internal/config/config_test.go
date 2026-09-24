package config

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	orgID     = "11111111-2222-3333-4444-555555555555"
	projectID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func TestParseDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"billing enabled", cfg.Billing.Enabled, true},
		{"billing interval", cfg.Billing.Interval, time.Hour},
		{"billing timeout", cfg.Billing.Timeout, 2 * time.Minute},
		{"billing lookback", cfg.Billing.LookbackPeriods, 1},
		{"finops enabled", cfg.FinOps.Enabled, true},
		{"finops interval", cfg.FinOps.Interval, time.Hour},
		{"finops lookback", cfg.FinOps.LookbackPeriods, 0},
		{"finops per-resource", cfg.FinOps.PerResource, false},
		{"finops max series", cfg.FinOps.MaxSeries, 2000},
		{"footprint enabled", cfg.Footprint.Enabled, true},
		{"footprint interval", cfg.Footprint.Interval, 12 * time.Hour},
		{"footprint lookback", cfg.Footprint.LookbackPeriods, 1},
		{"footprint daily offset", cfg.Footprint.DailyOffsetDays, 1},
		{"http timeout", cfg.Scaleway.HTTPTimeout, time.Minute},
		{"listen address", strings.Join(*cfg.Web.Toolkit.WebListenAddresses, ","), DefaultListenAddress},
		{"telemetry path", cfg.Web.TelemetryPath, "/metrics"},
		{"shutdown timeout", cfg.Web.ShutdownTimeout, 10 * time.Second},
		{"organization", cfg.Scaleway.OrganizationID, ""},
		{"projects", len(cfg.Scaleway.ProjectIDs), 0},
		{"log level", cfg.Log.Level.String(), "info"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseFlags(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]string{
		"--scaleway.organization-id=" + orgID,
		"--scaleway.project-id=" + projectID,
		"--scaleway.project-id=bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--scaleway.secret-key-file=/var/run/secrets/scw",
		"--no-finops.enabled",
		"--billing.interval=30m",
		"--billing.lookback-periods=3",
		"--footprint.daily-offset-days=2",
		"--web.telemetry-path=/prom",
		"--log.level=debug",
		"--log.format=json",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Scaleway.OrganizationID != orgID {
		t.Errorf("organization = %q", cfg.Scaleway.OrganizationID)
	}
	if len(cfg.Scaleway.ProjectIDs) != 2 {
		t.Errorf("projects = %v, want 2 entries", cfg.Scaleway.ProjectIDs)
	}
	if cfg.Scaleway.SecretKeyFile != "/var/run/secrets/scw" {
		t.Errorf("secret key file = %q", cfg.Scaleway.SecretKeyFile)
	}
	if cfg.FinOps.Enabled {
		t.Error("finops should be disabled")
	}
	if cfg.Billing.Interval != 30*time.Minute || cfg.Billing.LookbackPeriods != 3 {
		t.Errorf("billing = %+v", cfg.Billing)
	}
	if cfg.Footprint.DailyOffsetDays != 2 {
		t.Errorf("daily offset = %d", cfg.Footprint.DailyOffsetDays)
	}
	if cfg.Web.TelemetryPath != "/prom" {
		t.Errorf("telemetry path = %q", cfg.Web.TelemetryPath)
	}
	if cfg.Log.Level.String() != "debug" || cfg.Log.Format.String() != "json" {
		t.Errorf("log = %s/%s", cfg.Log.Level, cfg.Log.Format)
	}
}

// Environment tests cannot run in parallel because they mutate the process environment.
func TestParseEnvironment(t *testing.T) {
	t.Setenv("SCALEWAY_FINOPS_EXPORTER_BILLING_INTERVAL", "2h")
	t.Setenv("SCALEWAY_FINOPS_EXPORTER_SCALEWAY_ORGANIZATION_ID", orgID)
	t.Setenv("SCALEWAY_FINOPS_EXPORTER_FINOPS_PER_RESOURCE", "true")

	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Billing.Interval != 2*time.Hour {
		t.Errorf("billing interval = %s, want 2h", cfg.Billing.Interval)
	}
	if cfg.Scaleway.OrganizationID != orgID {
		t.Errorf("organization = %q", cfg.Scaleway.OrganizationID)
	}
	if !cfg.FinOps.PerResource {
		t.Error("per-resource should be enabled from the environment")
	}

	// Flags take precedence over the environment.
	cfg, err = Parse([]string{"--billing.interval=3h"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Billing.Interval != 3*time.Hour {
		t.Errorf("billing interval = %s, want 3h", cfg.Billing.Interval)
	}
}

func TestParseHelpAndVersion(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"--help", "--version"} {
		t.Run(arg, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			_, err := Parse([]string{arg}, &out)
			if !errors.Is(err, ErrHelp) {
				t.Fatalf("Parse(%s) error = %v, want ErrHelp", arg, err)
			}
			if out.Len() == 0 {
				t.Errorf("Parse(%s) wrote nothing", arg)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"unknown flag", []string{"--nope"}, "parsing flags"},
		{"bad duration", []string{"--billing.interval=soon"}, "parsing flags"},
		{"bad organization", []string{"--scaleway.organization-id=acme"}, "organization-id \"acme\" is not a valid UUID"},
		{"uppercase organization", []string{"--scaleway.organization-id=" + strings.ToUpper(projectID)}, "not a valid UUID"},
		{"bad project", []string{"--scaleway.project-id=prod"}, "project-id \"prod\" is not a valid UUID"},
		{"no source", []string{"--no-billing.enabled", "--no-finops.enabled", "--no-footprint.enabled"}, "at least one source must be enabled"},
		{"interval too short", []string{"--billing.interval=30s", "--billing.timeout=10s"}, "--billing.interval must be at least 1m"},
		{"timeout above interval", []string{"--footprint.interval=1h", "--footprint.timeout=2h"}, "--footprint.timeout (2h0m0s) must not exceed"},
		{"zero timeout", []string{"--finops.timeout=0s"}, "--finops.timeout must be positive"},
		{"negative lookback", []string{"--billing.lookback-periods=-1"}, "--billing.lookback-periods must be between 0 and 12"},
		{"lookback too large", []string{"--footprint.lookback-periods=13"}, "--footprint.lookback-periods must be between 0 and 12"},
		{"max series", []string{"--finops.max-series=0"}, "--finops.max-series must be at least 1"},
		{"daily offset", []string{"--footprint.daily-offset-days=0"}, "--footprint.daily-offset-days must be between 1 and 31"},
		{"telemetry path relative", []string{"--web.telemetry-path=metrics"}, "must start with /"},
		{"telemetry path conflict", []string{"--web.telemetry-path=/healthz"}, "conflicts with a built-in endpoint"},
		{"http timeout", []string{"--scaleway.http-timeout=0s"}, "--scaleway.http-timeout must be positive"},
		{"shutdown timeout", []string{"--web.shutdown-timeout=0s"}, "--web.shutdown-timeout must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.args, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Parse(%v) succeeded, want error containing %q", tt.args, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse(%v) error = %q, want it to contain %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryError(t *testing.T) {
	t.Parallel()

	_, err := Parse([]string{"--scaleway.project-id=x", "--finops.max-series=0"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"project-id", "max-series"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestDisabledSourceIsNotValidated(t *testing.T) {
	t.Parallel()

	_, err := Parse([]string{"--no-finops.enabled", "--finops.interval=1s"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("a disabled source should not be validated: %v", err)
	}
}
