package launcher

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type generationHealthModel struct {
	run func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error]
}

func (generationHealthModel) Name() string { return "health-test" }
func (m generationHealthModel) GenerateContent(c context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return m.run(c, r)
}

func TestBotGenerationHealthDispatchEvaluationAndRecovery(t *testing.T) {
	for _, mode := range []string{"healthy_evaluation", "blocked_authority_stall", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
				f.req.Run.Lease.ExpiresAt = time.Now().Add(time.Hour)
				budget, err := bot.NewRunBudget(f.req.Version.Budget, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				f.req.Budget = budget
				dispatch := &progressDispatchProbe{}
				owner := bot.WithProgressDispatch(store.WithServices(f.ctx, f.services), dispatch)
				checks, calls, joins := 0, 0, 0
				var blockCheck atomic.Bool
				blockCheck.Store(mode == "blocked_authority_stall")
				binding := &botFactoryBinding{owner: owner, req: f.req, progress: &botProgressBridge{}, verifyIsolation: func(c context.Context) error {
					checks++
					if blockCheck.Load() {
						<-c.Done()
						joins++
						return c.Err()
					}
					return nil
				}}
				if mode == "revoked" {
					f.req.Run.Lease.Fence++ // Captured binding is now stale before monitor starts.
				}
				inner := generationHealthModel{run: func(c context.Context, r *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
					return func(y func(*model.LLMResponse, error) bool) {
						calls++
						if !streaming.HealthEnabled(c) {
							t.Error("dispatch did not install health")
						}
						if _, ok := c.Deadline(); ok {
							t.Error("health generation has age deadline")
						}
						if blockCheck.Load() {
							<-c.Done()
							y(nil, c.Err())
							return
						}
						if mode == "revoked" && calls == 1 {
							for c.Err() == nil {
								time.Sleep(time.Second)
								streaming.ReportUpstreamActivity(c)
							}
							y(nil, c.Err())
							return
						}
						for range 6 {
							time.Sleep(30 * time.Second)
							streaming.ReportUpstreamActivity(c)
							if c.Err() != nil {
								t.Error(c.Err())
								return
							}
						}
						if mode == "healthy_evaluation" && bot.ModelCallPurposeFromContext(c) != bot.ModelCallPurposeProgress {
							t.Error("missing evaluator purpose")
						}
						y(&model.LLMResponse{Content: genai.NewContentFromText(`{"decision":"continue","summary":"Continue from retained sources.","next_steps":[],"evidence_refs":[]}`, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5}}, nil)
					}
				}}
				metered, err := provider.NewBudgetLLM(inner, budget, provider.BudgetLLMOptions{MaxOutputTokens: 2048, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 10, nil }})
				if err != nil {
					t.Fatal(err)
				}
				llm := &botProgressLLM{LLM: metered, binding: binding}
				if mode == "healthy_evaluation" {
					commits := 0
					c := &bot.ProgressController{LLM: llm, Interval: time.Minute, Timeout: 0, Now: time.Now, Boundary: binding.progress.boundary, Observe: func(context.Context) (bot.ProgressObservation, error) {
						return bot.ProgressObservation{Settled: true, Fingerprint: "retained"}, nil
					}, Commit: func(context.Context, bot.ProgressObservation, bot.ProgressDecision, time.Time) error {
						commits++
						return nil
					}, AskUser: func(context.Context, string) error { return nil }}
					if _, err := c.Check(owner); err != nil {
						t.Fatal(err)
					}
					time.Sleep(time.Minute)
					d, err := c.Check(owner)
					if err != nil || d == nil || commits != 1 || calls != 1 || checks == 0 || budget.Usage().OutputTokens != 5 {
						t.Fatalf("evaluation err=%v decision=%v commits=%d calls=%d checks=%d usage=%+v", err, d, commits, calls, checks, budget.Usage())
					}
				} else {
					var result error
					for _, e := range llm.GenerateContent(owner, &model.LLMRequest{}, true) {
						result = errors.Join(result, e)
					}
					if mode == "revoked" {
						if !errors.Is(result, store.ErrBotConflict) {
							t.Fatalf("authority not checked: %v", result)
						}
						f.req.Run.Lease.Fence--
					} else {
						var stalled *streaming.GenerationStalledError
						if !errors.As(result, &stalled) || joins != 1 {
							t.Fatalf("stall/join: %v joins=%d", result, joins)
						}
					}
					if err := binding.progress.boundary(owner); err != nil {
						t.Fatal(err)
					}
					// A later generation uses the same cumulative budget. Nothing is replayed
					// automatically, and a retired monitor cannot cancel the fresh call.
					if calls != 1 || dispatch.began != 1 || dispatch.settled != 1 {
						t.Fatalf("unexpected replay: calls=%d dispatch=%+v", calls, dispatch)
					}
					blockCheck.Store(false)
					for _, e := range llm.GenerateContent(owner, &model.LLMRequest{}, false) {
						if e != nil {
							t.Fatal(e)
						}
					}
					if calls != 2 || budget.Usage().OutputTokens != 2053 {
						t.Fatalf("recovery lost held usage: calls=%d usage=%+v", calls, budget.Usage())
					}
				}
				if err := binding.progress.boundary(owner); err != nil {
					t.Fatal(err)
				}
				if dispatch.began != calls || dispatch.settled != calls {
					t.Fatalf("unjoined dispatch: %+v calls=%d", dispatch, calls)
				}
			})
		})
	}
}
