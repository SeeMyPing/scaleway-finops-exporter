package scaleway

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var scwEnv = []string{
	"SCW_ACCESS_KEY", "SCW_SECRET_KEY", "SCW_DEFAULT_ORGANIZATION_ID", "SCW_DEFAULT_PROJECT_ID",
	"SCW_PROFILE", "SCW_API_URL", "SCW_CONFIG_PATH", "SCW_DEFAULT_REGION", "SCW_DEFAULT_ZONE", "SCW_INSECURE",
}

// isolateEnv unsets every SCW_* variable and points the SDK at a config file
// path inside a temporary directory. Tests using it cannot run in parallel.
func isolateEnv(t *testing.T) (configPath string) {
	t.Helper()
	for _, k := range scwEnv {
		t.Setenv(k, "") // registers the restoration of the original value
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	configPath = filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SCW_CONFIG_PATH", configPath)
	return configPath
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewClientFromEnvironment(t *testing.T) {
	isolateEnv(t)
	t.Setenv("SCW_ACCESS_KEY", fakeAccessKey)
	t.Setenv("SCW_SECRET_KEY", fakeSecretKey)
	t.Setenv("SCW_DEFAULT_ORGANIZATION_ID", fakeOrgID)

	api := newFakeAPI(t)
	api.handle("GET /billing/v2beta1/taxes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"taxes":[],"total_count":0}`)
	})

	c, err := NewClient(ClientConfig{
		HTTPTimeout: time.Minute,
		UserAgent:   "scaleway-finops-exporter/test",
		apiURL:      api.server.URL,
		transport:   api.server.Client().Transport,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if c.OrganizationID != fakeOrgID {
		t.Errorf("OrganizationID = %q, want %q", c.OrganizationID, fakeOrgID)
	}

	// The client really authenticates with the environment credentials.
	if _, err := NewBilling(c).Taxes(t.Context(), c.OrganizationID, "2026-09"); err != nil {
		t.Fatalf("request through the client failed: %v", err)
	}
	if ua := api.recorded()[0].Header.Get("User-Agent"); !strings.Contains(ua, "scaleway-finops-exporter/test") {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestNewClientFromProfile(t *testing.T) {
	configPath := isolateEnv(t)
	writeFile(t, configPath, fmt.Sprintf(`
access_key: SCWAAAAAAAAAAAAAAAAA
secret_key: 99999999-0000-4000-8000-000000000000
default_organization_id: 22222222-0000-4000-8000-000000000000
profiles:
  billing:
    access_key: %s
    secret_key: %s
    default_organization_id: %s
`, fakeAccessKey, fakeSecretKey, fakeOrgID))

	tests := []struct {
		name    string
		cfg     ClientConfig
		env     map[string]string
		wantOrg string
	}{
		{"root profile", ClientConfig{}, nil, "22222222-0000-4000-8000-000000000000"},
		{"named profile", ClientConfig{Profile: "billing"}, nil, fakeOrgID},
		{"SCW_PROFILE", ClientConfig{}, map[string]string{"SCW_PROFILE": "billing"}, fakeOrgID},
		{"environment overrides profile", ClientConfig{}, map[string]string{"SCW_DEFAULT_ORGANIZATION_ID": "33333333-0000-4000-8000-000000000000"}, "33333333-0000-4000-8000-000000000000"},
		{"flag overrides everything", ClientConfig{OrganizationID: "44444444-0000-4000-8000-000000000000"}, map[string]string{"SCW_DEFAULT_ORGANIZATION_ID": "33333333-0000-4000-8000-000000000000"}, "44444444-0000-4000-8000-000000000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			tt.cfg.HTTPTimeout = time.Minute
			c, err := NewClient(tt.cfg)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if c.OrganizationID != tt.wantOrg {
				t.Errorf("OrganizationID = %q, want %q", c.OrganizationID, tt.wantOrg)
			}
		})
	}
}

func TestNewClientSecretKeyFile(t *testing.T) {
	isolateEnv(t)
	t.Setenv("SCW_ACCESS_KEY", fakeAccessKey)
	t.Setenv("SCW_SECRET_KEY", "88888888-0000-4000-8000-000000000000") // must be overridden
	t.Setenv("SCW_DEFAULT_ORGANIZATION_ID", fakeOrgID)

	secretFile := filepath.Join(t.TempDir(), "secret")
	writeFile(t, secretFile, fakeSecretKey+"\n") // Kubernetes secrets often end with a newline

	api := newFakeAPI(t) // rejects any secret other than fakeSecretKey
	api.handle("GET /billing/v2beta1/taxes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"taxes":[],"total_count":0}`)
	})
	c, err := NewClient(ClientConfig{
		SecretKeyFile: secretFile,
		HTTPTimeout:   time.Minute,
		apiURL:        api.server.URL,
		transport:     api.server.Client().Transport,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := NewBilling(c).Taxes(t.Context(), fakeOrgID, "2026-09"); err != nil {
		t.Fatalf("the secret key file was not used: %v", err)
	}
}

func TestNewClientErrors(t *testing.T) {
	const invalidSecret = "not-a-uuid-but-still-a-secret"
	emptyFile := filepath.Join(t.TempDir(), "empty")
	writeFile(t, emptyFile, "  \n")
	badSecretFile := filepath.Join(t.TempDir(), "bad")
	writeFile(t, badSecretFile, invalidSecret)

	tests := []struct {
		name    string
		cfg     ClientConfig
		env     map[string]string
		config  string
		wantErr string
	}{
		{"no access key", ClientConfig{}, map[string]string{"SCW_SECRET_KEY": fakeSecretKey, "SCW_DEFAULT_ORGANIZATION_ID": fakeOrgID}, "", "no access key"},
		{"no secret key", ClientConfig{}, map[string]string{"SCW_ACCESS_KEY": fakeAccessKey, "SCW_DEFAULT_ORGANIZATION_ID": fakeOrgID}, "", "no secret key"},
		{"no organization", ClientConfig{}, map[string]string{"SCW_ACCESS_KEY": fakeAccessKey, "SCW_SECRET_KEY": fakeSecretKey}, "", "no organization ID"},
		{"missing secret file", ClientConfig{SecretKeyFile: "/nonexistent/secret"}, nil, "", "reading secret key file"},
		{"empty secret file", ClientConfig{SecretKeyFile: emptyFile}, nil, "", "is empty"},
		{"invalid secret is redacted", ClientConfig{SecretKeyFile: badSecretFile}, map[string]string{"SCW_ACCESS_KEY": fakeAccessKey, "SCW_DEFAULT_ORGANIZATION_ID": fakeOrgID}, "", "[REDACTED]"},
		{"invalid access key", ClientConfig{}, map[string]string{"SCW_ACCESS_KEY": "nope", "SCW_SECRET_KEY": fakeSecretKey, "SCW_DEFAULT_ORGANIZATION_ID": fakeOrgID}, "", "invalid access key format"},
		{"profile without config file", ClientConfig{Profile: "billing"}, nil, "", "no configuration file found"},
		{"unknown profile", ClientConfig{Profile: "nope"}, nil, "access_key: " + fakeAccessKey, "loading profile \"nope\""},
		{"unknown active profile", ClientConfig{}, map[string]string{"SCW_PROFILE": "nope"}, "access_key: " + fakeAccessKey, "loading active profile"},
		{"malformed config file", ClientConfig{}, nil, "access_key: [", "loading configuration file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := isolateEnv(t)
			if tt.config != "" {
				writeFile(t, configPath, tt.config)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			tt.cfg.HTTPTimeout = time.Minute

			_, err := NewClient(tt.cfg)
			if err == nil {
				t.Fatalf("NewClient() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NewClient() error = %q, want it to contain %q", err, tt.wantErr)
			}
			for _, secret := range []string{invalidSecret, fakeSecretKey} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("NewClient() error leaks a secret: %q", err)
				}
			}
		})
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.New("boom"), ReasonUnknown},
		{fmt.Errorf("wrapped: %w", os.ErrDeadlineExceeded), ReasonTimeout},
	}
	for _, tt := range tests {
		if got := Classify(tt.err); got != tt.want {
			t.Errorf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
	for code, want := range map[int]string{
		http.StatusUnauthorized: ReasonAuth, http.StatusTooManyRequests: ReasonRateLimited,
		http.StatusServiceUnavailable: ReasonServer, http.StatusConflict: ReasonClient, http.StatusFound: ReasonUnknown,
	} {
		if got := classifyStatus(code); got != want {
			t.Errorf("classifyStatus(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestPaginateStopsRunawayLists(t *testing.T) {
	t.Parallel()

	calls := 0
	err := paginate(func(int32) (int, uint64, error) {
		calls++
		return 1, 1 << 40, nil // the API claims far more items than it ever returns
	})
	if err == nil || calls != maxPages {
		t.Errorf("paginate() error = %v after %d calls, want an error after %d", err, calls, maxPages)
	}
}
