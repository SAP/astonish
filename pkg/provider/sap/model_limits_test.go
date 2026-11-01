package sap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// memLimits is an in-memory ModelLimitsStore for tests.
type memLimits struct {
	mu   sync.Mutex
	data map[string]store.ModelLimitEntry
}

func newMemLimits() *memLimits {
	return &memLimits{data: map[string]store.ModelLimitEntry{}}
}

func (m *memLimits) Get(_ context.Context, provider, modelName string) (*store.ModelLimitEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[store.ModelLimitsKey(provider, modelName)]
	if !ok {
		return nil, nil
	}
	cp := e
	return &cp, nil
}

func (m *memLimits) UpsertMaxOutput(_ context.Context, provider, modelName string, max int, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := store.ModelLimitsKey(provider, modelName)
	if existing, ok := m.data[key]; ok && existing.MaxOutputTokens > 0 && existing.MaxOutputTokens <= max {
		return nil
	}
	e := m.data[key]
	e.MaxOutputTokens = max
	e.Source = source
	e.UpdatedAt = time.Now().UTC()
	m.data[key] = e
	return nil
}

func (m *memLimits) UpsertSupportsTools(_ context.Context, provider, modelName string, supports bool, source string) error {
	if supports {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := store.ModelLimitsKey(provider, modelName)
	if existing, ok := m.data[key]; ok && existing.SupportsTools != nil && !*existing.SupportsTools {
		return nil
	}
	e := m.data[key]
	f := false
	e.SupportsTools = &f
	e.Source = source
	e.UpdatedAt = time.Now().UTC()
	m.data[key] = e
	return nil
}

func TestResolveMaxOutputTokens_LearnedOverridesStatic(t *testing.T) {
	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() { SetModelLimitsStore(nil) })

	if got := resolveMaxOutputTokens(context.Background(), "gemini-image-test-xyz"); got != 64000 {
		t.Fatalf("fallback: got %d, want 64000", got)
	}

	_ = mem.UpsertMaxOutput(context.Background(), sapAICoreProviderType, "gemini-image-test-xyz", 32768, "learned_400")
	if got := resolveMaxOutputTokens(context.Background(), "gemini-image-test-xyz"); got != 32768 {
		t.Fatalf("learned: got %d, want 32768", got)
	}
}

func TestMemLimits_UpsertOnlyDecreases(t *testing.T) {
	t.Parallel()
	mem := newMemLimits()
	ctx := context.Background()
	_ = mem.UpsertMaxOutput(ctx, "sap_ai_core", "m", 32768, "learned_400")
	_ = mem.UpsertMaxOutput(ctx, "sap_ai_core", "m", 64000, "learned_400")
	e, _ := mem.Get(ctx, "sap_ai_core", "m")
	if e.MaxOutputTokens != 32768 {
		t.Fatalf("got %d, want 32768 (only-decrease)", e.MaxOutputTokens)
	}
	_ = mem.UpsertMaxOutput(ctx, "sap_ai_core", "m", 8192, "learned_400")
	e, _ = mem.Get(ctx, "sap_ai_core", "m")
	if e.MaxOutputTokens != 8192 {
		t.Fatalf("got %d, want 8192 (decrease allowed)", e.MaxOutputTokens)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGenerateVertexContent_LearnsAndRetries(t *testing.T) {
	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() { SetModelLimitsStore(nil) })

	var calls int
	var secondMax int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		gen, _ := payload["generationConfig"].(map[string]any)
		maxF, _ := gen["maxOutputTokens"].(float64)

		if calls == 1 {
			if int(maxF) != 64000 {
				t.Errorf("first call maxOutputTokens=%v, want 64000", maxF)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Unable to submit request because it has a maxOutputTokens value of 64000 but the supported range is from 1 (inclusive) to 32769 (exclusive).","status":"INVALID_ARGUMENT"}}`))
			return
		}
		secondMax = int(maxF)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`))
	}))
	defer srv.Close()

	p := &Provider{
		httpClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return http.DefaultTransport.RoundTrip(req)
		})},
		baseURL:      srv.URL + "/v2",
		deploymentID: "dep1",
		modelName:    "gemini-image-learn-retry",
		authConfig:   &sapTransport{resourceGroup: "default"},
	}

	req := &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Hi"}}}},
	}

	var gotText string
	var gotErr error
	for resp, err := range p.generateVertexContent(context.Background(), req, false) {
		if err != nil {
			gotErr = err
			break
		}
		if resp != nil && resp.Content != nil && len(resp.Content.Parts) > 0 {
			gotText = resp.Content.Parts[0].Text
		}
	}
	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if gotText != "ok" {
		t.Fatalf("got text %q, want ok", gotText)
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls (fail+retry), got %d", calls)
	}
	if secondMax != 32768 {
		t.Fatalf("retry maxOutputTokens=%d, want 32768", secondMax)
	}
	entry, _ := mem.Get(context.Background(), sapAICoreProviderType, "gemini-image-learn-retry")
	if entry == nil || entry.MaxOutputTokens != 32768 {
		t.Fatalf("expected learned 32768 in store, got %+v", entry)
	}
}

func TestResolveOmitTools_LearnedFalse(t *testing.T) {
	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() { SetModelLimitsStore(nil) })

	if resolveOmitTools(context.Background(), "gemini-image-no-tools") {
		t.Fatal("expected omitTools=false when unknown")
	}
	_ = mem.UpsertSupportsTools(context.Background(), sapAICoreProviderType, "gemini-image-no-tools", false, "learned_400")
	if !resolveOmitTools(context.Background(), "gemini-image-no-tools") {
		t.Fatal("expected omitTools=true after learning false")
	}
}

func TestMemLimits_UpsertSupportsToolsOnlyFalse(t *testing.T) {
	t.Parallel()
	mem := newMemLimits()
	ctx := context.Background()
	_ = mem.UpsertSupportsTools(ctx, "sap_ai_core", "m", true, "learned_400")
	e, _ := mem.Get(ctx, "sap_ai_core", "m")
	if e != nil {
		t.Fatalf("expected no entry when upserting true, got %+v", e)
	}
	_ = mem.UpsertSupportsTools(ctx, "sap_ai_core", "m", false, "learned_400")
	e, _ = mem.Get(ctx, "sap_ai_core", "m")
	if e == nil || e.SupportsTools == nil || *e.SupportsTools {
		t.Fatalf("expected supports_tools=false, got %+v", e)
	}
	// Coexist with max output on same entry.
	_ = mem.UpsertMaxOutput(ctx, "sap_ai_core", "m", 32768, "learned_400")
	e, _ = mem.Get(ctx, "sap_ai_core", "m")
	if e.MaxOutputTokens != 32768 || e.SupportsTools == nil || *e.SupportsTools {
		t.Fatalf("expected both fields, got %+v", e)
	}
}

func TestGenerateVertexContent_LearnsNoToolsAndRetries(t *testing.T) {
	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() { SetModelLimitsStore(nil) })

	var calls int
	var secondHadTools bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		_, hasTools := payload["tools"]
		_, hasToolConfig := payload["toolConfig"]

		if calls == 1 {
			if !hasTools && !hasToolConfig {
				t.Error("first call expected tools or toolConfig")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Unable to submit request because the model does not support function calling. Learn more: https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/gemini","status":"INVALID_ARGUMENT"}}`))
			return
		}
		secondHadTools = hasTools || hasToolConfig
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`))
	}))
	defer srv.Close()

	p := &Provider{
		httpClient:   srv.Client(),
		baseURL:      srv.URL + "/v2",
		deploymentID: "dep1",
		modelName:    "gemini-image-no-fc",
		authConfig:   &sapTransport{resourceGroup: "default"},
	}

	req := &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Hi"}}}},
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "read_file",
					Description: "read a file",
				}},
			}},
		},
	}

	var gotText string
	var gotErr error
	for resp, err := range p.generateVertexContent(context.Background(), req, false) {
		if err != nil {
			gotErr = err
			break
		}
		if resp != nil && resp.Content != nil && len(resp.Content.Parts) > 0 {
			gotText = resp.Content.Parts[0].Text
		}
	}
	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if gotText != "ok" {
		t.Fatalf("got text %q, want ok", gotText)
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls (fail+retry), got %d", calls)
	}
	if secondHadTools {
		t.Fatal("retry request still included tools/toolConfig")
	}
	entry, _ := mem.Get(context.Background(), sapAICoreProviderType, "gemini-image-no-fc")
	if entry == nil || entry.SupportsTools == nil || *entry.SupportsTools {
		t.Fatalf("expected learned supports_tools=false, got %+v", entry)
	}
}

func TestListModelsWithMetadata_UsesLearnedMax(t *testing.T) {
	// Reset cache
	invalidateModelCache()

	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() {
		SetModelLimitsStore(nil)
		invalidateModelCache()
	})
	_ = mem.UpsertMaxOutput(context.Background(), sapAICoreProviderType, "gemini-image-meta", 32768, "learned_400")

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gemini-image-meta"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	models, err := ListModelsWithMetadata(
		context.Background(),
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL,
		"default",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].MaxOutputTokens != 32768 {
		t.Errorf("MaxOutputTokens=%d, want learned 32768", models[0].MaxOutputTokens)
	}
}

func TestListModelsWithMetadata_UsesLearnedSupportsTools(t *testing.T) {
	invalidateModelCache()

	mem := newMemLimits()
	SetModelLimitsStore(mem)
	t.Cleanup(func() {
		SetModelLimitsStore(nil)
		invalidateModelCache()
	})
	_ = mem.UpsertSupportsTools(context.Background(), sapAICoreProviderType, "gemini-image-meta-tools", false, "learned_400")

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gemini-image-meta-tools"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	models, err := ListModelsWithMetadata(
		context.Background(),
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL,
		"default",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].SupportsTools == nil || *models[0].SupportsTools {
		t.Errorf("SupportsTools=%v, want false", models[0].SupportsTools)
	}
}
