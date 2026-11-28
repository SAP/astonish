package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// HTTP admission + production worker, BudgetLLM, ChatRunner, scoped SQLite
// CommitRun and publication. Only model, tool and isolated Home are controlled.
func TestBotBudgetComposedHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scheduled := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/scheduled=%t/partial=%t", scope, scheduled, partial), func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
					v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
					if err != nil {
						t.Fatal(err)
					}
					limit := int64(71000)
					v.Budget.MaxTokens = &limit
					if scheduled {
						v.Autonomy.AllowedTools = []string{"read_file"}
						// Autonomous runs use the worker's owner identity rather than
						// the interactive HTTP caller; retain the model's identity check.
						f.model.principal.Authentication = execution.AuthMethod("daemon_owner")
						f.model.principal.Surface = execution.Surface("daemon")
					}
					paused, err := f.repo.Pause(f.ctx, store.BotLifecycleRequest{BotID: f.b.ID, ActorID: f.b.OwnerID, ExpectedRevision: f.b.Revision, Reason: "Review lower token cap"})
					if err != nil {
						t.Fatal(err)
					}
					f.b = *paused
					configured, err := f.repo.(store.BotConfigurationStore).ConfigureReviewed(f.ctx, store.BotConfigurationRequest{BotID: f.b.ID, ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Explicit lower token cap", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
					if err != nil {
						t.Fatal(err)
					}
					f.b = *configured
					estimates := 0
					f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: f.workerConfig.Sandbox, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
						ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) {
							estimates++
							// Agent-led work calls the producer directly: allow its tool
							// request, then deny the final-answer request after the read.
							if partial && !f.model.called {
								return 20, nil
							}
							return 80000, nil
						}},
						ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
							return mediateHTTPModel(f.model), nil
						},
						Prepare: f.workerConfig.Prepare,
					})
					if err != nil {
						t.Fatal(err)
					}
					var run *store.BotRun
					if scheduled {
						triggerID := uuid.New()
						saved, e := f.repo.(store.BotTriggerStore).ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: f.b.ID, TriggerID: triggerID, ExpectedRevision: f.b.Revision, Action: store.BotTriggerSave, Reason: "Controlled scheduled budget proof", Definition: store.TriggerSpec{ID: triggerID, Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "Read the controlled receipt"}})
						if e != nil {
							t.Fatal(e)
						}
						f.b = *saved
						if e = json.Unmarshal(f.request("POST", f.path+"/resume", fmt.Sprintf(`{"expected_revision":%d,"reason":"Explicit activation"}`, f.b.Revision), 200), &f.b); e != nil {
							t.Fatal(e)
						}
						saved, e = f.repo.(store.BotTriggerStore).ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: f.b.ID, TriggerID: triggerID, ExpectedRevision: f.b.Revision, Action: store.BotTriggerEnable, Reason: "Explicit enable"})
						if e != nil {
							t.Fatal(e)
						}
						f.b = *saved
						v, e = f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
						if e != nil {
							t.Fatal(e)
						}
						// Make this isolated fixture's job due through its scoped store,
						// as in the scheduler acceptance fixtures; do not wait for cron.
						tenant := store.TenantContextFrom(f.ctx)
						ods, e := f.db.ForOrg(tenant.OrgSlug)
						if e != nil {
							t.Fatal(e)
						}
						jobs := ods.ForTeam(tenant.TeamSlug).ScheduledJobs()
						if scope == store.BotScopePersonal {
							jobs = ods.ForUser(tenant.UserID).ScheduledJobs()
						}
						jobID := v.Triggers[0].ScheduleID.String()
						job := jobs.Get(f.ctx, jobID)
						if job == nil || !job.Enabled || job.NextRun == nil {
							t.Fatalf("missing enabled scheduled occurrence: %+v", job)
						}
						due := time.Now().UTC().Add(-time.Second)
						job.NextRun = &due
						if e = jobs.Update(f.ctx, job); e != nil {
							t.Fatal(e)
						}
						// Admission fences the exact persisted timestamp. Read it back
						// before deriving the scheduler's canonical occurrence identity.
						job = jobs.Get(f.ctx, jobID)
						if job == nil || job.NextRun == nil {
							t.Fatal("due occurrence was not persisted")
						}
						due = *job.NextRun
						identity := fmt.Sprintf("%s/%d/%s/%s", f.b.ID, f.b.Version, triggerID, due.UTC().Format(time.RFC3339Nano))
						admission, e := f.repo.(store.BotScheduledRunStore).AdmitScheduledRun(f.ctx, store.BotScheduledOccurrence{JobID: jobID, BotID: f.b.ID, Version: f.b.Version, TriggerID: triggerID, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))})
						if e != nil {
							t.Fatal(e)
						}
						if e = f.worker.Poll(context.Background()); e != nil {
							t.Fatal(e)
						}
						run, e = f.repo.GetRun(f.ctx, f.b.ID, admission.Run.ID)
						if e != nil {
							t.Fatal(e)
						}
						if run.IsInteractiveChat() {
							t.Fatal("scheduled run became interactive")
						}
					} else {
						if err = json.Unmarshal(f.request("POST", f.path+"/resume", fmt.Sprintf(`{"expected_revision":%d,"reason":"Resume reviewed budget"}`, f.b.Revision), 200), &f.b); err != nil {
							t.Fatal(err)
						}
						run = f.turn()
						if run.Presentation != store.BotRunPresentationHomeInteractive {
							t.Fatalf("not Home interactive: %+v", run)
						}
					}
					// The terminal report must project the exact persisted work while
					// Employee and private Chat share one durable accounting ledger.
					if run.Work == nil || run.Report.Work == nil || !reflect.DeepEqual(run.Report.Work, run.Work) {
						t.Fatalf("budget stop projected stale work: work=%+v report=%+v", run.Work, run.Report.Work)
					}
					// Employee uses the shared accounting ledger rather than reinstating
					// historic per-run caps. Both estimates must still be charged.
					if run.Status != store.RunSucceeded || run.Report.Failure != nil {
						t.Fatalf("accounting-only run failed: %+v", run)
					}
					expectedCalls := 1
					if f.pool.calls != expectedCalls || !f.model.called {
						t.Fatalf("effects=%d model called=%t", f.pool.calls, f.model.called)
					}
					if run.Usage.InputTokens != 20 || run.Usage.OutputTokens != 12 || run.Usage.ToolCalls != 1 || len(run.Report.Unchanged) != 2 || estimates == 0 {
						t.Fatalf("Employee/private Chat accounting or evidence lost: %+v estimates=%d", run, estimates)
					}
					estimatesBeforeReconcile := estimates
					publisher := &bot.Publisher{Store: f.repo.(store.BotPublicationStore)}
					for range 2 {
						if _, err = publisher.Publish(f.ctx, f.b.ID, run.ID); err != nil {
							t.Fatal(err)
						}
					}
					// Reconciliation and duplicate publication must not replay tools or model.
					if err = f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					if f.pool.calls != expectedCalls || estimates != estimatesBeforeReconcile {
						t.Fatalf("work retried: calls=%d estimates=%d", f.pool.calls, estimates)
					}
					restored, e := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
					if e != nil {
						t.Fatal(e)
					}
					if restored.Status != run.Status || !reflect.DeepEqual(restored.Usage, run.Usage) || restored.Report.Summary != run.Report.Summary || len(restored.Report.Unchanged) != len(run.Report.Unchanged) {
						t.Fatalf("restore lost evidence: %+v", restored)
					}
					after, e := f.repo.Get(f.ctx, f.b.ID)
					if e != nil {
						t.Fatal(e)
					}
					if after.Status != f.b.Status {
						t.Fatal("budget failure changed bot lifecycle")
					}
					v, e = f.repo.GetVersion(f.ctx, f.b.ID, run.Version)
					if e != nil {
						t.Fatal(e)
					}
					if v.Budget.MaxTokens == nil || *v.Budget.MaxTokens != limit {
						t.Fatal("configured cap replaced")
					}
					// Read actual Home SSE twice (fresh cursor/replay); neither raw transport
					// errors nor synthetic [Error:] transcript rows may accompany the report.
					for range 2 {
						history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
						if run.Report.EmployeeMessage == nil || strings.Count(history, run.Report.EmployeeMessage.Text) != 1 || strings.Contains(history, "[Error:") || strings.Contains(history, "event: error") {
							t.Fatalf("duplicate/raw Home failure: %s", history)
						}
						if !strings.Contains(history, "bot-report:"+run.ID.String()) {
							t.Fatalf("missing canonical report identity: %s", history)
						}
					}
					events := string(f.request("GET", f.path+"/sessions/"+run.SessionID.String()+"/stream", "", 200))
					if strings.Contains(events, `"type":"error"`) || strings.Contains(events, "[Error:") {
						t.Fatalf("native raw error duplicated: %s", events)
					}
					t.Logf("%s scheduled=%t partial=%t: one canonical Home report, no raw errors, explicit 71,000 cap; usage=%+v", scope, scheduled, partial, run.Usage)
				})
			}
		}
	}
}
