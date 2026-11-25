package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
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
	client := &sourceAccessCatalogueClient{response: []byte(`{"content":"mandate read"}`)}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, s)
		if err == nil {
			safe.Catalog.Pool = &sharedBotDispatchPool{client: client}
		}
		return safe, err
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
	for _, name := range []string{"shell_command", "delegate_tasks"} {
		entry := resources.Agent.ToolIndex.GetToolEntry(name)
		if entry == nil {
			t.Fatalf("shared catalog missing %s", name)
		}
		invocation := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
		if _, err := entry.Tool.(botRunnableTool).Run(invocation, map[string]any{}); err == nil {
			t.Fatalf("mandate allowed unauthorized %s", name)
		}
	}
	if client.calls != 0 {
		t.Fatalf("unauthorized node execution: %d", client.calls)
	}
	read := resources.Agent.ToolIndex.GetToolEntry("read_file")
	if read == nil {
		t.Fatal("mandate read missing")
	}
	invocation := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
	if _, err := read.Tool.(botRunnableTool).Run(invocation, map[string]any{"path": "outputs/report.md"}); err != nil || client.calls != 1 {
		t.Fatalf("authorized read failed: calls=%d err=%v", client.calls, err)
	}
	runtime.revoked = true
	if _, err = f.construct(t); err == nil {
		t.Fatal("revocation accepted")
	}
}
