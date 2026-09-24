// Package scaleway adapts the Scaleway SDK to the small interfaces declared by
// the data sources. It is the only package that imports the SDK.
package scaleway

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// ClientConfig configures the SDK client.
type ClientConfig struct {
	// OrganizationID overrides the organization from the environment or profile.
	OrganizationID string
	// Profile selects a named profile from the configuration file. Empty means
	// the active profile (SCW_PROFILE, then active_profile, then the root profile).
	Profile string
	// SecretKeyFile, if set, holds the secret key and overrides every other source.
	SecretKeyFile string
	// HTTPTimeout bounds a single HTTP request.
	HTTPTimeout time.Duration
	// UserAgent identifies the exporter in Scaleway API logs.
	UserAgent string

	// apiURL and transport are only overridden by tests to target an httptest server.
	apiURL    string
	transport http.RoundTripper
}

// Client is a configured SDK client together with the organization it targets.
type Client struct {
	SDK            *scw.Client
	OrganizationID string
}

// NewClient builds an SDK client. Credentials are resolved like the Scaleway
// CLI does (configuration file profile, overridden by SCW_* environment
// variables), then overridden by cfg. Returned errors never contain secrets.
func NewClient(cfg ClientConfig) (*Client, error) {
	profile, err := loadProfile(cfg.Profile)
	if err != nil {
		return nil, err
	}

	if cfg.SecretKeyFile != "" {
		secret, readErr := readSecretFile(cfg.SecretKeyFile)
		if readErr != nil {
			return nil, readErr
		}
		profile.SecretKey = &secret
	}
	if cfg.OrganizationID != "" {
		profile.DefaultOrganizationID = &cfg.OrganizationID
	}

	switch {
	case profile.AccessKey == nil || *profile.AccessKey == "":
		return nil, errors.New("scaleway: no access key found (set SCW_ACCESS_KEY or configure a profile)")
	case profile.SecretKey == nil || *profile.SecretKey == "":
		return nil, errors.New("scaleway: no secret key found (set SCW_SECRET_KEY, --scaleway.secret-key-file or configure a profile)")
	case profile.DefaultOrganizationID == nil || *profile.DefaultOrganizationID == "":
		return nil, errors.New("scaleway: no organization ID found (set --scaleway.organization-id, SCW_DEFAULT_ORGANIZATION_ID or configure a profile)")
	}

	httpClient := &http.Client{Timeout: cfg.HTTPTimeout, Transport: cfg.transport}
	opts := []scw.ClientOption{
		scw.WithProfile(profile),
		scw.WithHTTPClient(httpClient),
		scw.WithUserAgent(cfg.UserAgent),
	}
	if cfg.apiURL != "" {
		opts = append(opts, scw.WithAPIURL(cfg.apiURL))
	}

	client, err := scw.NewClient(opts...)
	if err != nil {
		// Some SDK validation messages quote the invalid secret key verbatim.
		return nil, fmt.Errorf("scaleway: creating client: %s", redact(err.Error(), *profile.SecretKey))
	}
	return &Client{SDK: client, OrganizationID: *profile.DefaultOrganizationID}, nil
}

// loadProfile merges the configuration file profile with the SCW_* environment.
// A missing configuration file is not an error: the environment may be enough.
func loadProfile(name string) (*scw.Profile, error) {
	fileProfile := &scw.Profile{}

	config, err := scw.LoadConfig()
	var notFound *scw.ConfigFileNotFoundError
	switch {
	case errors.As(err, &notFound):
		if name != "" {
			return nil, fmt.Errorf("scaleway: profile %q requested but no configuration file found", name)
		}
	case err != nil:
		return nil, fmt.Errorf("scaleway: loading configuration file: %w", err)
	case name != "":
		if fileProfile, err = config.GetProfile(name); err != nil {
			return nil, fmt.Errorf("scaleway: loading profile %q: %w", name, err)
		}
	default:
		if fileProfile, err = config.GetActiveProfile(); err != nil {
			return nil, fmt.Errorf("scaleway: loading active profile: %w", err)
		}
	}

	return scw.MergeProfiles(fileProfile, scw.LoadEnvProfile()), nil
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("scaleway: reading secret key file: %w", err)
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", fmt.Errorf("scaleway: secret key file %s is empty", path)
	}
	return secret, nil
}

func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}
