package tools

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// ScopedBotControlService never captures a viewer's stores. Each operation uses
// the authenticated request's tenant and repositories; stores perform live
// membership, ownership, revision and admission checks, as for the HTTP API.
type ScopedBotControlService struct{}

func (ScopedBotControlService) repository(ctx context.Context, scope store.BotScope) (store.BotStore, uuid.UUID, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	tc := store.TenantContextFrom(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || tc == nil || tc.UserID != p.Subject || tc.OrgSlug != p.OrgSlug || tc.TeamSlug != p.TeamSlug {
		return nil, uuid.Nil, store.ErrBotForbidden
	}
	if _, botRun := execution.BotRunIdentityFromContext(ctx); botRun {
		return nil, uuid.Nil, store.ErrBotForbidden
	}
	actor, err := uuid.Parse(p.Subject)
	if err != nil || actor == uuid.Nil {
		return nil, uuid.Nil, store.ErrBotForbidden
	}
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		return nil, uuid.Nil, store.ErrBotInvalid
	}
	repo := store.BotStoreFromContext(ctx, scope)
	if repo == nil {
		return nil, uuid.Nil, store.ErrBotUnsupported
	}
	return repo, actor, nil
}
func (s ScopedBotControlService) ListBots(ctx context.Context, scope store.BotScope, page store.BotPage) ([]store.Bot, error) {
	r, _, err := s.repository(ctx, scope)
	if err != nil {
		return nil, err
	}
	return r.List(ctx, page)
}
func (s ScopedBotControlService) GetBot(ctx context.Context, scope store.BotScope, id store.BotID) (*store.Bot, error) {
	r, _, err := s.repository(ctx, scope)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}
func (s ScopedBotControlService) PauseBot(ctx context.Context, scope store.BotScope, id store.BotID, revision int64, reason string) (*store.Bot, error) {
	r, a, err := s.repository(ctx, scope)
	if err != nil {
		return nil, err
	}
	return r.Pause(ctx, store.BotLifecycleRequest{BotID: id, ActorID: a, ExpectedRevision: revision, Reason: reason})
}
func (s ScopedBotControlService) StopBot(ctx context.Context, scope store.BotScope, id store.BotID, revision int64, reason string) (*store.Bot, error) {
	r, a, err := s.repository(ctx, scope)
	if err != nil {
		return nil, err
	}
	return r.Stop(ctx, store.BotLifecycleRequest{BotID: id, ActorID: a, ExpectedRevision: revision, Reason: reason})
}
func (s ScopedBotControlService) RequestBotRun(ctx context.Context, scope store.BotScope, id store.BotID, version int64, instructions, key string) (*store.BotRun, error) {
	r, _, err := s.repository(ctx, scope)
	if err != nil {
		return nil, err
	}
	return r.RequestRun(ctx, store.BotRunRequest{BotID: id, Version: version, Trigger: store.TriggerManual, Instructions: instructions, IdempotencyKey: key})
}

// BindScopedBotControls installs a stateless service only on ordinary Chat's
// control tools. Code callers receive no tools from GetBotToolsForMode.
func BindScopedBotControls(all []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(all))
	for _, t := range all {
		out = append(out, &scopedBotControlTool{Tool: t})
	}
	return out
}

type scopedBotControlTool struct{ tool.Tool }

func (t *scopedBotControlTool) Declaration() *genai.FunctionDeclaration {
	return t.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
}
func (t *scopedBotControlTool) ProcessRequest(ctx tool.Context, r *model.LLMRequest) error {
	if err := t.Tool.(interface {
		ProcessRequest(tool.Context, *model.LLMRequest) error
	}).ProcessRequest(ctx, r); err != nil {
		return err
	}
	if r != nil && r.Tools != nil {
		r.Tools[t.Name()] = t
	}
	return nil
}

type scopedBotControlContext struct {
	tool.Context
	context context.Context
}

func (c scopedBotControlContext) Value(key any) any { return c.context.Value(key) }
func (t *scopedBotControlTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	bound := scopedBotControlContext{Context: ctx, context: WithBotControlService(ctx, ScopedBotControlService{})}
	return t.Tool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(bound, args)
}
