package api

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type isolatedValidationModel struct{ calls int }

func (*isolatedValidationModel) Name() string { return "host-model" }
func (m *isolatedValidationModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText("[]", genai.RoleModel)}, nil)
	}
}

func TestValidationProviderRetainsLocalWarmModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	llm := &isolatedValidationModel{}
	previous := GetChatManager()
	globalChatManager = &ChatManager{components: &StudioChatComponents{LLM: llm, ProviderName: "host", ModelName: "host-model"}}
	t.Cleanup(func() { globalChatManager = previous })
	got := getValidationLLMProvider(httptest.NewRequest(http.MethodPost, "/", nil))
	if got == nil {
		t.Fatal("local warm model was discarded")
	}
	if _, err := got.EvaluateText(context.Background(), "review"); err != nil || llm.calls != 1 {
		t.Fatalf("local warm model calls=%d, err=%v", llm.calls, err)
	}
}

func TestValidateSkillHandlerIgnoresGlobalModelInPlatformMode(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			resetSkillValidationRateLimit(t)
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			dir := filepath.Join(home, "astonish")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("storage:\n  backend: "+backend+"\ngeneral:\n  default_provider: host\n  default_model: host-model\nproviders:\n  host:\n    type: openai\n    api_key: fixture-only\n"), 0600); err != nil {
				t.Fatal(err)
			}
			llm := &isolatedValidationModel{}
			previous := GetChatManager()
			globalChatManager = &ChatManager{components: &StudioChatComponents{LLM: llm, ProviderName: "host", ModelName: "host-model"}}
			t.Cleanup(func() { globalChatManager = previous })
			skillStore := &apiSkillStore{skills: []store.Skill{{Name: "isolated", Content: "Review changes."}}}
			svc := &store.Services{Mode: store.ModePlatform, PlatformSkills: skillStore}
			r := httptest.NewRequest(http.MethodPost, "/api/skills/isolated/validate?scope=platform", nil)
			r = mux.SetURLVars(r, map[string]string{"name": "isolated"})
			ctx := WithPlatformUser(store.WithServices(r.Context(), svc), &PlatformUser{ID: "admin", PlatformRole: "superadmin"})
			for attempt := 0; attempt < 2; attempt++ {
				w := httptest.NewRecorder()
				ValidateSkillHandler(w, r.WithContext(ctx))
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("status=%d, want 503: %s", w.Code, w.Body.String())
				}
			}
			if llm.calls != 0 || skillStore.validationUpdateCall != 0 {
				t.Fatalf("unauthorized model calls=%d, persistence=%d", llm.calls, skillStore.validationUpdateCall)
			}
			// Explicit scoped defaults still construct a provider, without falling
			// back to the global model or making an external model request.
			svc.PlatformSettings = &providerMatrixPlatformStore{value: &store.PlatformSettings{
				DefaultProvider: "scoped", DefaultModel: "scoped-model",
				Providers: map[string]map[string]string{"scoped": {"type": "openai", "api_key": "fixture-only"}},
			}}
			if got := getValidationLLMProvider(r.WithContext(ctx)); got == nil {
				t.Fatal("explicit scoped provider was not deployable")
			}
		})
	}
}
