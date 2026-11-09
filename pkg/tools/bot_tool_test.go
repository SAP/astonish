package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

type botControlTestContext struct {
	tool.Context
	base context.Context
}

func (c botControlTestContext) Value(key any) any           { return c.base.Value(key) }
func (c botControlTestContext) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c botControlTestContext) Done() <-chan struct{}       { return c.base.Done() }
func (c botControlTestContext) Err() error                  { return c.base.Err() }

type botReadProbe struct{ calls int }

func (p *botReadProbe) ListBots(context.Context, store.BotScope, store.BotPage) ([]store.Bot, error) {
	p.calls++
	return nil, nil
}
func (p *botReadProbe) GetBot(_ context.Context, _ store.BotScope, id store.BotID) (*store.Bot, error) {
	p.calls++
	return &store.Bot{ID: id, Revision: 7}, nil
}

type botWriteProbe struct {
	botReadProbe
	revision int64
	stopped  bool
	key      string
}

func (p *botWriteProbe) PauseBot(_ context.Context, _ store.BotScope, id store.BotID, r int64, _ string) (*store.Bot, error) {
	p.revision = r
	return &store.Bot{ID: id, Status: store.BotPaused}, nil
}
func (p *botWriteProbe) StopBot(_ context.Context, _ store.BotScope, id store.BotID, r int64, _ string) (*store.Bot, error) {
	p.revision = r
	p.stopped = true
	return &store.Bot{ID: id, Status: store.BotStopped}, nil
}
func (p *botWriteProbe) RequestBotRun(_ context.Context, _ store.BotScope, id store.BotID, v int64, _ string, key string) (*store.BotRun, error) {
	p.key = key
	return &store.BotRun{BotID: id, Version: v, Status: store.RunQueued}, nil
}
func botTestContext(t *testing.T, s BotControlService) botControlTestContext {
	t.Helper()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		ctx = WithBotControlService(ctx, s)
	}
	return botControlTestContext{base: ctx}
}
func TestBotControlsFailClosed(t *testing.T) {
	if _, err := botList(nil, BotListArgs{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	if _, err := botList(botTestContext(t, nil), BotListArgs{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	p := &botReadProbe{}
	ctx := botTestContext(t, p)
	if _, err := botList(ctx, BotListArgs{Scope: "all"}); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal(err)
	}
	if _, err := botList(ctx, BotListArgs{Limit: 201}); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal(err)
	}
	if _, err := botLifecycle(ctx, BotLifecycleArgs{}, true); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	if _, err := botRun(ctx, BotRunArgs{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	unauthed := botControlTestContext{base: WithBotControlService(context.Background(), p)}
	if _, err := botList(unauthed, BotListArgs{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatal("dispatched invalid request")
	}
	result, err := botList(ctx, BotListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := result["bots"].([]store.Bot); !ok || b == nil || len(b) != 0 {
		t.Fatal("empty list not explicit")
	}
}
func TestBotControlsTypedDispatch(t *testing.T) {
	p := &botWriteProbe{}
	ctx := botTestContext(t, p)
	id := uuid.NewString()
	if _, err := botGet(ctx, BotGetArgs{BotID: id}); err != nil {
		t.Fatal(err)
	}
	args := BotLifecycleArgs{BotID: id, ExpectedRevision: 7, Reason: "user requested"}
	if _, err := botLifecycle(ctx, args, true); err != nil {
		t.Fatal(err)
	}
	if _, err := botLifecycle(ctx, args, false); err != nil {
		t.Fatal(err)
	}
	if p.revision != 7 || !p.stopped {
		t.Fatal("revision lost")
	}
	result, err := botRun(ctx, BotRunArgs{BotID: id, Version: 3, Instructions: "check", IdempotencyKey: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if p.key != "stable" || result["run"].(*store.BotRun).Status != store.RunQueued {
		t.Fatal("not queued")
	}
}
func TestBotToolCatalogCodeAndDeclarations(t *testing.T) {
	disabled, err := GetBotToolsForMode(true)
	if err != nil || len(disabled) != 0 {
		t.Fatal("Bot tools present in code")
	}
	enabled, err := GetBotToolsForMode(false)
	if err != nil {
		t.Fatal(err)
	}
	declarations := BotToolDeclarations()
	if len(enabled) != len(declarations) {
		t.Fatal("declaration mismatch")
	}
	for i, tool := range enabled {
		if tool.Name() != declarations[i].Name {
			t.Fatal("name mismatch")
		}
		d := tool.(interface {
			Declaration() *genai.FunctionDeclaration
		}).Declaration()
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"actor_id", "owner_id", "tenant_id", "auto_approve", "run_policy"} {
			if strings.Contains(string(raw), bad) {
				t.Fatalf("public authority field %s", bad)
			}
		}
	}
	for _, d := range GetAllFlowToolDeclarations() {
		if d.Category == BotToolGroup {
			t.Fatal("Bot controls exposed to flows")
		}
	}
}
