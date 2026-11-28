package launcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// Real SQLite admission, committed responsibility mutation, factory and ChatRunner.
// The capturing provider proves input composition, not live-model compliance.
func TestBotResponsibilityContinuityProvider(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			owner := f.ctx
			if scope == store.BotScopePersonal {
				f.services.PersonalBots = repo
			} else {
				f.services.Bots = repo
			}
			jobs := f.services.Scheduler
			if scope == store.BotScopePersonal {
				jobs = f.services.PersonalScheduler
			}
			const task = "Report current-day market news with source links."
			const revised = "Report market news with source links. After 4PM UTC, cover the next trading day."
			triggerID := uuid.New()
			current := func() (*store.Bot, *store.BotVersion) {
				t.Helper()
				b, err := repo.Get(owner, f.req.Bot.ID)
				if err != nil {
					t.Fatal(err)
				}
				v, err := repo.GetVersion(owner, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				return b, v
			}
			b, _ := current()
			triggers := repo.(store.BotTriggerStore)
			_, err := triggers.ChangeTrigger(owner, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Fixture owner requested recurring news", Definition: store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/2 * * * *", Timezone: "UTC", Instructions: task}})
			if err != nil {
				t.Fatal(err)
			}
			b, _ = current()
			_, err = triggers.ChangeTrigger(owner, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "Fixture owner enabled news"})
			if err != nil {
				t.Fatal(err)
			}
			_, initialVersion := current()
			initialTrigger := initialVersion.Triggers[0]

			claim := func(run store.BotRun) (context.Context, store.BotLease) {
				t.Helper()
				id := execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
				worker := execution.WithBotRunIdentity(owner, id)
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "continuity-test", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				return worker, lease
			}
			finish := func(ctx context.Context, lease store.BotLease) {
				t.Helper()
				work, err := repo.(store.BotWorkStore).LoadWork(ctx, lease)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Context-only test", Unverified: []string{"Live model behavior not exercised"}}}); err != nil {
					t.Fatal(err)
				}
			}
			occurrence := func(wantCharter string, n int) *store.BotWorkState {
				t.Helper()
				b, v := current()
				tr := v.Triggers[0]
				job := jobs.Get(owner, tr.ScheduleID.String())
				if job == nil {
					t.Fatal("missing schedule")
				}
				// Advance only the fixture clock input, never production cadence/task fields.
				due := time.Now().UTC().Add(-time.Duration(3-n) * time.Minute).Truncate(time.Second)
				job.NextRun = &due
				if err := jobs.Update(owner, job); err != nil {
					t.Fatal(err)
				}
				identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tr.ID, due.Format(time.RFC3339Nano))
				q := store.BotScheduledOccurrence{BotID: b.ID, Version: b.Version, TriggerID: tr.ID, JobID: job.ID, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
				admissions := repo.(store.BotScheduledRunStore)
				admitted, err := admissions.AdmitScheduledRun(owner, q)
				if err != nil {
					t.Fatal(err)
				}
				duplicate, err := admissions.AdmitScheduledRun(owner, q)
				if err != nil || !duplicate.Duplicate || duplicate.Run.ID != admitted.Run.ID {
					t.Fatalf("duplicate=%+v err=%v", duplicate, err)
				}
				input, err := repo.(store.BotRuntimeStore).LoadRunInput(owner, b.ID, admitted.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				worker, lease := claim(input.Run)
				work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
				if err != nil {
					t.Fatal(err)
				}
				if work == nil || work.Contract.Charter != wantCharter || work.Contract.Instructions != task {
					t.Fatalf("wrong pinned contract: %+v", work)
				}
				f.req.Bot, f.req.Version, f.req.Run, f.req.Goals, f.req.Instructions = input.Bot, input.Version, input.Run, input.Goals, input.Instructions
				f.req.Run.Status, f.req.Run.Lease = store.RunRunning, lease
				f.req.Work = work.Clone()
				id, _ := execution.BotRunIdentityFromContext(worker)
				f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: input.Version.Autonomy.Level, AllowedTools: []string{"read_file"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.sessions.Create(owner, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: input.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
				if err != nil {
					t.Fatal(err)
				}
				llm := &botFactoryTestModel{}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) {
					return continuityAssessmentModel{llm}, nil
				}
				factory, err := NewBotChatAgentFactory(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				adapter, err := api.NewBotChatRuntimeAdapter(factory)
				if err != nil {
					t.Fatal(err)
				}
				_, err = adapter.Start(owner, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if len(llm.requests) == 0 {
					t.Fatal("provider not invoked")
				}
				system := providerSystem(llm.requests[0])
				for _, want := range []string{wantCharter, "applicable charter and goal constraints pinned for this occurrence take precedence", "schedule, persona or server default (including UTC) is not evidence", "Do not copy the charter into every trigger"} {
					if !strings.Contains(system, want) {
						t.Errorf("provider missing %q", want)
					}
				}
				found := false
				for _, content := range llm.requests[0].Contents {
					for _, part := range content.Parts {
						found = found || content.Role == "user" && part.Text == task
					}
				}
				if !found {
					t.Fatal("saved task was replaced instead of contextualized")
				}
				finish(worker, lease)
				return work.Clone()
			}
			old := occurrence(initialVersion.Charter, 0)

			// Explicit owner source + reviewed responsibility-only change. No generic
			// trigger rewriting or test-only authorization bypass is used for mutation.
			b, _ = current()
			source, err := repo.RequestRun(owner, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Change your responsibility: " + revised, IdempotencyKey: "responsibility-source"})
			if err != nil {
				t.Fatal(err)
			}
			worker, lease := claim(*source)
			responsibility := revised
			proposals := repo.(store.BotControlProposalStore)
			proposal, err := proposals.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "responsibility", ExpiresAt: time.Now().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Responsibility: &responsibility, Reason: "Fixture owner selected UTC explicitly"}})
			if err != nil {
				t.Fatal(err)
			}
			finish(worker, lease)
			applied, err := proposals.ConfirmControlProposal(owner, store.BotControlProposalDecision{BotID: b.ID, ProposalID: proposal.ID, Digest: proposal.Digest})
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("responsibility commit=%+v err=%v", applied, err)
			}
			_, updated := current()
			if !reflect.DeepEqual(updated.Triggers[0], initialTrigger) {
				t.Fatal("responsibility mutation rewrote trigger")
			}
			next := occurrence(revised, 1)
			if old.Contract.Charter != initialVersion.Charter || next.Contract.Version <= old.Contract.Version {
				t.Fatal("pinned and fresh contracts conflated")
			}
			historical, err := repo.GetVersion(owner, b.ID, old.Contract.Version)
			if err != nil || historical.Charter != old.Contract.Charter {
				t.Fatal("old version changed")
			}
			rows, err := repo.ListRuns(owner, b.ID, store.BotPage{Limit: 20})
			if err != nil || len(rows) != 3 {
				t.Fatalf("unexpected replay/runs=%d err=%v", len(rows), err)
			}
			job := jobs.Get(owner, initialTrigger.ScheduleID.String())
			if job.Schedule.Cron != "*/2 * * * *" || job.Schedule.Timezone != "UTC" || !job.Enabled {
				t.Fatal("cadence changed")
			}
			t.Log("Committed responsibility reached new occurrence provider input; saved task/cadence, old version/work and duplicate identity preserved. Controlled provider only.")
		})
	}
}
