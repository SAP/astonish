package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const BotToolGroup = "bots"

// BotControlService is an optional trusted control-plane adapter. Implementations
// resolve scope, owner and authorization from ctx, recheck current membership and
// revisions, and use durable service operations. Never inject an unscoped store.
// Worker execution, grants, credentials and actor IDs are deliberately absent.
type BotControlService interface {
	ListBots(context.Context, store.BotScope, store.BotPage) ([]store.Bot, error)
	GetBot(context.Context, store.BotScope, store.BotID) (*store.Bot, error)
}

// Mutations are optional: read-only integrations must not fake successful writes.
type BotLifecycleControl interface {
	PauseBot(context.Context, store.BotScope, store.BotID, int64, string) (*store.Bot, error)
	StopBot(context.Context, store.BotScope, store.BotID, int64, string) (*store.Bot, error)
}
type BotRunControl interface {
	// RequestBotRun admits/enqueues work; it does not prove execution succeeded.
	RequestBotRun(context.Context, store.BotScope, store.BotID, int64, string, string) (*store.BotRun, error)
}

type botControlKey struct{}

func WithBotControlService(ctx context.Context, service BotControlService) context.Context {
	return context.WithValue(ctx, botControlKey{}, service)
}
func botControlFromContext(ctx context.Context) (BotControlService, error) {
	if ctx == nil {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	service, _ := ctx.Value(botControlKey{}).(BotControlService)
	if service == nil {
		return nil, fmt.Errorf("%w: Bot control service is not configured", store.ErrBotUnsupported)
	}
	return service, nil
}

type BotListArgs struct {
	Scope string `json:"scope,omitempty" jsonschema:"personal (default) or team; does not grant access"`
	Limit int    `json:"limit,omitempty" jsonschema:"Page size, 1 to 200 (default 50)"`
	After string `json:"after,omitempty" jsonschema:"Cursor from the previous page"`
}
type BotGetArgs struct {
	Scope string `json:"scope,omitempty" jsonschema:"personal (default) or team"`
	BotID string `json:"bot_id" jsonschema:"Bot UUID"`
}
type BotLifecycleArgs struct {
	Scope            string `json:"scope,omitempty" jsonschema:"personal (default) or team"`
	BotID            string `json:"bot_id" jsonschema:"Bot UUID"`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Exact current revision from get_bot; reload on conflict"`
	Reason           string `json:"reason" jsonschema:"User-requested reason for lifecycle change"`
}
type BotRunArgs struct {
	Scope          string `json:"scope,omitempty" jsonschema:"personal (default) or team"`
	BotID          string `json:"bot_id" jsonschema:"Bot UUID"`
	Version        int64  `json:"version" jsonschema:"Exact immutable Bot version to enqueue"`
	Instructions   string `json:"instructions" jsonschema:"Instructions for this run"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable key for this request; reuse on retry, never silently enqueue duplicate work"`
}

func botToolScope(raw string) (store.BotScope, error) {
	if raw == "" {
		return store.BotScopePersonal, nil
	}
	scope := store.BotScope(raw)
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		return "", store.ErrBotInvalid
	}
	return scope, nil
}
func botToolTarget(scope, id string) (store.BotScope, store.BotID, error) {
	s, err := botToolScope(scope)
	if err != nil {
		return "", uuid.Nil, err
	}
	b, err := uuid.Parse(id)
	if err != nil || b == uuid.Nil {
		return "", uuid.Nil, store.ErrBotInvalid
	}
	return s, b, nil
}
func botList(ctx tool.Context, args BotListArgs) (map[string]any, error) {
	svc, err := botControlFromContext(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := botToolScope(args.Scope)
	if err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Limit > 200 {
		return nil, store.ErrBotInvalid
	}
	if args.After != "" {
		if _, err := uuid.Parse(args.After); err != nil {
			return nil, store.ErrBotInvalid
		}
	}
	bots, err := svc.ListBots(ctx, scope, store.BotPage{Limit: args.Limit, After: args.After})
	if err != nil {
		return nil, err
	}
	if bots == nil {
		bots = []store.Bot{}
	}
	return map[string]any{"bots": bots}, nil
}
func botGet(ctx tool.Context, args BotGetArgs) (map[string]any, error) {
	svc, err := botControlFromContext(ctx)
	if err != nil {
		return nil, err
	}
	scope, id, err := botToolTarget(args.Scope, args.BotID)
	if err != nil {
		return nil, err
	}
	b, err := svc.GetBot(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, store.ErrBotNotFound
	}
	return map[string]any{"bot": b}, nil
}
func botLifecycle(ctx tool.Context, args BotLifecycleArgs, pause bool) (map[string]any, error) {
	svc, err := botControlFromContext(ctx)
	if err != nil {
		return nil, err
	}
	control, ok := svc.(BotLifecycleControl)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	scope, id, err := botToolTarget(args.Scope, args.BotID)
	if err != nil {
		return nil, err
	}
	if args.ExpectedRevision <= 0 || strings.TrimSpace(args.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	var b *store.Bot
	if pause {
		b, err = control.PauseBot(ctx, scope, id, args.ExpectedRevision, args.Reason)
	} else {
		b, err = control.StopBot(ctx, scope, id, args.ExpectedRevision, args.Reason)
	}
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, store.ErrBotUnsupported
	}
	return map[string]any{"bot": b}, nil
}
func botRun(ctx tool.Context, args BotRunArgs) (map[string]any, error) {
	svc, err := botControlFromContext(ctx)
	if err != nil {
		return nil, err
	}
	control, ok := svc.(BotRunControl)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	scope, id, err := botToolTarget(args.Scope, args.BotID)
	if err != nil {
		return nil, err
	}
	if args.Version <= 0 || strings.TrimSpace(args.Instructions) == "" || strings.TrimSpace(args.IdempotencyKey) == "" {
		return nil, store.ErrBotInvalid
	}
	run, err := control.RequestBotRun(ctx, scope, id, args.Version, args.Instructions, args.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, store.ErrBotUnsupported
	}
	return map[string]any{"run": run, "message": "Run request recorded; inspect the run status, not an execution-success claim."}, nil
}

// GetBotToolsForMode keeps Bot controls out of the Code tool catalog entirely.
func GetBotToolsForMode(codeMode bool) ([]tool.Tool, error) {
	if codeMode {
		return nil, nil
	}
	return GetBotTools()
}
func GetBotTools() ([]tool.Tool, error) {
	list, err := functiontool.New(functiontool.Config{Name: "list_bots", Description: "List authorized Bots. An empty list is not permission to create or activate one."}, botList)
	if err != nil {
		return nil, err
	}
	get, err := functiontool.New(functiontool.Config{Name: "get_bot", Description: "Read current Bot status and revision before a lifecycle change."}, botGet)
	if err != nil {
		return nil, err
	}
	pause, err := functiontool.New(functiontool.Config{Name: "pause_bot", Description: "Pause a Bot through the trusted service with an exact revision. Does not undo committed effects."}, func(ctx tool.Context, args BotLifecycleArgs) (map[string]any, error) {
		return botLifecycle(ctx, args, true)
	})
	if err != nil {
		return nil, err
	}
	stop, err := functiontool.New(functiontool.Config{Name: "stop_bot", Description: "Stop a Bot through the trusted service with an exact revision. Does not undo committed effects."}, func(ctx tool.Context, args BotLifecycleArgs) (map[string]any, error) {
		return botLifecycle(ctx, args, false)
	})
	if err != nil {
		return nil, err
	}
	run, err := functiontool.New(functiontool.Config{Name: "run_bot", Description: "Request a manual Bot run. Records a durable request, not completion; requires a stable idempotency key."}, botRun)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, get, pause, stop, run}, nil
}
