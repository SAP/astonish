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

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
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
					configured, err := f.repo.(store.BotConfigurationStore).ConfigureReviewed(f.ctx, store.BotConfigurationRequest{BotID: f.b.ID, ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Explicit lower token cap", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
					if err != nil {
						t.Fatal(err)
					}
					f.b = *configured
					estimates := 0
					f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
						ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) {
							estimates++
							if partial && estimates == 1 {
								return 20, nil
							}
							return 80000, nil
						}},
						ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
							return f.model, nil
						},
						Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
							resources := &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}
							if scheduled {
								read, e := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled receipt"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
									f.pool.calls++
									bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "isolated receipt observed", Source: "fixture://receipt", ObservedAt: time.Now(), ActorID: q.Bot.OwnerID}}})
									return map[string]any{"content": "isolated receipt"}, nil
								})
								if e != nil {
									return nil, e
								}
								resources.Tools = []tool.Tool{read}
								resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: composedWeatherClassifier{}, Guard: journeyGuard{repo: f.repo, req: q}}
							}
							return resources, nil
						},
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
						if e = json.Unmarshal(f.request("POST", f.path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"Explicit activation"}`, f.b.Revision), 200), &f.b); e != nil {
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
						run = f.turn()
						if run.Presentation != store.BotRunPresentationHomeInteractive {
							t.Fatalf("not Home interactive: %+v", run)
						}
					}
					if run.Status != store.RunFailed || run.Report.Failure == nil || run.Report.Failure.Kind != store.BotReportFailureBudget {
						t.Fatalf("not typed failed budget: %+v", run)
					}
					for _, want := range []string{"### Budget limit reached", "run budget: 71,000 tokens", "Estimated next input: 80,000 tokens", "increase this Bot’s token budget"} {
						if !strings.Contains(run.Report.Summary, want) {
							t.Errorf("missing %q: %s", want, run.Report.Summary)
						}
					}
					expectedCalls := 0
					if partial {
						expectedCalls = 1
					}
					if f.pool.calls != expectedCalls || f.model.called != partial {
						t.Fatalf("effects=%d model called=%t", f.pool.calls, f.model.called)
					}
					if partial {
						if run.Usage.InputTokens != 5 || run.Usage.OutputTokens != 3 || run.Usage.ToolCalls != 1 || len(run.Report.Unchanged) != 1 {
							t.Fatalf("partial work/accounting lost: %+v", run)
						}
						if !strings.Contains(run.Report.Summary, "Used or reserved: 8 tokens") {
							t.Fatal(run.Report.Summary)
						}
					} else if run.Usage.InputTokens != 0 || run.Usage.OutputTokens != 0 || run.Usage.ToolCalls != 0 || !strings.Contains(run.Report.Summary, "Used or reserved: 0 tokens") {
						t.Fatalf("first denial charged usage: %+v", run)
					}
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
					if f.pool.calls != expectedCalls || estimates != expectedCalls+1 {
						t.Fatalf("work retried: calls=%d estimates=%d", f.pool.calls, estimates)
					}
					restored, e := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
					if e != nil {
						t.Fatal(e)
					}
					if restored.Status != store.RunFailed || !reflect.DeepEqual(restored.Usage, run.Usage) || restored.Report.Summary != run.Report.Summary || len(restored.Report.Unchanged) != len(run.Report.Unchanged) {
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
						if strings.Count(history, "### Budget limit reached") != 1 || strings.Contains(history, "[Error:") || strings.Contains(history, "event: error") {
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
