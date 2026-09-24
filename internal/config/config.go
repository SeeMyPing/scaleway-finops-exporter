// Package config parses and validates the exporter command-line flags.
//
// Every flag can also be set through an environment variable named after the
// application and the flag, for example --billing.interval can be set with
// SCALEWAY_FINOPS_EXPORTER_BILLING_INTERVAL. Scaleway credentials are loaded by
// the SDK itself (SCW_* variables or the ~/.config/scw/config.yaml profile).
package config

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/common/promslog"
	promslogflag "github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

const (
	// AppName is the application name, also used as the environment variable prefix.
	AppName = "scaleway-finops-exporter"

	// DefaultListenAddress uses a port that is not allocated in the Prometheus
	// default port allocations registry.
	DefaultListenAddress = ":10056"

	maxLookbackPeriods = 12
)

// ErrHelp is returned by Parse when --help or --version was requested.
var ErrHelp = errors.New("help or version requested")

// uuidPattern matches the lowercase UUIDs Scaleway uses for organizations and projects.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Config is the validated exporter configuration.
type Config struct {
	Scaleway  Scaleway
	Billing   Billing
	FinOps    FinOps
	Footprint Footprint
	Web       Web
	Log       *promslog.Config
}

// Scaleway holds the API access settings that are not handled by the SDK itself.
type Scaleway struct {
	// OrganizationID overrides the SDK default organization. Empty means "use the SDK default".
	OrganizationID string
	// ProjectIDs restricts the exported data to these projects. Empty means all projects.
	ProjectIDs []string
	// Profile selects a profile from the SDK configuration file. Empty means the active profile.
	Profile string
	// SecretKeyFile is a file holding the secret key, for Kubernetes secret mounts.
	SecretKeyFile string
	// HTTPTimeout bounds every single HTTP request made by the SDK.
	HTTPTimeout time.Duration
}

// Source holds the settings shared by every data source.
type Source struct {
	Enabled  bool
	Interval time.Duration
	Timeout  time.Duration
}

// Billing configures the consumption and taxes source.
type Billing struct {
	Source
	LookbackPeriods int
}

// FinOps configures the raw charges source.
type FinOps struct {
	Source
	LookbackPeriods int
	PerResource     bool
	MaxSeries       int
}

// Footprint configures the environmental footprint source.
type Footprint struct {
	Source
	LookbackPeriods int
	// DailyOffsetDays selects which complete UTC day is exported by the daily
	// metrics: 1 means yesterday. Increase it if the API publishes data late.
	DailyOffsetDays int
}

// Web configures the HTTP server.
type Web struct {
	Toolkit         *web.FlagConfig
	TelemetryPath   string
	ShutdownTimeout time.Duration
}

// Parse parses args (without the program name) and validates the result.
// Help and version requests are written to out and reported as ErrHelp.
func Parse(args []string, out io.Writer) (*Config, error) {
	app := kingpin.New(AppName, "Prometheus exporter for Scaleway billing (FinOps) and environmental footprint (GreenOps) data.")
	app.DefaultEnvars()
	app.UsageWriter(out)
	app.ErrorWriter(out)
	app.Version(version.Print(AppName))
	app.HelpFlag.Short('h')

	exited := false
	app.Terminate(func(int) { exited = true })

	cfg := &Config{Log: &promslog.Config{}}

	app.Flag("scaleway.organization-id", "Scaleway organization ID. Defaults to the SDK default organization (SCW_DEFAULT_ORGANIZATION_ID or profile).").
		StringVar(&cfg.Scaleway.OrganizationID)
	app.Flag("scaleway.project-id", "Only export data for this project ID. Repeatable. Defaults to all projects.").
		StringsVar(&cfg.Scaleway.ProjectIDs)
	app.Flag("scaleway.profile", "Profile to use from the Scaleway configuration file. Defaults to the active profile.").
		StringVar(&cfg.Scaleway.Profile)
	app.Flag("scaleway.secret-key-file", "File containing the Scaleway secret key. Overrides SCW_SECRET_KEY and the profile.").
		StringVar(&cfg.Scaleway.SecretKeyFile)
	app.Flag("scaleway.http-timeout", "Timeout of a single HTTP request to the Scaleway API.").
		Default("60s").DurationVar(&cfg.Scaleway.HTTPTimeout)

	addSourceFlags(app, "billing", "consumption and taxes", &cfg.Billing.Source, "1h", "2m")
	app.Flag("billing.lookback-periods", "Number of previous billing periods (months) to export in addition to the current one.").
		Default("1").IntVar(&cfg.Billing.LookbackPeriods)

	addSourceFlags(app, "finops", "FinOps raw charges", &cfg.FinOps.Source, "1h", "5m")
	app.Flag("finops.lookback-periods", "Number of previous billing periods (months) to export in addition to the current one.").
		Default("0").IntVar(&cfg.FinOps.LookbackPeriods)
	app.Flag("finops.per-resource", "Export charges per resource (resource_id, resource_name labels) instead of per SKU.").
		Default("false").BoolVar(&cfg.FinOps.PerResource)
	app.Flag("finops.max-series", "Maximum number of charge series; the remainder is aggregated under sku=\"other\".").
		Default("2000").IntVar(&cfg.FinOps.MaxSeries)

	addSourceFlags(app, "footprint", "environmental footprint", &cfg.Footprint.Source, "12h", "2m")
	app.Flag("footprint.lookback-periods", "Number of previous months to export in addition to the current one.").
		Default("1").IntVar(&cfg.Footprint.LookbackPeriods)
	app.Flag("footprint.daily-offset-days", "Complete UTC day exported by the daily footprint metrics, counted back from today (1 = yesterday).").
		Default("1").IntVar(&cfg.Footprint.DailyOffsetDays)

	cfg.Web.Toolkit = kingpinflag.AddFlags(app, DefaultListenAddress)
	app.Flag("web.telemetry-path", "Path under which to expose metrics.").
		Default("/metrics").StringVar(&cfg.Web.TelemetryPath)
	app.Flag("web.shutdown-timeout", "Maximum time to wait for in-flight requests on shutdown.").
		Default("10s").DurationVar(&cfg.Web.ShutdownTimeout)

	promslogflag.AddFlags(app, cfg.Log)

	_, err := app.Parse(args)
	if exited {
		return nil, ErrHelp
	}
	if err != nil {
		return nil, fmt.Errorf("parsing flags: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func addSourceFlags(app *kingpin.Application, name, desc string, src *Source, interval, timeout string) {
	app.Flag(name+".enabled", fmt.Sprintf("Enable the %s source.", desc)).
		Default("true").BoolVar(&src.Enabled)
	app.Flag(name+".interval", fmt.Sprintf("Refresh interval of the %s source.", desc)).
		Default(interval).DurationVar(&src.Interval)
	app.Flag(name+".timeout", fmt.Sprintf("Timeout of one refresh of the %s source, including every API call.", desc)).
		Default(timeout).DurationVar(&src.Timeout)
}

// Validate checks the configuration and returns every problem found, joined.
func (c *Config) Validate() error {
	var errs []error

	if c.Scaleway.OrganizationID != "" && !uuidPattern.MatchString(c.Scaleway.OrganizationID) {
		errs = append(errs, fmt.Errorf("--scaleway.organization-id %q is not a valid UUID", c.Scaleway.OrganizationID))
	}
	for _, id := range c.Scaleway.ProjectIDs {
		if !uuidPattern.MatchString(id) {
			errs = append(errs, fmt.Errorf("--scaleway.project-id %q is not a valid UUID", id))
		}
	}
	if c.Scaleway.HTTPTimeout <= 0 {
		errs = append(errs, errors.New("--scaleway.http-timeout must be positive"))
	}

	if !c.Billing.Enabled && !c.FinOps.Enabled && !c.Footprint.Enabled {
		errs = append(errs, errors.New("at least one source must be enabled (--billing.enabled, --finops.enabled, --footprint.enabled)"))
	}
	errs = append(errs, c.Billing.validate("billing")...)
	errs = append(errs, c.FinOps.validate("finops")...)
	errs = append(errs, c.Footprint.validate("footprint")...)
	errs = append(errs, validateLookback("billing", c.Billing.LookbackPeriods)...)
	errs = append(errs, validateLookback("finops", c.FinOps.LookbackPeriods)...)
	errs = append(errs, validateLookback("footprint", c.Footprint.LookbackPeriods)...)
	if c.FinOps.MaxSeries < 1 {
		errs = append(errs, errors.New("--finops.max-series must be at least 1"))
	}
	if c.Footprint.DailyOffsetDays < 1 || c.Footprint.DailyOffsetDays > 31 {
		errs = append(errs, errors.New("--footprint.daily-offset-days must be between 1 and 31"))
	}

	switch {
	case !strings.HasPrefix(c.Web.TelemetryPath, "/"):
		errs = append(errs, fmt.Errorf("--web.telemetry-path %q must start with /", c.Web.TelemetryPath))
	case c.Web.TelemetryPath == "/" || c.Web.TelemetryPath == "/healthz" || c.Web.TelemetryPath == "/readyz":
		errs = append(errs, fmt.Errorf("--web.telemetry-path %q conflicts with a built-in endpoint", c.Web.TelemetryPath))
	}
	if c.Web.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("--web.shutdown-timeout must be positive"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return nil
}

func (s Source) validate(name string) []error {
	if !s.Enabled {
		return nil
	}
	var errs []error
	if s.Interval < time.Minute {
		errs = append(errs, fmt.Errorf("--%s.interval must be at least 1m, got %s", name, s.Interval))
	}
	if s.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("--%s.timeout must be positive", name))
	} else if s.Timeout > s.Interval {
		errs = append(errs, fmt.Errorf("--%s.timeout (%s) must not exceed --%s.interval (%s)", name, s.Timeout, name, s.Interval))
	}
	return errs
}

func validateLookback(name string, n int) []error {
	if n < 0 || n > maxLookbackPeriods {
		return []error{fmt.Errorf("--%s.lookback-periods must be between 0 and %d, got %d", name, maxLookbackPeriods, n)}
	}
	return nil
}
