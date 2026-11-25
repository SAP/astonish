package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

type chatCallbackSchedulerStore struct {
	store.SchedulerStore
	jobs map[string]*store.ScheduledJob
}

func (s *chatCallbackSchedulerStore) Get(_ context.Context, id string) *store.ScheduledJob {
	if j := s.jobs[id]; j != nil {
		copy := *j
		return &copy
	}
	return nil
}

type chatCallbackFlowStore struct{ store.FlowStore }

func (*chatCallbackFlowStore) GetFlow(_ context.Context, name string) (string, error) {
	if name != "owner-flow" {
		return "", errors.New("foreign flow")
	}
	return "name: owner-flow\n", nil
}

type chatCallbackMemoryStore struct {
	store.MemoryStore
	adds  atomic.Int32
	check func(context.Context, store.MemoryEntry) error
}

func (*chatCallbackMemoryStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, nil
}
func (s *chatCallbackMemoryStore) Add(ctx context.Context, entry store.MemoryEntry) error {
	if err := s.check(ctx, entry); err != nil {
		return err
	}
	s.adds.Add(1)
	return nil
}

// Exercise the native authenticated HTTP handler and its injected runtime factory.
// Rebinding Inputs is what the real constructor and guarded tool dispatch do;
// callbacks installed only on ChatRunner.ctx disappear at this boundary.
func TestStudioChatHTTPScopedServiceCallbacks(t *testing.T) {
	for _, configured := range []bool{true, false} {
		t.Run(fmt.Sprintf("configured=%t", configured), func(t *testing.T) {
			env := setupScopedChatHTTPAcceptance(t, NewMockLLM(TextTurn("scoped callbacks complete"), TextTurn("scoped callbacks complete"), TextTurn("scoped callbacks complete")), nil)
			cm := GetChatManager()
			if err := cm.ensureReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			var factoryCalls, jobCalls, ambientCalls atomic.Int32
			var sessionID string
			memStore := &chatCallbackMemoryStore{}
			memStore.check = func(ctx context.Context, entry store.MemoryEntry) error {
				p, ok := execution.PrincipalFromContext(ctx)
				card, parsed := memory.ParseScenarioCard(entry.Content)
				if !ok || p.Subject != env.UserID || p.OrgSlug != env.OrgSlug || store.SessionIDFromContext(ctx) != sessionID || !parsed || card.Scope != string(store.MemoryScopeTeam) || !strings.Contains(entry.Content, sessionID) {
					return fmt.Errorf("lost owner/session or structured memory: %+v %+v", p, card)
				}
				return nil
			}
			env.Services.Memory = nil
			env.Services.Scheduler, env.Services.PersonalScheduler = nil, nil
			if configured {
				env.Services.Memory = memStore
				env.Services.PersonalFlows = &chatCallbackFlowStore{}
				env.Services.PersonalScheduler = &chatCallbackSchedulerStore{jobs: map[string]*store.ScheduledJob{
					"owner-job":   {ID: "owner-job", Name: "owner job", OwnerID: env.UserID, Mode: string(scheduler.ModeRoutine), Payload: store.JobPayload{Flow: "owner-flow"}},
					"foreign-job": {ID: "foreign-job", OwnerID: "foreign-owner", Mode: string(scheduler.ModeRoutine), Payload: store.JobPayload{Flow: "owner-flow"}},
					"bot-job":     {ID: "bot-job", OwnerID: env.UserID, Mode: string(scheduler.ModeBotTrigger)},
				}}
			}
			previousExecutor := GetExecutor()
			t.Cleanup(func() { SetExecutor(previousExecutor) })
			SetExecutor(&scheduler.Executor{RunHeadless: func(ctx context.Context, cfg *scheduler.HeadlessRunConfig) (string, error) {
				p, ok := execution.PrincipalFromContext(ctx)
				if !ok || p.Subject != env.UserID || p.OrgSlug != env.OrgSlug || p.TeamSlug != env.TeamSlug || store.UserIDFromContext(ctx) != env.UserID || store.SessionIDFromContext(ctx) != sessionID || string(cfg.FlowYAML) != "name: owner-flow\n" {
					return "", fmt.Errorf("scheduler lost scoped context: %+v %+v", p, cfg)
				}
				jobCalls.Add(1)
				return "owner scheduler result", nil
			}})
			cm.components.RuntimeFactory = func(ctx context.Context, req StudioChatRuntimeRequest) (runtime *StudioChatRuntime, runtimeErr error) {
				defer func() {
					if runtimeErr != nil {
						t.Errorf("factory: %v", runtimeErr)
					}
				}()
				factoryCalls.Add(1)
				sessionID = req.SessionID
				// Ambient capabilities must not override captured callbacks or fill missing ones.
				ctx = store.WithRunJobFunc(ctx, func(context.Context, string) (string, error) { ambientCalls.Add(1); return "ambient", nil })
				ctx = store.WithMemorySaveOrMerge(ctx, func(context.Context, store.MemoryStore, store.MemoryEntry) error { ambientCalls.Add(1); return nil })
				bound, err := req.Inputs.Bind(ctx)
				if err != nil {
					return nil, err
				}
				foreign, _ := execution.PrincipalFromContext(bound)
				foreign.Subject = "foreign-owner"
				foreignCtx, err := execution.WithPrincipal(bound, foreign)
				if err != nil {
					return nil, err
				}
				if _, err := req.Inputs.Bind(foreignCtx); err == nil {
					t.Error("foreign principal rebound owner inputs")
				}
				runJob := store.RunJobFuncFromContext(bound)
				merge := store.MemorySaveOrMergeFromContext(bound)
				if merge == nil {
					return nil, errors.New("scoped memory merger missing")
				}
				entry := store.MemoryEntry{Content: "Use the owner scoped callback to save reusable knowledge.", SessionID: sessionID, CreatedBy: req.UserID}
				if configured {
					if runJob == nil {
						return nil, errors.New("scoped scheduler callback missing after rebind")
					}
					result, err := runJob(bound, "owner-job")
					if err != nil || result != "owner scheduler result" {
						return nil, fmt.Errorf("scheduler result %q: %v", result, err)
					}
					for _, id := range []string{"foreign-job", "missing-job", "bot-job"} {
						if _, err := runJob(bound, id); err == nil {
							t.Errorf("executed denied job %q", id)
						}
					}
					if _, err := runJob(foreignCtx, "owner-job"); err == nil {
						t.Error("foreign owner invoked captured scheduler")
					}
					if _, err := runJob(context.Background(), "owner-job"); err == nil {
						t.Error("missing principal invoked captured scheduler")
					}
					cancelled, cancel := context.WithCancel(bound)
					cancel()
					if _, err := runJob(cancelled, "owner-job"); !errors.Is(err, context.Canceled) {
						t.Errorf("cancelled scheduler: %v", err)
					}
					if err := merge(foreignCtx, memStore, entry); err == nil {
						t.Error("foreign owner invoked captured memory merger")
					}
					if err := merge(context.Background(), memStore, entry); err == nil {
						t.Error("missing principal invoked captured memory merger")
					}
					if err := merge(cancelled, memStore, entry); !errors.Is(err, context.Canceled) {
						t.Errorf("cancelled memory: %v", err)
					}
					entry.SessionID = "viewer-session-must-not-win"
					if err := merge(bound, store.MemoryStoreFromContext(bound), entry); err != nil {
						return nil, err
					}
				} else {
					if runJob != nil {
						t.Error("missing scheduler inherited ambient callback")
					}
					if target := store.MemoryStoreFromContext(bound); target != nil {
						t.Error("missing memory inherited store")
					}
					if err := merge(bound, nil, entry); err == nil {
						t.Error("missing memory store accepted durable write")
					}
				}
				if err := initializeFixtureChatPlan(req); err != nil {
					return nil, err
				}
				return &StudioChatRuntime{ChatAgent: agent.NewChatAgent(req.LLM, nil, nil, req.SessionService, &agent.SystemPromptBuilder{}, false, true)}, nil
			}
			events := consumeMemoryAcceptanceSSE(t, env.Request(t, http.MethodPost, "/api/studio/chat", StudioChatRequest{Message: "exercise scoped callbacks", MemoryScope: "team", AutoApprove: true}, memoryIdentityAdmin))
			assertHasEvent(t, events, "text")
			want := int32(0)
			if configured {
				want = 1
			}
			if factoryCalls.Load() != 1 || jobCalls.Load() != want || memStore.adds.Load() != want || ambientCalls.Load() != 0 {
				t.Fatalf("factory=%d jobs=%d memory=%d ambient=%d", factoryCalls.Load(), jobCalls.Load(), memStore.adds.Load(), ambientCalls.Load())
			}
			for _, identity := range []memoryAcceptanceIdentity{memoryIdentityForeign, memoryIdentityDenied} {
				response := env.Request(t, http.MethodPost, "/api/studio/chat", StudioChatRequest{SessionID: sessionID, Message: "denied viewer"}, identity)
				response.Body.Close()
				if response.StatusCode < 400 || factoryCalls.Load() != 1 {
					t.Errorf("denied identity %s reached callbacks: HTTP %d", identity, response.StatusCode)
				}
			}
		})
	}
}
