package tools

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"testing"
)

func (botControlTestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

type scopedControlRepo struct {
	store.BotStore
	calls   int
	actor   uuid.UUID
	request store.BotRunRequest
}

func (r *scopedControlRepo) List(context.Context, store.BotPage) ([]store.Bot, error) {
	r.calls++
	return []store.Bot{{Name: "scoped"}}, nil
}
func (r *scopedControlRepo) Get(_ context.Context, id store.BotID) (*store.Bot, error) {
	r.calls++
	return &store.Bot{ID: id, Revision: 3}, nil
}
func (r *scopedControlRepo) Pause(_ context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	r.calls++
	r.actor = q.ActorID
	return &store.Bot{ID: q.BotID, Status: store.BotPaused}, nil
}
func (r *scopedControlRepo) Stop(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.Pause(ctx, q)
}
func (r *scopedControlRepo) RequestRun(_ context.Context, q store.BotRunRequest) (*store.BotRun, error) {
	r.calls++
	r.request = q
	return &store.BotRun{BotID: q.BotID, Status: store.RunQueued}, nil
}
func TestBotScopedControlsInstalledDispatch(t *testing.T) {
	tc := botTestContext(t, nil)
	p, _ := execution.PrincipalFromContext(tc.base)
	personal, team := &scopedControlRepo{}, &scopedControlRepo{}
	tc.base = store.WithTenantContext(tc.base, &store.TenantContext{UserID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
	tc.base = store.WithServices(tc.base, &store.Services{PersonalBots: personal, Bots: team})
	all, err := GetBotToolsForMode(false)
	if err != nil {
		t.Fatal(err)
	}
	all = BindScopedBotControls(all)
	args := []map[string]any{{}, {"scope": "team", "bot_id": uuid.NewString()}, {"bot_id": uuid.NewString(), "expected_revision": 3, "reason": "requested"}, {"bot_id": uuid.NewString(), "expected_revision": 3, "reason": "requested"}, {"bot_id": uuid.NewString(), "version": 1, "instructions": "check", "idempotency_key": "stable"}}
	for i, target := range all {
		req := &model.LLMRequest{}
		if err := target.(interface {
			ProcessRequest(tool.Context, *model.LLMRequest) error
		}).ProcessRequest(tc, req); err != nil {
			t.Fatal(err)
		}
		if req.Tools[target.Name()] != target {
			t.Fatal("unbound dispatcher registered")
		}
		if _, err := target.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}).Run(tc, args[i]); err != nil {
			t.Fatalf("%s: %v", target.Name(), err)
		}
	}
	if personal.calls != 4 || team.calls != 1 || personal.actor.String() != p.Subject || personal.request.Trigger != store.TriggerManual || personal.request.IdempotencyKey != "stable" {
		t.Fatal("scope/actor/admission lost")
	}
	tc.base = store.WithTenantContext(tc.base, &store.TenantContext{UserID: "viewer", OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
	if _, err := (ScopedBotControlService{}).ListBots(tc, store.BotScopePersonal, store.BotPage{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if personal.calls != 4 {
		t.Fatal("mismatched viewer dispatched")
	}
}
func TestBotIsolatedInternalToolsHaveRealSchemas(t *testing.T) {
	all, err := GetIsolatedInternalTools()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, target := range all {
		found[target.Name()] = true
	}
	for _, name := range []string{"shell_command", "read_file", "web_fetch", "repo_map", "filter_json"} {
		if !found[name] {
			t.Fatal(name)
		}
	}
}
