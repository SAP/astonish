package openai_compat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectResponsesWithoutKeyOmitsAuthorization(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exists := r.Header["Authorization"]; exists {
			t.Error("unauthenticated probe must omit Authorization")
		}
		fmt.Fprint(w, `{"data":[{"id":"wanted"}]}`)
	}))
	defer s.Close()
	if _, ok := DetectResponsesBaseURL(context.Background(), "", s.URL+"/litellm/v1", "wanted"); !ok {
		t.Fatal("unauthenticated model endpoint should be detected")
	}
}

func TestDetectResponsesBaseURLExactRouteAuthAndModel(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/openai/v1/models" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"other"},{"id":"wanted"}]}`))
	}))
	defer s.Close()

	got, ok := DetectResponsesBaseURL(context.Background(), "secret", s.URL+"/litellm/v1", "wanted")
	if !ok || got != s.URL+"/openai/v1" || requests.Load() != 1 {
		t.Fatalf("got (%q,%v), requests=%d", got, ok, requests.Load())
	}
	// A positive result is cached.
	if again, ok := DetectResponsesBaseURL(context.Background(), "secret", s.URL+"/litellm/v1/", "wanted"); !ok || again != got || requests.Load() != 1 {
		t.Fatalf("cached call got (%q,%v), requests=%d", again, ok, requests.Load())
	}
}

func TestResponsesURLNormalizationAndSafety(t *testing.T) {
	for _, suffix := range []string{"/litellm", "/litellm/", "/litellm/v1", "/litellm/v1/", "/litellm/v1///"} {
		t.Run(suffix, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			}))
			defer s.Close()
			if got, ok := DetectResponsesBaseURL(context.Background(), "k", s.URL+suffix, "m"); !ok || got != s.URL+"/openai/v1" {
				t.Fatalf("got (%q,%v)", got, ok)
			}
		})
	}

	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer s.Close()
	unsafe := []string{
		s.URL + "/v1", s.URL + "/openai/v1", s.URL + "/x/litellm/v1?secret=1", s.URL + "/litellm/v1#secret",
		s.URL + "/litellm/v1#", strings.Replace(s.URL, "http://", "http://user:password@", 1) + "/litellm/v1",
		"file:///litellm/v1", "//example.com/litellm/v1",
	}
	for _, u := range unsafe {
		if got, ok := DetectResponsesBaseURL(context.Background(), "secret", u, "m"); ok || got != "" {
			t.Errorf("unsafe/non-sibling URL %q returned (%q,%v)", u, got, ok)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("rejected URLs caused %d requests", requests.Load())
	}
}

func TestResponsesRequiresExactTypedModelID(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"wrong-model", `{"data":[{"id":"model-10"}]}`},
		{"metadata-only", `{"data":[{"model_name":"model-1","model_info":{}}]}`},
		{"missing-data", `{"models":[{"id":"model-1"}]}`},
		{"null-data", `{"data":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer s.Close()
			if got, ok := DetectResponsesBaseURL(context.Background(), "k", s.URL+"/litellm/v1", "model-1"); ok || got != "" {
				t.Fatalf("got (%q,%v)", got, ok)
			}
		})
	}
	if got, ok := DetectResponsesBaseURL(context.Background(), "k", "https://example.invalid/litellm/v1", ""); ok || got != "" {
		t.Fatal("empty model was accepted")
	}
}

func TestResponsesCacheIsolationAndNegativeExpiry(t *testing.T) {
	var requests atomic.Int32
	var supported atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !supported.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, key)
	}))
	defer s.Close()
	base := s.URL + "/litellm/v1"
	if _, ok := DetectResponsesBaseURL(context.Background(), "a", base, "a"); ok {
		t.Fatal("404 supported")
	}
	supported.Store(true)
	if _, ok := DetectResponsesBaseURL(context.Background(), "a", base, "a"); ok || requests.Load() != 1 {
		t.Fatal("negative result was not cached")
	}
	expireResponsesForTest(t, "a", base, "a")
	if _, ok := DetectResponsesBaseURL(context.Background(), "a", base, "a"); !ok {
		t.Fatal("did not recover after negative expiry")
	}
	if _, ok := DetectResponsesBaseURL(context.Background(), "b", base, "b"); !ok {
		t.Fatal("key was not isolated")
	}
	if _, ok := DetectResponsesBaseURL(context.Background(), "a", base, "b"); ok {
		t.Fatal("model was not isolated")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[{"id":"a"}]}`)) }))
	defer other.Close()
	if got, ok := DetectResponsesBaseURL(context.Background(), "a", other.URL+"/litellm/v1", "a"); !ok || got != other.URL+"/openai/v1" {
		t.Fatal("URL was not isolated")
	}
}

func expireResponsesForTest(t *testing.T, apiKey, configured, model string) {
	t.Helper()
	normalized, derived, ok := normalizeResponsesURLs(configured)
	if !ok {
		t.Fatal("bad test URL")
	}
	key := responsesCacheKey(normalized, derived, apiKey, model)
	responsesCache.Lock()
	defer responsesCache.Unlock()
	entry, ok := responsesCache.entries[key]
	if !ok {
		t.Fatal("missing cache entry")
	}
	if left := time.Until(entry.expires); left <= 0 || left > time.Minute {
		t.Fatalf("negative TTL %s", left)
	}
	entry.expires = time.Now().Add(-time.Second)
	responsesCache.entries[key] = entry
}

func TestResponsesTransientFailuresRecover(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"5xx", 503, "outage"}, {"malformed", 200, `{"data":[`}, {"oversized", 200, strings.Repeat(" ", responsesBodyLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recovered atomic.Bool
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if recovered.Load() {
					_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			base := s.URL + "/litellm/v1"
			if _, ok := DetectResponsesBaseURL(context.Background(), "k", base, "m"); ok {
				t.Fatal("transient accepted")
			}
			recovered.Store(true)
			if _, ok := DetectResponsesBaseURL(context.Background(), "k", base, "m"); !ok {
				t.Fatal("transient cached")
			}
		})
	}
}

func TestResponsesCancellationAndRedirectSafety(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := DetectResponsesBaseURL(ctx, "k", s.URL+"/litellm/v1", "m"); ok || requests.Load() != 0 {
		t.Fatal("pre-canceled request sent")
	}

	started := make(chan struct{}, 1)
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	defer s2.Close()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { _, ok := DetectResponsesBaseURL(ctx2, "k", s2.URL+"/litellm/v1", "m"); done <- ok }()
	<-started
	cancel2()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("canceled probe succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was not prompt")
	}

	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
	}))
	defer source.Close()
	DetectResponsesBaseURL(context.Background(), "secret", source.URL+"/litellm/v1", "m")
	if targetRequests.Load() != 0 {
		t.Fatal("redirect target contacted")
	}
}
