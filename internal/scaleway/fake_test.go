package scaleway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Obviously fake credentials: the SDK validates their format, not their value.
const (
	fakeAccessKey = "SCWXXXXXXXXXXXXXXXXX"
	fakeSecretKey = "00000000-0000-4000-8000-000000000000"
	fakeOrgID     = "11111111-0000-4000-8000-000000000000"
)

// fakeAPI is an httptest server standing in for api.scaleway.com. Handlers
// are registered per "METHOD /path" pattern and requests are recorded.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server
	mux    *http.ServeMux

	mu       sync.Mutex
	requests []*http.Request
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, mux: http.NewServeMux()}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		f.mu.Unlock()
		if r.Header.Get("X-Auth-Token") != fakeSecretKey {
			writeJSON(w, http.StatusUnauthorized, `{"type":"denied_authentication","method":"api_key","reason":"invalid_argument","message":"authentication is denied"}`)
			return
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// client returns an SDK client pointed at the fake server, without reading
// the environment, so that tests using it can run in parallel.
func (f *fakeAPI) client() *Client {
	f.t.Helper()
	c, err := scw.NewClient(
		scw.WithAuth(fakeAccessKey, fakeSecretKey),
		scw.WithDefaultOrganizationID(fakeOrgID),
		scw.WithAPIURL(f.server.URL),
		scw.WithHTTPClient(f.server.Client()),
	)
	if err != nil {
		f.t.Fatalf("scw.NewClient() error = %v", err)
	}
	return &Client{SDK: c, OrganizationID: fakeOrgID}
}

func (f *fakeAPI) handle(pattern string, h http.HandlerFunc) {
	f.mux.HandleFunc(pattern, h)
}

func (f *fakeAPI) recorded() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// pagedFixture serves the list stored under listKey in a testdata fixture,
// honoring the page and page_size query parameters like the real API does.
func pagedFixture(t *testing.T, fixture, listKey string) http.HandlerFunc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(doc[listKey], &items); err != nil {
		t.Fatalf("parsing fixture list %q: %v", listKey, err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		page, size := 1, len(items)
		if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
			page = v
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && v > 0 {
			size = v
		}
		start := min((page-1)*size, len(items))
		end := min(start+size, len(items))

		out := make(map[string]json.RawMessage, len(doc))
		for k, v := range doc {
			out[k] = v
		}
		pageItems, _ := json.Marshal(items[start:end])
		out[listKey] = pageItems
		body, _ := json.Marshal(out)
		writeJSON(w, http.StatusOK, string(body))
	}
}
