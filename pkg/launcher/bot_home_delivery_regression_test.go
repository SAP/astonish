package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// The public Employee captures controls natively; only private Chat executes
// schedule_job. Neither route asks for a transport outside the Bot's home.
func TestInteractiveBotScheduleUsesHomeAdapter(t *testing.T) {
	f := newBotFactoryFixture(t)
	setBotSharedInteractive(f)
	proposals := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.PersonalBots = proposals
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if got.Agent.ToolIndex.GetToolEntry("schedule_job") != nil || got.Agent.ToolIndex.GetToolEntry("execute_tool") != nil {
		t.Fatal("public Employee exposed private schedule execution")
	}
	entry := got.Agent.ToolIndex.GetToolEntry(botProposalToolName)
	if entry == nil {
		t.Fatal("native control capture missing")
	}
	req := &model.LLMRequest{}
	if err := entry.Tool.(botRunnableTool).ProcessRequest(botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String()}, req); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(raw); strings.Contains(text, "ALWAYS ask") || strings.Contains(text, "Ask the user which channel") || !strings.Contains(text, "Schedule capture stays private") {
		t.Fatalf("unsafe public schedule contract: %s", text)
	}
	worker, err := f.constructWorker(t)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Cleanup()
	schedule := worker.Agent.ToolIndex.GetToolEntry("schedule_job")
	if schedule == nil || !strings.Contains(schedule.Tool.Description(), "home chat") {
		t.Fatal("private schedule adapter lost home destination")
	}
}

type homeScheduleProposalRepo struct {
	scheduleTestRepo
	store.BotControlProposalStore
}

func TestInteractiveBostonScheduleReviewedHomeOwnership(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	current, err := repo.Get(f.ctx, f.req.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	version, err := repo.GetVersion(f.ctx, current.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Bot, f.req.Version = *current, *version
	source, err := repo.RequestRun(f.ctx, store.BotRunRequest{BotID: current.ID, Version: current.Version, Trigger: store.TriggerManual, Instructions: "Report current Boston weather here every minute.", IdempotencyKey: "boston-owner-request"})
	if err != nil {
		t.Fatal(err)
	}
	identity := f.req.Policy.Identity()
	identity.BotID, identity.RunID, identity.Version = current.ID, source.ID, current.Version
	ownerRun := execution.WithBotRunIdentity(f.ctx, identity)
	lease, err := repo.ClaimRun(ownerRun, store.BotClaimRequest{BotID: current.ID, RunID: source.ID, WorkerID: "boston-capture", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Run = *source
	if _, err := f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: current.OwnerID.String(), SessionID: source.SessionID.String()}); err != nil {
		t.Fatal(err)
	}
	f.req.Run.Status, f.req.Run.Lease = store.RunRunning, lease
	f.req.Instructions = "Report current Boston weather here every minute."
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: []string{"schedule_job"}})
	if err != nil {
		t.Fatal(err)
	}
	setBotSharedInteractive(f)
	f.services.PersonalBots = homeScheduleProposalRepo{scheduleTestRepo: scheduleTestRepo{BotStore: repo, BotTriggerStore: repo.(store.BotTriggerStore), f: f}, BotControlProposalStore: repo.(store.BotControlProposalStore)}
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	capture := r.Agent.ToolIndex.GetToolEntry(botProposalToolName)
	if capture == nil {
		t.Fatal("native schedule capture missing")
	}
	trigger := store.TriggerSpec{ID: uuid.New(), Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "Report current Boston weather in the same format as the preceding weather answer.", Enabled: false}
	payload := store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Owner requested Boston weather here every minute", Schedule: &store.BotControlSchedule{ExpectedRevision: current.Revision, Trigger: trigger}}
	c := botFactoryHandoffContext{base: f.ctx, session: f.req.Run.SessionID.String(), functionCallID: "boston-schedule"}
	out, err := capture.Tool.(botRunnableTool).Run(c, botProposalArgs{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != store.BotControlProposed || out["proposal_id"] == nil || out["operation_id"] != nil {
		t.Fatalf("capture claimed applied: %v", out)
	}
	pid, err := uuid.Parse(out["proposal_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	savedProposal, err := repo.(store.BotControlProposalStore).ReadControlProposal(f.ctx, current.ID, pid)
	if err != nil {
		t.Fatal(err)
	}
	if savedProposal.Payload.Schedule.Trigger != trigger || savedProposal.SourceRunID != source.ID || savedProposal.SourceUserID != current.OwnerID || savedProposal.Digest != out["digest"] {
		t.Fatalf("capture lost exact owner request: %+v", savedProposal)
	}
	before, err := repo.GetVersion(f.ctx, current.ID, current.Version)
	if err != nil || len(before.Triggers) != 0 {
		t.Fatalf("capture mutated schedules: %+v %v", before, err)
	}
	decision := store.BotControlProposalDecision{BotID: current.ID, ProposalID: pid, Digest: savedProposal.Digest}
	if _, err := repo.(store.BotControlProposalStore).ConfirmControlProposal(f.ctx, decision); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("review applied during source run: %v", err)
	}
	state, err := repo.(store.BotWorkStore).LoadWork(ownerRun, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitRun(ownerRun, store.BotRunCommit{Lease: lease, Status: store.RunCancelled, Report: store.BotReport{Summary: "Captured for separate owner review.", Work: store.BuildBotWorkProjection(state)}}); err != nil {
		t.Fatal(err)
	}
	applied, err := repo.(store.BotControlProposalStore).ConfirmControlProposal(f.ctx, decision)
	if err != nil || applied.Status != store.BotControlApplied {
		t.Fatalf("owner review failed: %+v %v", applied, err)
	}
	saved, err := repo.Get(f.ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.GetVersion(f.ctx, saved.ID, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Triggers) != 1 || v.Triggers[0].Enabled || v.Triggers[0].ScheduleID == nil || v.Triggers[0].ChannelBinding != nil || len(v.Delivery.Bindings) != 0 || saved.ID != current.ID {
		t.Fatalf("wrong reviewed home target: %+v", v)
	}
	external := 0
	delivery := scheduler.WithoutBotTriggerDelivery(func(context.Context, *scheduler.Job, string, error) error { external++; return nil })
	if err := delivery(f.ctx, &scheduler.Job{Mode: scheduler.ModeBotTrigger}, "Boston weather", nil); err != nil {
		t.Fatal(err)
	}
	if external != 0 {
		t.Fatal("Bot tick reached external transport")
	}
}
