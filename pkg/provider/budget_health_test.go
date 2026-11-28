package provider_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

// Expose only RunBudget to exercise compatibility with budgets that have no
// optional in-flight contract.
type admissionOnlyBudget struct{ execution.RunBudget }

func TestGenerationBudgetLegacyFallback(t *testing.T) {
	b := meter(t, 110)
	ctx := context.Background()
	if _, err := b.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 10, OutputTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(provider.CheckGenerationBudget(ctx, admissionOnlyBudget{b}), bot.ErrBudgetExceeded) {
		t.Fatal("legacy budget check bypassed")
	}
}

func healthMeter(t *testing.T) *bot.SharedRunBudget {
	t.Helper()
	b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBudgetGenerationHealthExactReservation(t *testing.T) {
	for _, limit := range []string{"tokens", "cost"} {
		t.Run(limit, func(t *testing.T) {
			for _, stream := range []bool{true, false} {
				synctest.Test(t, func(t *testing.T) {
					ceiling := int64(110)
					spec := store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: execution.NewBotProgressRuntimePolicy()}
					if limit == "tokens" {
						spec.MaxTokens = &ceiling
					} else {
						spec.MaxModelCostMicros = &ceiling
					}
					b, err := bot.NewRunBudget(spec, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					opts := budgetOptions()
					opts.Price = func(in, out int64) (*int64, error) { n := in + out; return &n, nil }
					checks := 0
					ctx := provider.WithGenerationHealthCheck(context.Background(), func(context.Context) error { checks++; return nil })
					m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
						return func(y func(*model.LLMResponse, error) bool) {
							if !errors.Is(b.Check(c), bot.ErrBudgetExceeded) {
								t.Error("new admission accepted at ceiling")
							}
							for range 40 {
								time.Sleep(time.Second)
								streaming.ReportUpstreamActivity(c)
								if c.Err() != nil {
									y(nil, c.Err())
									return
								}
							}
							y(usageResponse(10, 5), nil)
						}
					}}
					responses, err := consume(wrap(t, m, b, opts), ctx, &model.LLMRequest{}, stream)
					if err != nil || len(responses) != 1 || checks < 3 {
						t.Fatalf("exact %s hold cancelled healthy generation: err=%v responses=%d checks=%d", limit, err, len(responses), checks)
					}
					u := b.Usage()
					if u.InputTokens != 10 || u.OutputTokens != 5 || u.CostMicros == nil || *u.CostMicros != 15 {
						t.Fatalf("actual usage not settled: %+v", u)
					}
				})
			}
		})
	}
}

func TestBudgetGenerationHealthSixHoursAndFinalUsage(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "single"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := healthMeter(t)
				start := time.Now()
				checks := 0
				ctx := provider.WithGenerationHealthCheck(context.Background(), func(context.Context) error { checks++; return nil })
				var call context.Context
				m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
					call = c
					return func(y func(*model.LLMResponse, error) bool) {
						if !streaming.HealthEnabled(c) {
							t.Fatal("health not installed")
						}
						if _, ok := c.Deadline(); ok {
							t.Fatal("generation age deadline")
						}
						for range 720 {
							time.Sleep(30 * time.Second)
							streaming.ReportUpstreamActivity(c)
							if c.Err() != nil {
								t.Fatal(c.Err())
							}
						}
						y(usageResponse(10, 5), nil)
					}
				}}
				responses, err := consume(wrap(t, m, b, budgetOptions()), ctx, &model.LLMRequest{}, stream)
				if err != nil || len(responses) != 1 || b.Usage().InputTokens != 10 || b.Usage().OutputTokens != 5 {
					t.Fatalf("normal stop/usage: %v %+v", err, b.Usage())
				}
				if time.Since(start) != 6*time.Hour || checks == 0 || call.Err() == nil {
					t.Fatal("monitor not exercised and joined")
				}
				n := checks
				time.Sleep(time.Minute)
				if checks != n {
					t.Fatal("check leaked after completion")
				}
			})
		})
	}
}

func TestBudgetGenerationHealthStallJoinsBlockedAuthority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := healthMeter(t)
		joined := false
		ctx := provider.WithGenerationHealthCheck(context.Background(), func(c context.Context) error { <-c.Done(); joined = true; return c.Err() })
		m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
			return func(y func(*model.LLMResponse, error) bool) { <-c.Done(); y(nil, c.Err()) }
		}}
		_, err := consume(wrap(t, m, b, budgetOptions()), ctx, &model.LLMRequest{}, true)
		var stalled *streaming.GenerationStalledError
		if !errors.As(err, &stalled) || !joined {
			t.Fatalf("stall/join: %v %v", err, joined)
		}
		if b.Usage().InputTokens != 10 || b.Usage().OutputTokens != 100 {
			t.Fatalf("uncertain usage released: %+v", b.Usage())
		}
	})
}

func TestBudgetGenerationHealthRevocationDespiteActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := healthMeter(t)
		revoked := errors.New("authority revoked")
		checks := 0
		ctx := provider.WithGenerationHealthCheck(context.Background(), func(context.Context) error {
			checks++
			if checks == 3 {
				return revoked
			}
			return nil
		})
		m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
			return func(y func(*model.LLMResponse, error) bool) {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-c.Done():
						y(nil, c.Err())
						return
					case <-ticker.C:
						streaming.ReportUpstreamActivity(c)
					}
				}
			}
		}}
		_, err := consume(wrap(t, m, b, budgetOptions()), ctx, &model.LLMRequest{}, false)
		if !errors.Is(err, revoked) || checks != 3 || m.calls.Load() != 1 {
			t.Fatalf("revocation/replay: %v checks=%d calls=%d", err, checks, m.calls.Load())
		}
	})
}

func TestBudgetGenerationHealthEarlyExitJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := healthMeter(t)
		joined, drained := false, false
		ctx := provider.WithGenerationHealthCheck(context.Background(), func(c context.Context) error { <-c.Done(); joined = true; return c.Err() })
		m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
			return func(y func(*model.LLMResponse, error) bool) {
				defer func() { drained = true }()
				time.Sleep(11 * time.Second)
				y(usageResponse(10, 5), nil)
			}
		}}
		for range wrap(t, m, b, budgetOptions()).GenerateContent(ctx, &model.LLMRequest{}, true) {
			break
		}
		if !joined || !drained {
			t.Fatal("early consumer returned before provider/check joined")
		}
		if b.Usage().OutputTokens != 100 {
			t.Fatal("early exit refunded uncertain reservation")
		}
	})
}

func TestBudgetGenerationHealthParentDeadlineJoinsBlockedAuthority(t *testing.T) {
	for _, boundary := range []string{"active_budget", "authority_expiry", "lease_cancel"} {
		t.Run(boundary, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := execution.NewBotProgressRuntimePolicy()
				cap := int64(25)
				if boundary == "active_budget" {
					p.MaxActiveSeconds = &cap
				}
				b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: p}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if boundary == "authority_expiry" {
					var done context.CancelFunc
					ctx, done = context.WithTimeout(ctx, 25*time.Second)
					defer done()
				}
				if boundary == "lease_cancel" {
					go func() { time.Sleep(25 * time.Second); cancel() }()
				}
				joined := false
				ctx = provider.WithGenerationHealthCheck(ctx, func(c context.Context) error { <-c.Done(); joined = true; return c.Err() })
				m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
					return func(y func(*model.LLMResponse, error) bool) { <-c.Done(); y(nil, c.Err()) }
				}}
				start := time.Now()
				_, err = consume(wrap(t, m, b, budgetOptions()), ctx, &model.LLMRequest{}, true)
				if err == nil || !joined || time.Since(start) != 25*time.Second {
					t.Fatalf("boundary %s err=%v joined=%v elapsed=%s", boundary, err, joined, time.Since(start))
				}
			})
		})
	}
}
