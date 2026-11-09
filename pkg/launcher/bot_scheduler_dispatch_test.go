package launcher

import (
	"context"
	"encoding/json"
	"iter"
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
	"google.golang.org/genai"
)

type schedulerDispatchRepo struct {
	scheduleListRepo
	sessions session.Service
}

func (r schedulerDispatchRepo) AppendBotRunEvent(ctx context.Context, _ store.BotLease, id uuid.UUID, event *session.Event) error {
	s, err := r.sessions.Get(ctx, &session.GetRequest{AppName: "astonish", UserID: r.fixture.req.Bot.OwnerID.String(), SessionID: id.String()})
	if err != nil {
		return err
	}
	return r.sessions.AppendEvent(ctx, s.Session, event)
}

type schedulerDispatchJobs struct {
	store.SchedulerStore
	job   *store.ScheduledJob
	calls int
}

func (s *schedulerDispatchJobs) Get(_ context.Context, id string) *store.ScheduledJob {
	s.calls++
	if id != s.job.ID {
		return nil
	}
	return s.job
}

type schedulerDispatchModel struct {
	t                 *testing.T
	callName          string
	calls             int
	restored          bool
	firstDeclarations string
}

func (*schedulerDispatchModel) Name() string { return "scheduler-dispatch" }
func (m *schedulerDispatchModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		names := map[string]int{}
		for _, tools := range req.Config.Tools {
			for _, d := range tools.FunctionDeclarations {
				names[d.Name]++
			}
		}
		if names["list_scheduled_jobs"] != 1 || req.Tools["list_scheduled_jobs"] == nil {
			m.t.Errorf("authorized scheduler missing: declarations=%v dispatch=%v", names, req.Tools)
		}
		if names["shell_command"] != 0 || req.Tools["shell_command"] != nil {
			m.t.Error("excluded tool exposed")
		}
		if _, ok := req.Tools["list_scheduled_jobs"].(*botBoundTool); !ok {
			m.t.Errorf("scheduler dispatch lost wrapper: %T", req.Tools["list_scheduled_jobs"])
		}
		raw, _ := json.Marshal(req.Config.Tools)
		if m.firstDeclarations == "" {
			m.firstDeclarations = string(raw)
		} else if string(raw) != m.firstDeclarations {
			m.t.Error("provider declarations changed mid-run")
		}
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionCall != nil && p.FunctionCall.ID == "historical-list" {
					m.restored = true
				}
			}
		}
		if m.calls > 1 && m.callName != "shell_command" {
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if response := part.FunctionResponse; response != nil && response.ID == "current-list" {
						data, _ := json.Marshal(response.Response)
						if !strings.Contains(string(data), `"error"`) && !strings.Contains(string(data), `"count":1`) {
							m.t.Errorf("direct/bridge schedule listing lost its row: %s", data)
						}
					}
				}
			}
		}
		if m.calls == 1 {
			args := map[string]any{"filter": "weather"}
			if m.callName == "execute_tool" {
				args = map[string]any{"name": "list_scheduled_jobs", "arguments": args}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "current-list", Name: m.callName, Args: args}}}}, TurnComplete: true}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Schedules inspected; no schedule changed.", "model"), TurnComplete: true}, nil)
	}
}

func TestBotSchedulerDispatchRestoredHistory(t *testing.T) {
	testBotSchedulerDispatch(t, execution.BotMandatePurposeTask)
}

func TestBotPausedSchedulerDispatchRestoredHistory(t *testing.T) {
	testBotSchedulerDispatch(t, execution.BotMandatePurposeOperation)
}

func testBotSchedulerDispatch(t *testing.T, purpose execution.BotMandatePurpose) {
	t.Helper()
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, runtime, _ := discoveryMandateFixture(t)
			f.req.Bot.Scope = scope
			f.req.Bot.Revision = 1
			runtime.saved.Constraints[1].Resources = []string{"list_scheduled_jobs"}
			runtime.saved.Purpose = purpose
			if purpose == execution.BotMandatePurposeOperation {
				f.req.Bot.Status = store.BotPaused
				f.req.Run.Intent = store.BotIntentConversation
			}
			auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate.Authorization = auth
			f.req.Principal = auth.Principal(execution.SurfaceStudio)
			f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"list_scheduled_jobs"}})
			if err != nil {
				t.Fatal(err)
			}
			tr, jobID := uuid.New(), uuid.New()
			f.req.Version.Triggers = []store.TriggerSpec{{ID: tr, Kind: store.TriggerCron, ScheduleID: &jobID, Cron: "*/2 * * * *", Timezone: "UTC", Instructions: "weather"}}
			jobs := &schedulerDispatchJobs{job: &store.ScheduledJob{ID: jobID.String(), Name: "weather", OwnerID: f.req.Bot.OwnerID.String(), Schedule: store.JobSchedule{Cron: "*/2 * * * *", Timezone: "UTC"}, Payload: store.JobPayload{BotID: f.req.Bot.ID.String(), BotTriggerID: tr.String(), BotVersion: f.req.Version.Version}}}
			repo := schedulerDispatchRepo{scheduleListRepo: scheduleListRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, version: &f.req.Version}, sessions: f.sessions}
			f.services.Bots, f.services.PersonalBots = repo, repo
			f.services.Scheduler, f.services.PersonalScheduler = jobs, jobs
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, r, s)
				if err != nil {
					return nil, err
				}
				schedules, err := BotScheduleTools(r, s)
				safe.Tools = append(safe.Tools, schedules...)
				return safe, err
			}
			existing, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
			if err != nil {
				t.Fatal(err)
			}
			for _, content := range []*genai.Content{
				{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "historical-list", Name: "list_scheduled_jobs", Args: map[string]any{"filter": "weather"}}}}},
				{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "historical-list", Name: "list_scheduled_jobs", Response: map[string]any{"count": 1}}}}},
			} {
				if err := f.sessions.AppendEvent(f.ctx, existing.Session, &session.Event{ID: uuid.NewString(), InvocationID: "past-run", Author: "chat", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: content}}); err != nil {
					t.Fatal(err)
				}
			}
			for i, call := range []string{"list_scheduled_jobs", "execute_tool", "list_scheduled_jobs", "execute_tool", "shell_command", "list_scheduled_jobs_future", "update_scheduled_job", "http_request"} {
				readsBefore := jobs.calls
				excluded := i >= 4
				foreign := i == 2 || i == 3
				if foreign {
					jobs.job.OwnerID = uuid.NewString()
				} else {
					jobs.job.OwnerID = f.req.Bot.OwnerID.String()
				}
				// Reconstruct resources for a new run over the same stored conversation.
				identity := f.req.Policy.Identity()
				identity.RunID = uuid.New()
				f.req.Run.ID, f.req.Run.Lease.RunID = identity.RunID, identity.RunID
				f.req.Mandate.Identity = identity
				f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: []string{"list_scheduled_jobs"}})
				if err != nil {
					t.Fatal(err)
				}
				llm := &schedulerDispatchModel{t: t, callName: call}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
				factory, err := NewBotChatAgentFactory(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				adapter, err := api.NewBotChatRuntimeAdapter(factory)
				if err != nil {
					t.Fatal(err)
				}
				result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if foreign || excluded {
					if err == nil || result.Status == store.RunSucceeded || len(result.Report.Changed) != 0 {
						t.Fatalf("forbidden call succeeded: %s result=%+v err=%v", call, result, err)
					}
					wantReads := readsBefore
					if foreign {
						wantReads++
					}
					if jobs.calls != wantReads {
						t.Fatalf("forbidden call dispatches=%d want=%d", jobs.calls, wantReads)
					}
					continue
				}
				if err != nil || result.Status != store.RunSucceeded {
					t.Fatalf("%s result=%+v err=%v", call, result, err)
				}
				if !llm.restored || llm.calls != 2 || jobs.calls != i+1 {
					t.Fatalf("%s restored=%v rounds=%d dispatch=%d", call, llm.restored, llm.calls, jobs.calls)
				}
				if len(result.Report.Unchanged) != 1 || !strings.Contains(result.Report.Unchanged[0].Source, "current-list") || len(result.Report.Changed) != 0 || result.Report.NextRunAt != nil {
					t.Fatalf("unexpected read result: %+v", result.Report)
				}
			}
			runtime.revoked = true
			reads := jobs.calls
			if _, err := f.construct(t); err == nil {
				t.Fatal("revoked scheduler mandate accepted")
			}
			if jobs.calls != reads {
				t.Fatal("revoked mandate read schedules")
			}
		})
	}
}
