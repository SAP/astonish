package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type knowledgeSkillStore struct {
	store.SkillStore
	rows map[string]*store.Skill
	err  error
}

func (s knowledgeSkillStore) Get(_ context.Context, n string) (*store.Skill, error) {
	return s.rows[n], s.err
}
func (s knowledgeSkillStore) ListFiles(context.Context, string) ([]store.SkillFile, error) {
	return []store.SkillFile{{Path: "scripts", Filename: "run.sh"}}, nil
}
func (s knowledgeSkillStore) GetFile(_ context.Context, n, p, f string) (*store.SkillFile, error) {
	return &store.SkillFile{Content: n + "/" + p + "/" + f}, nil
}

type knowledgeContext struct {
	botFactoryToolContext
	sid, uid string
}

func (knowledgeContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c knowledgeContext) SessionID() string                                  { return c.sid }
func (c knowledgeContext) UserID() string                                     { return c.uid }
func (c knowledgeContext) AppName() string                                    { return "astonish" }
func knowledgeCtx(f *botFactoryFixture) knowledgeContext {
	return knowledgeContext{botFactoryToolContext: botFactoryToolContext{base: execution.WithBotRunIdentity(f.ctx, execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: f.req.Bot.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug})}, sid: f.req.Run.SessionID.String(), uid: f.req.Bot.OwnerID.String()}
}
func knowledgeScope(f *botFactoryFixture) *BotChatScope {
	return &BotChatScope{Services: f.services, Sessions: f.sessions, Models: f.resolver}
}
func TestBotKnowledgeSkillScopeAndFiles(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Bot.Scope = store.BotScopeTeam
	row := func(n, v string) *store.Skill {
		return &store.Skill{Name: n, Content: v, ValidationStatus: "clean", Directory: "/never-read-host"}
	}
	f.services.TeamSkills = knowledgeSkillStore{rows: map[string]*store.Skill{"same": row("same", "team")}}
	f.services.Skills = knowledgeSkillStore{rows: map[string]*store.Skill{"same": row("same", "org"), "org": row("org", "org")}}
	f.services.PlatformSkills = knowledgeSkillStore{rows: map[string]*store.Skill{"platform": row("platform", "platform")}}
	ts, e := BotKnowledgeTools(f.req, knowledgeScope(f), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"same", "org", "platform"} {
		got, e := ts[0].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"name": n})
		if e != nil {
			t.Fatal(e)
		}
		want := n
		if n == "same" {
			want = "team"
		}
		if got["content"] != want {
			t.Fatalf("%s: %+v", n, got)
		}
	}
	got, e := ts[0].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"name": "same", "path": "scripts", "filename": "run.sh"})
	if e != nil || got["content"] != "same/scripts/run.sh" {
		t.Fatalf("file=%v %v", got, e)
	}
	got, e = ts[0].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"name": "same", "file": "../secret"})
	if e != nil || got["error"] == "" {
		t.Fatalf("traversal=%v %v", got, e)
	}
	for _, name := range []string{"unknown-host-skill", "generative-ui"} {
		got, e = ts[0].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"name": name})
		if e != nil || got["content"] != "" || got["error"] != "skill not found in Bot scope" {
			t.Fatalf("unscoped=%v %v", got, e)
		}
	}
	f.services.TeamSkills = knowledgeSkillStore{err: errors.New("private-database-secret")}
	ts, e = BotKnowledgeTools(f.req, knowledgeScope(f), nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ts[0].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"name": "org"})
	if !errors.Is(e, store.ErrBotForbidden) {
		t.Fatal(e)
	}
}

type knowledgeModel struct {
	text  string
	err   error
	calls int
	after func()
}

func (m *knowledgeModel) Name() string { return "sonar" }
func (m *knowledgeModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(y func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.after != nil {
			m.after()
		}
		y(&model.LLMResponse{Content: genai.NewContentFromText(m.text, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 5}}, m.err)
	}
}
func knowledgeSearchSettings(f *botFactoryFixture) {
	f.services.PlatformSettings = botKnowledgePlatformSettings{row: &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search", Model: "sonar"}, Providers: map[string]store.ProviderConfig{"search": {"api_key": "platform-secret"}}}}
	f.services.OrgSettings = botKnowledgeOrgSettings{row: &store.OrgSettings{Providers: map[string]store.ProviderConfig{"search": {"api_key": "org-secret"}}}}
	f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{Providers: map[string]store.ProviderConfig{"search": {"api_key": "team-secret"}}}}
}
func TestBotKnowledgeSearchScopedMeteredRedacted(t *testing.T) {
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	budget, e := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	f.req.Budget = budget
	m := &knowledgeModel{text: "team-secret Sources: https://example.com/current"}
	factory := func(c context.Context, p, n string, cfg *config.AppConfig) (model.LLM, error) {
		if !provider.IsConfinedProviderSecrets(c) || p != "search" || n != "sonar" || cfg.Providers[p]["api_key"] != "team-secret" {
			t.Fatal("wrong configuration")
		}
		cfg.Providers[p]["api_key"] = "mutated"
		return m, nil
	}
	ts, e := BotKnowledgeTools(f.req, knowledgeScope(f), factory)
	if e != nil {
		t.Fatal(e)
	}
	got, e := ts[1].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"query": "current sources"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "team-secret") || !strings.Contains(string(raw), "https://example.com/current") || m.calls != 1 {
		t.Fatalf("search=%s calls=%d", raw, m.calls)
	}
	if u := budget.Usage(); u.InputTokens != 3 || u.OutputTokens != 5 {
		t.Fatalf("usage=%+v", u)
	}
	m.err = errors.New("provider echoed team-secret")
	_, e = ts[1].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"query": "fail"})
	if e == nil || strings.Contains(e.Error(), "team-secret") {
		t.Fatalf("secret error=%v", e)
	}
	f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{WebSearchTool: "other:search"}}
	ts, e = BotKnowledgeTools(f.req, knowledgeScope(f), factory)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ts[1].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"query": "unsupported"})
	if !errors.Is(e, store.ErrBotUnsupported) || m.calls != 2 {
		t.Fatalf("unknown=%v calls=%d", e, m.calls)
	}
}

type knowledgeFlowStore struct {
	store.FlowStore
	saved, yaml string
	calls       int
}

func (s *knowledgeFlowStore) SaveFlow(_ context.Context, n, y string) error {
	s.saved, s.yaml = n, y
	s.calls++
	return nil
}
func TestBotKnowledgeDistillOwnerScopeAndFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			personal, team := &knowledgeFlowStore{}, &knowledgeFlowStore{}
			f.services.PersonalFlows, f.services.Flows = personal, team
			sess, e := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
			if e != nil {
				t.Fatal(e)
			}
			for _, ev := range []*session.Event{
				{Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Read the status", "user")}},
				{Author: "agent", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "/workspace/status"}}}, {FunctionResponse: &genai.FunctionResponse{Name: "read_file", Response: map[string]any{"content": "healthy"}}}}}}},
			} {
				ev.ID = "event"
				ev.Timestamp = time.Now()
				if e := f.sessions.AppendEvent(f.ctx, sess.Session, ev); e != nil {
					t.Fatal(e)
				}
			}
			m := &knowledgeModel{text: "```yaml\nname: status_flow\ndescription: Read status\nnodes:\n  - name: status\n    type: tool\n    tools_selection: [read_file]\n    args:\n      path: /workspace/status\nflow:\n  - from: START\n    to: status\n  - from: status\n    to: END\n```\nflow_name: status_flow"}
			budget, e := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if e != nil {
				t.Fatal(e)
			}
			f.req.Budget = budget
			metered, e := provider.NewBudgetLLM(m, budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
			if e != nil {
				t.Fatal(e)
			}
			c := knowledgeCtx(f)
			c.base = agent.WithBotAuxiliaryLLM(c.base, metered)
			ts, e := BotKnowledgeTools(f.req, knowledgeScope(f), nil)
			if e != nil {
				t.Fatal(e)
			}
			got, e := ts[2].(botRunnableTool).Run(c, map[string]any{})
			if e != nil {
				t.Fatal(e)
			}
			target, other := personal, team
			if scope == store.BotScopeTeam {
				target, other = team, personal
			}
			if target.calls != 1 || other.calls != 0 || got["flow_name"] == "" || m.calls != 1 || budget.Usage().OutputTokens != 5 {
				t.Fatalf("result=%v target=%+v other=%+v calls=%d", got, target, other, m.calls)
			}
			// The lease is revoked between model generation and the store write.
			m.after = func() { f.req.Run.Lease.Fence++ }
			_, e = ts[2].(botRunnableTool).Run(c, map[string]any{})
			if !errors.Is(e, store.ErrBotConflict) || target.calls != 1 {
				t.Fatalf("stale fence=%v calls=%d", e, target.calls)
			}
		})
	}
}

func TestBotKnowledgeSearchRejectsRevokedRun(t *testing.T) {
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = budget
	m := &knowledgeModel{text: "must not be returned", after: func() { f.req.Run.Lease.Fence++ }}
	ts, err := BotKnowledgeTools(f.req, knowledgeScope(f), func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return m, nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := ts[1].(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"query": "current sources"})
	if !errors.Is(err, store.ErrBotConflict) || m.calls != 1 || len(got) != 0 {
		t.Fatalf("revoked run result=%v err=%v calls=%d", got, err, m.calls)
	}
}

func TestBotKnowledgeFactoryPolicyDenial(t *testing.T) {
	f := newBotFactoryFixture(t)
	knowledgeSearchSettings(f)
	m := &knowledgeModel{}
	original := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		safe, e := original(ctx, r, s)
		if e != nil {
			return nil, e
		}
		safe.Tools, e = BotKnowledgeTools(r, s, func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return m, nil })
		return safe, e
	}
	res, e := f.construct(t)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Cleanup()
	entry := res.Agent.ToolIndex.FirstPartyToolEntry("perplexity_web_search")
	if entry == nil {
		t.Fatal("missing real helper tool")
	}
	_, e = entry.Tool.(botRunnableTool).Run(knowledgeCtx(f), map[string]any{"query": "blocked"})
	if e == nil || m.calls != 0 {
		t.Fatalf("policy=%v calls=%d", e, m.calls)
	}
}

var _ tools.LLMFactory = provider.GetProvider
