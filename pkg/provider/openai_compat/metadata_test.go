package openai_compat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestOpenAICompatibleDetectedResponsesRouting(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openai/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-astra"}]}`)
		case "/openai/v1/responses":
			fmt.Fprint(w, `{"id":"resp","status":"completed","output":[{"type":"function_call","call_id":"call","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	llm := NewProvider(context.Background(), "secret", server.URL+"/litellm/v1", "gpt-6-astra", false)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("lookup", genai.RoleUser)}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", ParametersJsonSchema: map[string]any{"type": "object"}}}}}}}
	var call *genai.FunctionCall
	for response, err := range llm.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		if response.Content != nil {
			call = response.Content.Parts[0].FunctionCall
		}
	}
	if call == nil || call.Name != "lookup" {
		t.Fatalf("call=%+v", call)
	}
	if fmt.Sprint(paths) != "[/openai/v1/models /openai/v1/responses]" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestOpenAICompatibleUndetectedStaysChat(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openai/v1/models":
			fmt.Fprint(w, `{"data":[]}`)
		case "/litellm/v1/chat/completions":
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	llm := NewProvider(context.Background(), "secret", server.URL+"/litellm/v1", "gpt-6-astra", false)
	for _, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(paths) != "[/openai/v1/models /litellm/v1/chat/completions]" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestFetchModelMetadataFieldSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/model/info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		_, _ = w.Write([]byte(`{"data":[
   {"model_name":"alias","model_info":{"context_length":128000,"context_window":99000,"max_input_tokens":64000,"max_output_tokens":8000,"max_tokens":999999}},
   {"model_name":"input-only","model_info":{"max_input_tokens":32000}},
   {"model_name":"window","model_info":{"context_length":-1,"context_window":16000}},
   {"model_name":"max-only","model_info":{"max_tokens":999999}},
   {"model_name":"negative","model_info":{"context_length":-1,"max_input_tokens":-2,"max_output_tokens":-3}},
   {"model_name":"duplicate","model_info":{"context_length":12000}},
   {"model_name":"duplicate","model_info":{"context_length":24000}},
   {"model_name":" alias ","model_info":{"context_length":1000}}
  ]}`))
	}))
	defer server.Close()
	got := FetchModelMetadata(context.Background(), "secret", server.URL)
	want := map[string]ModelMetadata{
		"alias":      {ID: "alias", Name: "alias", ContextLength: 128000, MaxInputTokens: 64000, MaxOutputTokens: 8000},
		"input-only": {ID: "input-only", Name: "input-only", ContextLength: 32000, MaxInputTokens: 32000},
		"window":     {ID: "window", Name: "window", ContextLength: 16000},
		"max-only":   {ID: "max-only", Name: "max-only"},
		"negative":   {ID: "negative", Name: "negative"},
		" alias ":    {ID: " alias ", Name: " alias ", ContextLength: 1000},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, expected := range want {
		if got[id] != expected {
			t.Errorf("%q: got %+v want %+v", id, got[id], expected)
		}
	}

}

const validMetadataJSON = `{"data":[{"model_name":"m","model_info":{"context_window":100,"max_output_tokens":20}}]}`

func TestMetadataURLNormalization(t *testing.T) {
	for _, input := range []string{"", "https://api.openai.com", "https://api.openai.com/", "https://api.openai.com/v1///"} {
		got, ok := normalizeMetadataURL(input)
		if !ok || got != "https://api.openai.com/v1" {
			t.Errorf("%q normalized as %q %v", input, got, ok)
		}
	}
	for _, input := range []string{"http://user:password@example.com", "https://example.com/#secret", "https://example.com/#", "file:///tmp", "//example.com", "://bad"} {
		if _, ok := normalizeMetadataURL(input); ok {
			t.Errorf("accepted unsafe URL %q", input)
		}
	}
	if metadataCacheKey("ab", "c") == metadataCacheKey("a", "bc") {
		t.Error("cache key framing collision")
	}
}

func TestMetadataProbePrefixesAndTrailingVariants(t *testing.T) {
	for _, prefix := range []string{"", "/litellm", "/nested/v1/litellm"} {
		for _, suffix := range []string{"", "/", "/v1", "/v1/", "/v1///"} {
			t.Run(prefix+suffix, func(t *testing.T) {
				var paths []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.URL.Path)
					if r.URL.Path == prefix+"/model/info" {
						_, _ = w.Write([]byte(validMetadataJSON))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer server.Close()
				got := FetchModelMetadata(context.Background(), "key", server.URL+prefix+suffix)
				if got["m"].ContextLength != 100 {
					t.Fatalf("missing limits: %v", got)
				}
				want := []string{prefix + "/v1/model/info", prefix + "/model/info"}
				if !reflect.DeepEqual(paths, want) {
					t.Fatalf("got paths %v want %v", paths, want)
				}
			})
		}
	}
}

func TestMetadataCacheIsolationCopiesAndConcurrency(t *testing.T) {
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		limit := 100
		if r.Header.Get("Authorization") == "Bearer second" {
			limit = 200
		}
		if strings.HasPrefix(r.URL.Path, "/other/") {
			limit += 1
		}
		fmt.Fprintf(w, `{"data":[{"model_name":"m","model_info":{"context_length":%d}}]}`, limit)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	other := httptest.NewServer(handler)
	defer other.Close()
	first := FetchModelMetadata(context.Background(), "first", server.URL)
	first["m"] = ModelMetadata{ContextLength: 999999}
	first["injected"] = ModelMetadata{}
	got := FetchModelMetadata(context.Background(), "first", server.URL+"/v1/")
	if got["m"].ContextLength != 100 || len(got) != 1 || requests.Load() != 1 {
		t.Fatalf("cache/copy failure: %v requests=%d", got, requests.Load())
	}
	if FetchModelMetadata(context.Background(), "second", server.URL)["m"].ContextLength != 200 {
		t.Error("API key isolation failed")
	}
	if FetchModelMetadata(context.Background(), "first", server.URL+"/other")["m"].ContextLength != 101 {
		t.Error("URL prefix isolation failed")
	}
	FetchModelMetadata(context.Background(), "first", other.URL)
	if requests.Load() != 4 {
		t.Fatalf("URL host isolation failed: %d", requests.Load())
	}
	key := metadataCacheKey(server.URL+"/v1", "first")
	metadataCache.Lock()
	expiry := metadataCache.entries[key].expires
	metadataCache.Unlock()
	if remaining := time.Until(expiry); remaining < 59*time.Minute || remaining > time.Hour {
		t.Errorf("success TTL %s", remaining)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := FetchModelMetadata(context.Background(), "first", server.URL)
			m["m"] = ModelMetadata{}
		}()
	}
	wg.Wait()
	if requests.Load() != 4 {
		t.Error("cached concurrent calls probed")
	}
}

func expireMetadataForTest(t *testing.T, baseURL, key string) {
	t.Helper()
	normalized, ok := normalizeMetadataURL(baseURL)
	if !ok {
		t.Fatal("invalid test URL")
	}
	cacheKey := metadataCacheKey(normalized, key)
	metadataCache.Lock()
	defer metadataCache.Unlock()
	entry, ok := metadataCache.entries[cacheKey]
	if !ok {
		t.Fatal("no negative cache entry")
	}
	remaining := time.Until(entry.expires)
	if remaining <= 0 || remaining > time.Minute {
		t.Fatalf("negative TTL: %s", remaining)
	}
	entry.expires = time.Now().Add(-time.Second)
	metadataCache.entries[cacheKey] = entry
}

func TestMetadataUnsupportedCacheExpiry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		probes int
	}{
		{"404", 404, "", 2}, {"405", 405, "", 2}, {"401", 401, "", 1}, {"403", 403, "", 1},
		{"missing-data", 200, `{"models":[]}`, 2}, {"null-data", 200, `{"data":null}`, 2},
		{"wrong-type", 200, `{"data":[{"model_name":"m","model_info":{"context_length":"100"}}]}`, 2},
		{"missing-info", 200, `{"data":[{"model_name":"m"}]}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			var recovered atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if recovered.Load() {
					_, _ = w.Write([]byte(validMetadataJSON))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if got := FetchModelMetadata(context.Background(), "key", server.URL); len(got) != 0 {
				t.Fatalf("unexpected %v", got)
			}
			recovered.Store(true)
			if got := FetchModelMetadata(context.Background(), "key", server.URL); len(got) != 0 || requests.Load() != int32(tc.probes) {
				t.Fatalf("negative cache failure %v requests=%d", got, requests.Load())
			}
			expireMetadataForTest(t, server.URL, "key")
			if got := FetchModelMetadata(context.Background(), "key", server.URL); got["m"].ContextLength != 100 {
				t.Fatalf("failed expiry recovery: %v", got)
			}
		})
	}
}

func TestMetadataTransientFailuresRecover(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"outage", 503, "secret upstream response"}, {"rate-limit", 429, ""},
		{"malformed", 200, `{"data":[`}, {"oversized", 200, strings.Repeat(" ", metadataBodyLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recovered atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if recovered.Load() {
					_, _ = w.Write([]byte(validMetadataJSON))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if got := FetchModelMetadata(context.Background(), "key", server.URL); len(got) != 0 {
				t.Fatalf("unexpected %v", got)
			}
			recovered.Store(true)
			if got := FetchModelMetadata(context.Background(), "key", server.URL); got["m"].ContextLength != 100 {
				t.Fatalf("transient failure was cached: %v", got)
			}
		})
	}
}

func TestMetadataRedirectNeverContactsTarget(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetRequests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("credential disclosed")
				}
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/secret", status) }))
			defer source.Close()
			if got := FetchModelMetadata(context.Background(), "secret", source.URL); len(got) != 0 {
				t.Fatalf("unexpected %v", got)
			}
			if targetRequests.Load() != 0 {
				t.Error("redirect target contacted")
			}
		})
	}
	// Same-origin redirects are rejected too.
	var redirected atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer source.Close()
	FetchModelMetadata(context.Background(), "secret", source.URL)
	if redirected.Load() != 0 {
		t.Error("same-origin redirect followed")
	}
}

func TestMetadataRejectUnsafeURLBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, u := range []string{strings.Replace(server.URL, "http://", "http://user:secret@", 1), server.URL + "/#fragment", server.URL + "/#"} {
		if got := FetchModelMetadata(context.Background(), "secret", u); got != nil {
			t.Errorf("unsafe URL returned %v", got)
		}
	}
	if requests.Load() != 0 {
		t.Error("unsafe URL contacted")
	}
}

func TestMetadataCancellationAndOverallTimeout(t *testing.T) {
	var requests atomic.Int32
	var recovered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if recovered.Load() {
			_, _ = w.Write([]byte(validMetadataJSON))
			return
		}
		if r.URL.Path == "/v1/model/info" {
			select {
			case <-time.After(1800 * time.Millisecond):
				w.WriteHeader(404)
			case <-r.Context().Done():
			}
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := FetchModelMetadata(ctx, "key", server.URL); got != nil || requests.Load() != 0 {
		t.Fatal("pre-canceled call sent a request")
	}
	start := time.Now()
	if got := FetchModelMetadata(context.Background(), "key", server.URL); len(got) != 0 {
		t.Fatalf("timeout returned %v", got)
	}
	elapsed := time.Since(start)
	if elapsed < 2800*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("overall timeout across probes was %s", elapsed)
	}
	recovered.Store(true)
	if got := FetchModelMetadata(context.Background(), "key", server.URL); got["m"].ContextLength != 100 {
		t.Fatalf("timeout cached: %v", got)
	}
}

func TestMetadataInFlightCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	var recovered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recovered.Load() {
			_, _ = w.Write([]byte(validMetadataJSON))
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan map[string]ModelMetadata, 1)
	go func() { result <- FetchModelMetadata(ctx, "key", server.URL) }()
	<-started
	cancel()
	select {
	case got := <-result:
		if len(got) != 0 {
			t.Errorf("canceled result: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not return promptly")
	}
	recovered.Store(true)
	if got := FetchModelMetadata(context.Background(), "key", server.URL); got["m"].ContextLength != 100 {
		t.Fatalf("cancellation cached: %v", got)
	}
}

func TestModelMetadataJSON(t *testing.T) {
	encoded, err := json.Marshal(ModelMetadata{ID: "m", Name: "m", MaxOutputTokens: 20})
	if err != nil || string(encoded) != `{"id":"m","name":"m","max_completion_tokens":20}` {
		t.Fatalf("JSON contract: %s %v", encoded, err)
	}
}
