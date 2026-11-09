package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"
	"time"
)

type factoryMandateRuntime struct {
	store.BotMandateRuntime
	saved   execution.BotMandate
	revoked bool
}

func (r *factoryMandateRuntime) VerifyBotMandate(context.Context, execution.BotMandateRef, execution.BotMandatePurpose) (execution.BotMandate, error) {
	if r.revoked {
		return execution.BotMandate{}, store.ErrBotForbidden
	}
	return r.saved, nil
}
func TestBotChatFactoryMandateWithoutTurnRegistry(t *testing.T) {
	f := newBotFactoryFixture(t)
	id := f.req.Policy.Identity()
	runtime := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{"read_file"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
	auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Principal = auth.Principal(execution.SurfaceStudio)
	f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: id}
	f.req.Run.MandateRef = &runtime.saved.Ref
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, s)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"read_file", "shell_command"} {
			native, e := functiontool.New(functiontool.Config{Name: name, Description: "test"}, func(tool.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
			if e != nil {
				return nil, e
			}
			safe.Tools = append(safe.Tools, native)
		}
		return safe, nil
	}
	missing := f.req.Mandate
	f.req.Mandate = nil
	if _, err = f.construct(t); err == nil {
		t.Fatal("persisted mandate fell back to interactive authority")
	}
	f.req.Mandate = missing
	f.req.Interactive = true
	if _, err = f.construct(t); err == nil {
		t.Fatal("mandate entered interactive execution")
	}
	f.req.Interactive = false
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	found := false
	for _, native := range resources.Agent.Tools {
		if native.Name() == "shell_command" || native.Name() == "delegate_tasks" {
			t.Fatalf("unscoped catalogue: %s", native.Name())
		}
		if native.Name() == "read_file" {
			found = true
		}
	}
	if !found {
		t.Fatal("mandate read missing")
	}
	runtime.revoked = true
	if _, err = f.construct(t); err == nil {
		t.Fatal("revocation accepted")
	}
}
