package opencostengine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeRelay records what the shim forwarded, standing in for relay-server.
type relayCapture struct {
	path   string
	method string
	secret string
	acct   string
	scope  string
	body   string
}

func fakeRelay(t *testing.T, cap *relayCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read forwarded body: %v", err)
		}
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.secret = r.Header.Get("X-SECRET-KEY")
		cap.acct = r.Header.Get("X-NB-ACCOUNT-ID")
		cap.scope = r.Header.Get("X-Scope-OrgID")
		cap.body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
}

// Without an observability backend every Prometheus path must proxy straight to
// the relay's passthrough facade, translating OpenCost's auth pair to the one
// the passthrough expects.
func TestShimPassthroughRoutesAllPathsToV2(t *testing.T) {
	for _, path := range []string{
		"/api/v1/query",
		"/api/v1/query_range",
		"/api/v1/series",
		"/api/v1/labels",
		"/api/v1/label/__name__/values",
	} {
		t.Run(path, func(t *testing.T) {
			var cap relayCapture
			relay := fakeRelay(t, &cap)
			defer relay.Close()

			fb, err := newRelayFallback(relay.URL, "sekrit")
			if err != nil {
				t.Fatal(err)
			}
			// serviceURL empty => passthrough mode
			shim := newPrometheusShim("", "", fb)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Scope-OrgID", "acct-1")
			shim.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if want := "/prometheus-v2" + path; cap.path != want {
				t.Errorf("forwarded path = %q; want %q", cap.path, want)
			}
			if cap.secret != "sekrit" {
				t.Errorf("X-SECRET-KEY = %q; the passthrough authenticates on this", cap.secret)
			}
			if cap.acct != "acct-1" {
				t.Errorf("X-NB-ACCOUNT-ID = %q; the passthrough selects the account on this", cap.acct)
			}
			if cap.scope != "acct-1" {
				t.Errorf("X-Scope-OrgID = %q; should be carried across, not dropped", cap.scope)
			}
		})
	}
}

// OpenCost's Prometheus client posts its query as a form by default, so the
// body has to survive the hop. This is the shape that used to hang the agent
// before k8s-agent#613.
func TestShimPassthroughForwardsPostBody(t *testing.T) {
	var cap relayCapture
	relay := fakeRelay(t, &cap)
	defer relay.Close()

	fb, err := newRelayFallback(relay.URL, "sekrit")
	if err != nil {
		t.Fatal(err)
	}
	shim := newPrometheusShim("", "", fb)

	form := url.Values{"query": {"up"}, "start": {"1"}, "end": {"2"}, "step": {"60"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query_range", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Scope-OrgID", "acct-1")
	rec := httptest.NewRecorder()
	shim.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %q; want POST", cap.method)
	}
	if !strings.Contains(cap.body, "query=up") {
		t.Errorf("body not forwarded: %q", cap.body)
	}
}

// With an observability backend configured, queries are answered from it and
// only the metadata endpoints proxy. This guards the existing behaviour.
func TestShimObservabilityModeStillProxiesMetadata(t *testing.T) {
	var cap relayCapture
	relay := fakeRelay(t, &cap)
	defer relay.Close()

	metrics := fakeMetricsServer(t, "tok", `{"results":[{"query_key":"q","query":"up","payload":[]}]}`, nil)
	defer metrics.Close()

	fb, err := newRelayFallback(relay.URL, "sekrit")
	if err != nil {
		t.Fatal(err)
	}
	shim := newPrometheusShim(metrics.URL, "tok", fb)

	// A query must NOT reach the relay.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/query?query=up&time=1", nil)
	req.Header.Set("X-Scope-OrgID", "acct-1")
	shim.Handler().ServeHTTP(rec, req)
	if cap.path != "" {
		t.Errorf("query reached the relay at %q; it should be served from the observability backend", cap.path)
	}

	// Metadata still proxies.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/labels", nil)
	req.Header.Set("X-Scope-OrgID", "acct-1")
	shim.Handler().ServeHTTP(rec, req)
	if cap.path != "/prometheus-v2/api/v1/labels" {
		t.Errorf("metadata path = %q; want /prometheus-v2/api/v1/labels", cap.path)
	}
}
