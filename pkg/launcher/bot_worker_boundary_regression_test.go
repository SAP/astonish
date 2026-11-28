package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

func TestBotPrivateNestedDispatchConversationBoundary(t *testing.T) {
	f := newBotFactoryFixture(t)
	resources, invocation := basicFactory(t, f, "read_file")
	binding := botFactoryModelForTest(t, resources.Agent.LLM).binding
	entry := resources.Agent.ToolIndex.GetToolEntry("read_file").Tool.(*chatRuntimeTool)
	calls := 0
	dispatch := botSharedDispatch{binding}
	boundInvocation := botBoundToolContext{Context: invocation, owner: binding.bind(invocation)}
	_, err := dispatch.Dispatch(boundInvocation, entry.chatRuntimeRunnable, map[string]any{"path": "outputs/test"}, func(c tool.Context, a any) (map[string]any, error) {
		if c.SessionID() != f.req.Run.SessionID.String() {
			t.Fatal("physical execution session lost")
		}
		// Scoped engines rebind before their nested mirror/verifier dispatch.
		owner, err := (botSharedBinding{binding}).Bind(c)
		if err != nil {
			return nil, err
		}
		nested := botBoundToolContext{Context: c, owner: owner}
		return dispatch.Dispatch(nested, entry.chatRuntimeRunnable, a, func(tool.Context, any) (map[string]any, error) {
			calls++
			return map[string]any{"content": "nested"}, nil
		})
	})
	if err != nil || calls != 1 {
		t.Fatalf("nested dispatch: calls=%d err=%v", calls, err)
	}
	for _, sid := range []string{f.req.Run.SessionID.String(), uuid.NewString()} {
		wrong := invocation
		wrong.session = sid
		_, err := dispatch.Dispatch(wrong, entry.chatRuntimeRunnable, map[string]any{}, func(tool.Context, any) (map[string]any, error) { calls++; return nil, nil })
		if !errors.Is(err, store.ErrBotForbidden) || calls != 1 {
			t.Fatalf("wrong conversation accepted: %s calls=%d err=%v", sid, calls, err)
		}
	}
	foreign := *binding
	marked := botWorkerExecutionContext{Context: invocation, sessionID: f.req.Run.SessionID.String(), conversation: botWorkerConversation{binding: &foreign, id: binding.conversationID}}
	_, err = dispatch.Dispatch(marked, entry.chatRuntimeRunnable, map[string]any{}, func(tool.Context, any) (map[string]any, error) { calls++; return nil, nil })
	if !errors.Is(err, store.ErrBotForbidden) || calls != 1 {
		t.Fatalf("foreign binding accepted: calls=%d err=%v", calls, err)
	}
}

func TestBotPausedNativeScheduleInspectionBoundary(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			f, runtime, _ := discoveryMandateFixture(t)
			f.req.Bot.Scope = scope
			f.req.Bot.Status = store.BotPaused
			f.req.Bot.Revision = 1
			f.req.Run.Intent = store.BotIntentConversation
			runtime.saved.Purpose = execution.BotMandatePurposeOperation
			runtime.saved.Constraints[1].Resources = []string{"list_scheduled_jobs"}
			auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, execution.BotMandatePurposeOperation)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate.Authorization = auth
			f.req.Principal = auth.Principal(execution.SurfaceStudio)
			f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
			tr, jobID := uuid.New(), uuid.New()
			f.req.Version.Triggers = []store.TriggerSpec{{ID: tr, Kind: store.TriggerCron, ScheduleID: &jobID, Cron: "*/2 * * * *", Timezone: "UTC", Instructions: "weather"}}
			jobs := &schedulerDispatchJobs{job: &store.ScheduledJob{ID: jobID.String(), OwnerID: f.req.Bot.OwnerID.String(), Name: "weather", Payload: store.JobPayload{BotID: f.req.Bot.ID.String(), BotTriggerID: tr.String(), BotVersion: f.req.Version.Version}}}
			repo := schedulerDispatchRepo{scheduleListRepo: scheduleListRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, version: &f.req.Version}, sessions: f.sessions}
			f.services.Bots, f.services.PersonalBots = repo, repo
			f.services.Scheduler, f.services.PersonalScheduler = jobs, jobs
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			var reader botRunnableTool
			for _, candidate := range resources.Agent.Tools {
				if candidate.Name() == "employee_chat" {
					ctx := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}}
					// A paused operation-scope conversation carries no scoped Bot
					// execution authority, so employee_chat is denied outright.
					if _, err := candidate.(botRunnableTool).Run(ctx, map[string]any{"message": "execute something"}); err == nil {
						t.Fatalf("paused delegation accepted: %v", err)
					}
				}
				if candidate.Name() == "list_scheduled_jobs" {
					reader = candidate.(botRunnableTool)
				}
				switch candidate.Name() {
				case "execute_tool", "shell_command", "browser_navigate", "schedule_job", "update_scheduled_job":
					t.Fatalf("paused Employee exposes external tool %s", candidate.Name())
				}
			}
			if reader == nil {
				t.Fatal("paused native schedule reader missing")
			}
			invocation := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}}
			out, err := reader.Run(invocation, map[string]any{"filter": "weather"})
			if err != nil || out["count"] != float64(1) || jobs.calls != 1 {
				t.Fatalf("inspection: %v err=%v reads=%d", out, err, jobs.calls)
			}
			wrong := invocation
			wrong.session = uuid.NewString()
			if _, err := reader.Run(wrong, map[string]any{}); !errors.Is(err, store.ErrBotForbidden) || jobs.calls != 1 {
				t.Fatalf("wrong session read: %v reads=%d", err, jobs.calls)
			}
			jobs.job.OwnerID = uuid.NewString()
			if _, err := reader.Run(invocation, map[string]any{}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("foreign schedule read: %v", err)
			}
			runtime.revoked = true
			before := jobs.calls
			if _, err := reader.Run(invocation, map[string]any{}); err == nil || jobs.calls != before {
				t.Fatalf("revoked mandate read: %v reads=%d", err, jobs.calls)
			}
		})
	}
}
