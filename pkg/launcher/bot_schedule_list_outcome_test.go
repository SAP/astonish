package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

type scheduleListRepo struct {
	botFactoryTestRepo
	version *store.BotVersion
	err     error
}

func (r scheduleListRepo) GetVersion(context.Context, store.BotID, int64) (*store.BotVersion, error) {
	return r.version, r.err
}

type scheduleListJobs struct {
	store.SchedulerStore
	job *store.ScheduledJob
}

func (s scheduleListJobs) Get(context.Context, string) *store.ScheduledJob { return s.job }

type scheduleListContext struct {
	tool.Context
	call string
}

func (c scheduleListContext) FunctionCallID() string { return c.call }

func TestBotScheduleListReadOutcome(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"populated", "empty", "filtered-empty", "unbound", "read-error", "missing-version", "missing-job", "foreign-owner", "foreign-trigger", "stale-version", "missing-scheduler", "wrong-run", "cancelled"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				f.req.Bot.Revision = 1
				triggerID, jobID := uuid.New(), uuid.New()
				v := f.req.Version
				v.Triggers = []store.TriggerSpec{{ID: triggerID, Kind: store.TriggerCron, ScheduleID: &jobID, Cron: "* * * * *", Timezone: "UTC", Instructions: "Fetch the current weather for Orlando, FL and report."}}
				job := &store.ScheduledJob{ID: jobID.String(), Name: "bot-" + f.req.Bot.ID.String() + "-" + triggerID.String(), OwnerID: f.req.Bot.OwnerID.String(), Enabled: true, Schedule: store.JobSchedule{Cron: "* * * * *", Timezone: "UTC"}, Payload: store.JobPayload{BotID: f.req.Bot.ID.String(), BotTriggerID: triggerID.String(), BotVersion: v.Version}}
				repo := scheduleListRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, version: &v}
				jobs := scheduleListJobs{job: job}
				ctx := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
				ctx, _ = bot.WithToolOutcomes(ctx)
				filter := " ORLANDO  weather "
				wantCount := 1
				wantErr := false
				switch scenario {
				case "empty":
					v.Triggers = nil
					wantCount = 0
					filter = ""
				case "filtered-empty":
					filter = "Orlando missing"
					wantCount = 0
				case "unbound":
					v.Triggers[0].ScheduleID = nil
				case "read-error":
					repo.err = errors.New("read failed")
					wantErr = true
				case "missing-version":
					repo.version = nil
					wantErr = true
				case "missing-job":
					jobs.job = nil
					wantErr = true
				case "foreign-owner":
					job.OwnerID = uuid.NewString()
					wantErr = true
				case "foreign-trigger":
					job.Payload.BotTriggerID = uuid.NewString()
					wantErr = true
				case "stale-version":
					job.Payload.BotVersion++
					wantErr = true
				case "missing-scheduler":
					wantErr = true
				case "wrong-run":
					id := f.req.Policy.Identity()
					id.RunID = uuid.New()
					ctx = execution.WithBotRunIdentity(ctx, id)
					wantErr = true
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					wantErr = true
				}
				s := botScheduleTools{request: f.req, repo: repo, scheduler: jobs}
				if scenario == "missing-scheduler" {
					s.scheduler = nil
				}
				c := scheduleListContext{Context: basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: ctx}}, call: "read/exact-call"}
				out, err := s.list(c, tools.ListScheduledJobsArgs{Filter: filter})
				if (err != nil) != wantErr {
					t.Fatalf("read error %v want %v", err, wantErr)
				}
				if _, ok := bot.TakeToolOutcome(ctx, "other-call"); ok {
					t.Fatal("wrong call consumed read")
				}
				evidence, ok := bot.TakeToolOutcome(ctx, c.FunctionCallID())
				if wantErr {
					if ok {
						t.Fatalf("failed read attested: %+v", evidence)
					}
					return
				}
				if out.Count != wantCount || !ok || !evidence.Verified || len(evidence.Unchanged) != 1 || len(evidence.Changed) != 0 || evidence.NextRunAt != nil || evidence.ScheduleCapture != nil {
					t.Fatalf("read/result ledger: %+v %+v present=%t", out, evidence, ok)
				}
				if out.Count > 0 {
					row := out.Jobs[0]
					if row.ID != triggerID.String() {
						t.Fatal("legacy trigger id changed")
					}
					if scenario == "unbound" {
						if row.ScheduleID != "" || row.JobID != "" {
							t.Fatal("unbound row invented schedule identity")
						}
					} else if row.ScheduleID != jobID.String() || row.JobID != row.ScheduleID {
						t.Fatalf("canonical schedule identity missing: %+v", row)
					}
				}
				if !strings.Contains(out.Message, "not id") {
					t.Fatal("list omitted ID guidance")
				}
				e := evidence.Unchanged[0]
				if !bot.ValidObservation(e) || e.ActorID != f.req.Bot.OwnerID || !strings.Contains(e.Source, f.req.Run.ID.String()+"/read%2Fexact-call") {
					t.Fatalf("not exact scoped read: %+v", e)
				}
				if _, ok := bot.TakeToolOutcome(ctx, c.FunctionCallID()); ok {
					t.Fatal("read reused")
				}
				if scenario == "filtered-empty" && !strings.Contains(out.Message, "No schedules matched") {
					t.Fatal("ambiguous empty filter")
				}
			})
		}
	}
}
