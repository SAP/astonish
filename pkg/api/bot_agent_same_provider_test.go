package api_test

// These helpers never read the daemon's settings, environment credentials, or
// session files. The only live input is an explicitly supplied private v1 file.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func redactStockDiagnostic(v any) any {
	switch value := v.(type) {
	case map[string]any:
		if thought, _ := value["thought"].(bool); thought {
			return map[string]any{"omitted": "non-public model part"}
		}
		for key, item := range value {
			switch strings.ToLower(key) {
			case "headers", "header", "authorization", "client_secret", "access_token", "thoughtsignature", "thought_signature":
				value[key] = "[redacted]"
			case "error":
				if item != nil && item != "<nil>" && item != "" {
					value[key] = "diagnostic error (details withheld)"
				}
			default:
				value[key] = redactStockDiagnostic(item)
			}
		}
	case []any:
		for i := range value {
			value[i] = redactStockDiagnostic(value[i])
		}
	}
	return v
}

func TestBotAgentSameProviderRecorderRedaction(t *testing.T) {
	rec := &realStockHTTPRecorder{dir: t.TempDir(), redact: []string{"private-value"}}
	if err := rec.save("record.json", map[string]any{"headers": map[string]any{"Authorization": "bearer"}, "text": "private-value", "error": "upstream private body", "parts": []any{map[string]any{"thought": true, "text": "hidden"}}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rec.dir, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-value", "bearer", "upstream private body", "hidden"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("sensitive diagnostic content retained")
		}
	}
}

const stockAgentProvider = "New AI Core Provider"
const stockAgentModel = "anthropic--claude-4.6-sonnet"

type stockAgentConfig struct {
	Version  int                   `json:"schema_version"`
	Instance string                `json:"provider_instance"`
	Model    string                `json:"model"`
	Provider config.ProviderConfig `json:"provider_config"`
	Intended struct {
		Provider string `json:"provider"`
		Type     string `json:"provider_type"`
		Model    string `json:"model"`
	} `json:"intended"`
	Binary         string          `json:"binary"`
	Docker         string          `json:"docker"`
	Layers         string          `json:"layers"`
	Browser        string          `json:"browser"`
	EmbeddingCache string          `json:"embedding_cache"`
	RoutinePolicy  json.RawMessage `json:"bot_routine_policy,omitempty"`
}

func (c stockAgentConfig) validate() error {
	invalid := errors.New("explicit private version 1 same-provider configuration required")
	if c.Version != 1 || c.Instance != stockAgentProvider || c.Model != stockAgentModel || c.Intended.Provider != c.Instance || c.Intended.Model != c.Model || c.Intended.Type != "sap_ai_core" || c.Provider["type"] != "sap_ai_core" {
		return invalid
	}
	if len(c.Provider) != 6 {
		return invalid
	}
	for _, key := range []string{"type", "base_url", "auth_url", "client_id", "client_secret", "resource_group"} {
		if strings.TrimSpace(c.Provider[key]) == "" {
			return invalid
		}
	}
	for _, key := range []string{"base_url", "auth_url"} {
		u, err := url.Parse(c.Provider[key])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return invalid
		}
	}
	return nil
}

func readStockAgentConfig(path string) (stockAgentConfig, error) {
	var c stockAgentConfig
	invalid := errors.New("explicit private version 1 same-provider configuration unavailable or invalid")
	if path == "" {
		return c, invalid
	}
	f, err := os.Open(path)
	if err != nil {
		return c, invalid
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 1<<20 {
		return c, invalid
	}
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return stockAgentConfig{}, invalid
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF || c.validate() != nil {
		return stockAgentConfig{}, invalid
	}
	return c, nil
}

func (c stockAgentConfig) resolveScoped(ctx context.Context, expected execution.Principal, expectedScope store.BotScope, owner execution.Principal, scope store.BotScope, instance, name string) (model.LLM, error) {
	if scope != expectedScope || owner.Subject != expected.Subject || owner.OrgSlug != expected.OrgSlug || owner.TeamSlug != expected.TeamSlug {
		return nil, errors.New("worker scoped model identity mismatch")
	}
	return c.resolve(ctx, instance, name)
}

func (c stockAgentConfig) resolve(ctx context.Context, instance, name string) (model.LLM, error) {
	// Reject a substituted callback selection BEFORE any network or factory call.
	if err := c.validate(); err != nil {
		return nil, err
	}
	if instance != c.Instance || name != c.Model {
		return nil, errors.New("worker provider/model differs from explicit identity")
	}
	pin, err := store.ResolveBotModelSelection(ctx, &store.SessionPin{Provider: instance, Model: name}, nil, nil, nil, nil)
	if err != nil {
		return nil, errors.New("owner model selection failed")
	}
	cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{c.Instance: c.Provider}}
	llm, err := provider.GetProvider(ctx, pin.Provider, pin.Model, cfg)
	// Upstream errors may contain endpoint bodies. Never persist or log them.
	if err != nil {
		return nil, errors.New("same-provider factory failed (details withheld)")
	}
	if _, err := c.observeAdapter(llm); err != nil {
		return nil, err
	}
	return llm, nil
}

// This is an observation of the actual factory return value, not a certificate
// inferred from the configured display name. It deliberately does not claim
// wire-level identity or provenance for a different native candidate process.
type stockAdapterObservation struct {
	Provider            string `json:"provider"`
	ProviderType        string `json:"provider_type"`
	Model               string `json:"model"`
	Adapter             string `json:"adapter"`
	AdapterModel        string `json:"adapter_model"`
	ObservationBoundary string `json:"observation_boundary"`
	WireVerified        bool   `json:"wire_verified"`
}

func (c stockAgentConfig) observeAdapter(llm model.LLM) (stockAdapterObservation, error) {
	var observation stockAdapterObservation
	if err := c.validate(); err != nil {
		return observation, err
	}
	adapter, ok := llm.(*sap.Provider)
	if !ok || adapter == nil || adapter.Name() != c.Model {
		return observation, errors.New("same-provider adapter mismatch")
	}
	return stockAdapterObservation{
		Provider: c.Instance, ProviderType: c.Provider["type"], Model: c.Model,
		Adapter: fmt.Sprintf("%T", adapter), AdapterModel: adapter.Name(),
		ObservationBoundary: "diagnostic-worker-factory-return",
	}, nil
}

func stockAgentFixtureConfig(endpoint string) stockAgentConfig {
	c := stockAgentConfig{Version: 1, Instance: stockAgentProvider, Model: stockAgentModel, Provider: config.ProviderConfig{"type": "sap_ai_core", "base_url": endpoint, "auth_url": endpoint, "client_id": "fixture-id", "client_secret": "fixture-secret", "resource_group": "fixture-group"}}
	c.Intended.Provider, c.Intended.Type, c.Intended.Model = c.Instance, "sap_ai_core", c.Model
	return c
}

func TestBotAgentSameProviderConfig(t *testing.T) {
	c := stockAgentFixtureConfig("https://fixture.invalid")
	raw, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStockAgentConfig(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readStockAgentConfig(""); err == nil {
		t.Fatal("missing explicit configuration accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readStockAgentConfig(path); err == nil {
		t.Fatal("public configuration accepted")
	}
	for _, mutate := range []func(*stockAgentConfig){
		func(c *stockAgentConfig) { c.Version = 2 },
		func(c *stockAgentConfig) { c.Instance = "alias" },
		func(c *stockAgentConfig) { c.Model = "substitute" },
		func(c *stockAgentConfig) { c.Intended.Type = "litellm" },
		func(c *stockAgentConfig) { c.Provider["client_secret"] = "" },
		func(c *stockAgentConfig) { c.Provider["base_url"] = "https://secret@fixture.invalid" },
	} {
		candidate := stockAgentFixtureConfig("https://fixture.invalid")
		mutate(&candidate)
		if candidate.validate() == nil {
			t.Fatal("invalid identity or secret configuration accepted")
		}
	}
	// Wrong callback input must fail without attempting fixture.invalid.
	if _, err := c.resolve(context.Background(), "alias", c.Model); err == nil {
		t.Fatal("callback substitution accepted")
	}
}

func TestBotAgentSameProviderScopedRejection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected factory call", http.StatusForbidden)
	}))
	defer server.Close()
	c := stockAgentFixtureConfig(server.URL)
	owner := execution.Principal{Subject: "owner", OrgSlug: "org", TeamSlug: "team"}
	for _, field := range []string{"provider", "model", "owner", "org", "team", "scope"} {
		t.Run(field, func(t *testing.T) {
			actual, scope, instance, name := owner, store.BotScopePersonal, c.Instance, c.Model
			switch field {
			case "provider":
				instance = "alias"
			case "model":
				name = "substitute"
			case "owner":
				actual.Subject = "other"
			case "org":
				actual.OrgSlug = "other"
			case "team":
				actual.TeamSlug = "other"
			case "scope":
				scope = store.BotScopeTeam
			}
			if _, err := c.resolveScoped(context.Background(), owner, store.BotScopePersonal, actual, scope, instance, name); err == nil {
				t.Fatal("substituted scoped identity accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("rejected identity made network calls")
	}
}

func TestBotAgentSameProviderHomeWorkerIdentity(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "web_fetch", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	f.worker.Stop()
	pinRaw := f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"New AI Core Provider","model":"anthropic--claude-4.6-sonnet"}`, 200)
	var pin struct {
		Provider string `json:"pinnedProvider"`
		Model    string `json:"pinnedModel"`
	}
	if json.Unmarshal(pinRaw, &pin) != nil || pin.Provider != stockAgentProvider || pin.Model != stockAgentModel {
		t.Fatal("Home pin differs from intended identity")
	}
	owner, _ := execution.PrincipalFromContext(f.ctx)
	var calls atomic.Int32
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: "same-provider-identity", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(ctx context.Context, actual execution.Principal, scope store.BotScope, instance, name string) (model.LLM, error) {
		calls.Add(1)
		if actual.Subject != owner.Subject || actual.OrgSlug != owner.OrgSlug || actual.TeamSlug != owner.TeamSlug || scope != f.scope || instance != pin.Provider || name != pin.Model {
			t.Error("worker did not resolve owner-scoped Home identity")
		}
		// Deliberate stop at the factory boundary: no paid inference and no claim
		// that this controlled HTTP fixture is a native-daemon acceptance run.
		return nil, errors.New("controlled identity checkpoint")
	}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		return nil, errors.New("identity checkpoint must precede preparation")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Stock Market News","idempotency_key":"same-provider-identity"}`, f.b.Version), 202)
	_ = worker.Poll(f.ctx)
	if calls.Load() != 1 {
		t.Fatal("worker model identity checkpoint was not reached exactly once")
	}
}

func TestBotAgentSameProviderObservationRejectsSubstitute(t *testing.T) {
	c := stockAgentFixtureConfig("https://fixture.invalid")
	var typedNil *sap.Provider
	for _, llm := range []model.LLM{nil, typedNil, litellm.NewProvider("fixture", "https://fixture.invalid", c.Model)} {
		observation, err := c.observeAdapter(llm)
		if err == nil || observation != (stockAdapterObservation{}) {
			t.Fatal("same model name certified a nil or substitute adapter")
		}
	}
}

func TestBotAgentSameProviderFactoryDeployment(t *testing.T) {
	// Ordinary per-test HTTP server, not a process-wide RegisterProtocol hook.
	// This tests real SAP deployment resolution, not a substitute model adapter.
	for _, matching := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "wrong-model"}[matching], func(t *testing.T) {
			deploymentCalls, inferenceCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/oauth/token":
					_, _ = io.WriteString(w, `{"access_token":"fixture-token","expires_in":3600}`)
				case "/v2/lm/deployments":
					deploymentCalls++
					if r.URL.Query().Get("status") != "RUNNING" || r.URL.Query().Get("resourceGroup") != "fixture-group" {
						t.Error("deployment scope not applied")
					}
					name := stockAgentModel
					if !matching {
						name = "wrong-model"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"resources": []any{map[string]any{"id": "fixture-deployment", "status": "RUNNING", "details": map[string]any{"resources": map[string]any{"backendDetails": map[string]any{"model": map[string]any{"name": name}}}}}}})
				case "/v2/inference/deployments/fixture-deployment/invoke":
					inferenceCalls++
					if !matching || r.Method != http.MethodPost || r.Header.Get("AI-Resource-Group") != "fixture-group" {
						t.Error("inference identity mismatch")
					}
					_, _ = io.WriteString(w, `{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"controlled identity response"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				default:
					t.Error("unexpected deployment inference route")
					http.Error(w, "denied", 400)
				}
			}))
			defer server.Close()
			c := stockAgentFixtureConfig(server.URL)
			llm, err := c.resolve(context.Background(), c.Instance, c.Model)
			if matching && (err != nil || llm == nil || llm.Name() != c.Model) {
				t.Fatal("matching factory resolution failed")
			}
			if !matching && err == nil {
				t.Fatal("wrong deployment model accepted")
			}
			if matching {
				observation, err := c.observeAdapter(llm)
				if err != nil || observation.Provider != c.Instance || observation.ProviderType != "sap_ai_core" || observation.Adapter != "*sap.Provider" || observation.AdapterModel != c.Model || observation.Model != c.Model || observation.WireVerified || observation.ObservationBoundary != "diagnostic-worker-factory-return" {
					t.Fatal("factory adapter observation missing or overclaims provenance")
				}
				recorder := &realStockHTTPRecorder{dir: t.TempDir(), redact: []string{"fixture-secret"}}
				if err := recorder.save("worker-model-identity.json", observation); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(recorder.dir, "worker-model-identity.json"))
				var restored stockAdapterObservation
				if err != nil || json.Unmarshal(raw, &restored) != nil || restored != observation {
					t.Fatal("retained observation differs from actual factory return")
				}
				wrong := c
				wrong.Model, wrong.Intended.Model = "substitute", "substitute"
				if _, err := wrong.observeAdapter(llm); err == nil {
					t.Fatal("adapter observation accepted substituted model")
				}
				responses := 0
				for response, callErr := range llm.GenerateContent(context.Background(), &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("identity probe", "user")}}, false) {
					if callErr != nil || response == nil || response.Content == nil {
						t.Fatal("controlled deployment inference failed")
					}
					responses++
				}
				if responses != 1 || inferenceCalls != 1 {
					t.Fatal("resolved deployment was not used for inference")
				}
			} else if inferenceCalls != 0 {
				t.Fatal("wrong model reached inference")
			}
			if deploymentCalls != 1 {
				t.Fatal("deployment resolver was not exercised exactly once")
			}
		})
	}
}
