package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

type workflowCredStore struct {
	store.CredentialStore
	entries map[string]*store.Credential
	writes  int
}

func (s *workflowCredStore) List(context.Context) map[string]store.CredentialType {
	out := map[string]store.CredentialType{}
	for k, v := range s.entries {
		out[k] = v.Type
	}
	return out
}
func (s *workflowCredStore) ListSecrets(context.Context) []string              { return nil }
func (s *workflowCredStore) Reload(context.Context) error                      { return nil }
func (s *workflowCredStore) Get(_ context.Context, n string) *store.Credential { return s.entries[n] }
func (s *workflowCredStore) Set(_ context.Context, n string, c *store.Credential) error {
	s.writes++
	s.entries[n] = c
	return nil
}
func (s *workflowCredStore) Remove(_ context.Context, n string) error {
	s.writes++
	delete(s.entries, n)
	return nil
}
func (s *workflowCredStore) Resolve(context.Context, string) (string, string, error) {
	panic("daemon credential resolution forbidden")
}

type workflowFlowStore struct {
	store.FlowStore
	reads  int
	writes int
	data   map[string]string
}

func (s *workflowFlowStore) GetFlow(ctx context.Context, n string) (string, error) {
	s.reads++
	if store.TeamSlugFromContext(ctx) != "owner-team" {
		panic("wrong team")
	}
	v, ok := s.data[n]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return v, nil
}
func (s *workflowFlowStore) SaveFlow(_ context.Context, n, v string) error {
	s.writes++
	s.data[n] = v
	return nil
}

type workflowDocs struct {
	store.DocsStore
	calls   int
	session string
	deck    *store.DeckManifest
}

func (s *workflowDocs) GetDeck(_ context.Context, n string) (*store.DeckManifest, error) {
	if s.deck != nil && s.deck.Slug == n {
		return s.deck, nil
	}
	return nil, store.ErrDocsNotFound
}
func (s *workflowDocs) CreateDeck(ctx context.Context, d *store.DeckManifest) error {
	s.calls++
	s.session = store.SessionIDFromContext(ctx)
	s.deck = d
	return nil
}
func (s *workflowDocs) ListDecks(context.Context) ([]*store.DeckManifest, error) { return nil, nil }
func (s *workflowDocs) ListDecksLite(context.Context) ([]*store.DeckManifest, error) {
	s.calls++
	return []*store.DeckManifest{{Slug: "owner-deck", Title: "Scoped deck"}}, nil
}
func (s *workflowDocs) ListSlides(context.Context, string) ([]*store.SlideContent, error) {
	return nil, nil
}

func workflowFactory(t *testing.T, f *botFactoryFixture, names ...string) (func(string, map[string]any) (map[string]any, error), func()) {
	t.Helper()
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: names})
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Prepare = func(_ context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		ts, err := BotWorkflowTools(r, s)
		if err != nil {
			return nil, err
		}
		return &BotChatSafeResources{Tools: ts, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity()), f.req.Policy)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}})
	tc := botFactoryHandoffContext{base: ctx, session: f.req.Run.SessionID.String()}
	var execute botRunnableTool
	for _, x := range r.Agent.Tools {
		if x.Name() == "execute_tool" {
			execute = x.(botRunnableTool)
		}
	}
	if execute == nil {
		t.Fatal("missing Chat bridge")
	}
	return func(n string, a map[string]any) (map[string]any, error) {
		return execute.Run(tc, map[string]any{"name": n, "arguments": a})
	}, r.Cleanup
}

func TestBotWorkflowFactoryDispatch(t *testing.T) {
	f := newBotFactoryFixture(t)
	cs := &workflowCredStore{entries: map[string]*store.Credential{"owner-key": {Type: store.CredBearer, Token: "workflow-secret-never-return"}}}
	fs := &workflowFlowStore{data: map[string]string{"smoke": "name: smoke\ntype: drill\nsuite: owner\nnodes: []\n"}}
	ds := &workflowDocs{}
	f.services.PersonalCredentials = cs
	f.services.Flows = fs
	f.services.PersonalDocs = ds
	call, cleanup := workflowFactory(t, f, "resolve_credential", "save_credential", "remove_credential", "read_drill", "edit_drill", "create_deck", "list_decks")
	defer cleanup()
	got, err := call("resolve_credential", map[string]any{"name": "owner-key"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "workflow-secret-never-return") || !strings.Contains(string(raw), "CREDENTIAL:owner-key:token") {
		t.Fatalf("unsafe or missing placeholder: %s", raw)
	}
	got, err = call("save_credential", map[string]any{"name": "saved", "type": "bearer", "token": "new-workflow-secret"})
	if err != nil || cs.writes != 1 {
		t.Fatalf("save: %v %v writes=%d", got, err, cs.writes)
	}
	got, err = call("remove_credential", map[string]any{"name": "saved"})
	if err != nil || cs.writes != 2 {
		t.Fatalf("remove: %v %v", got, err)
	}
	got, err = call("read_drill", map[string]any{"name": "smoke"})
	if err != nil || fs.reads == 0 {
		t.Fatalf("drill: %v %v", got, err)
	}
	updated := "name: smoke\ntype: drill\nsuite: owner\nnodes:\n  - name: verify\n    tool: shell_command\n    args:\n      command: echo scoped\n"
	got, err = call("edit_drill", map[string]any{"name": "smoke", "yaml": updated})
	if err != nil || fs.writes != 1 || fs.data["smoke"] != updated {
		t.Fatalf("edit drill: %v %v", got, err)
	}
	got, err = call("create_deck", map[string]any{"slug": "scoped", "title": "Scoped deck"})
	if err != nil || ds.calls == 0 || ds.session != f.req.Run.SessionID.String() {
		t.Fatalf("deck: %v %v calls=%d session=%s", got, err, ds.calls, ds.session)
	}
	count := ds.calls
	f.req.Bot.Status = store.BotPaused
	if _, err = call("list_decks", map[string]any{}); err == nil || ds.calls != count {
		t.Fatal("revoked bot reached docs")
	}
}

func TestBotWorkflowUnsupportedNoFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   map[string]any
		reason string
	}{
		{"read_drill", map[string]any{"name": "host-file"}, "filesystem fallback"},
		{"resolve_credential", map[string]any{"name": "host-key"}, "global fallback"},
		{"test_credential", map[string]any{"name": "host-key"}, "daemon OAuth"},
		{"add_deck_image", map[string]any{"deck_slug": "owner", "url": "http://127.0.0.1/secret"}, "daemon HTTP"},
		{"create_deck", map[string]any{"slug": "owner", "title": "Owner", "titleImage": "https://example.com/image.png"}, "daemon HTTP"},
		{"ask_user", map[string]any{"prompt": "hello", "kind": "yesno"}, "checkpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.PersonalDocs = &workflowDocs{}
			call, done := workflowFactory(t, f, tc.name)
			defer done()
			_, err := call(tc.name, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestBotWorkflowTeamNeverUsesPersonal(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Bot.Scope = store.BotScopeTeam
	cs := &workflowCredStore{entries: map[string]*store.Credential{}}
	ds := &workflowDocs{}
	f.services.PersonalCredentials = cs
	f.services.PersonalDocs = ds
	call, done := workflowFactory(t, f, "list_decks", "save_credential")
	defer done()
	if _, err := call("list_decks", map[string]any{}); err == nil {
		t.Fatal("team read personal decks")
	}
	if _, err := call("save_credential", map[string]any{"name": "x", "type": "bearer", "token": "secret"}); err == nil {
		t.Fatal("team wrote credential")
	}
	if cs.writes != 0 || ds.calls != 0 {
		t.Fatal("personal store reached")
	}
}

func TestBotWorkflowFleetProfileAndExactReview(t *testing.T) {
	f := newBotFactoryFixture(t)
	call, done := workflowFactory(t, f, "get_setup_profile", "save_fleet_plan")
	defer done()
	got, err := call("get_setup_profile", map[string]any{"profile_key": "generic"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), "generic") {
		t.Fatalf("profile handler not reached: %s", raw)
	}
	if _, err = call("get_setup_profile", map[string]any{"profile_key": "generic", "draft_id": "other-session"}); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("draft isolation: %v", err)
	}
	action, err := (BotCatalogueClassifier{}).DescribeAction(context.Background(), "save_credential", json.RawMessage(`{"name":"x"}`))
	if err != nil || action.Effect != execution.EffectUnknown {
		t.Fatal("workflow mutations exempted from exact review")
	}
	all, err := BotWorkflowTools(f.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BotCatalogueTools(&catalogueClient{}, f.req.Run.SessionID.String(), all...); err != nil {
		t.Fatalf("production catalogue composition: %v", err)
	}
}

func TestBotWorkflowSchemasAndRawGuard(t *testing.T) {
	f := newBotFactoryFixture(t)
	all, err := BotWorkflowTools(f.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	originals := map[string]tool.Tool{}
	for _, makeTools := range []func() ([]tool.Tool, error){tools.GetCredentialTools, tools.GetDrillTools, slides.GetTools, tools.GetFleetPlanTools, tools.GetFleetPlanValidateTools, tools.GetFleetSetupTools, tools.GetFleetTools} {
		ts, e := makeTools()
		if e != nil {
			t.Fatal(e)
		}
		for _, x := range ts {
			originals[x.Name()] = x
		}
	}
	for _, x := range all {
		r := x.(botRunnableTool)
		if !reflect.DeepEqual(r.Declaration(), originals[x.Name()].(botRunnableTool).Declaration()) {
			t.Fatalf("schema changed: %s", x.Name())
		}
		if _, err = r.Run(catalogueContext{botFactoryToolContext{base: context.Background()}}, map[string]any{}); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("raw guard: %s %v", x.Name(), err)
		}
	}
}
