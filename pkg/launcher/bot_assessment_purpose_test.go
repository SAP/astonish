package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type assessmentPurposeBudget struct {
	botFactoryTestBudget
	blocked         bool
	denyReservation bool
	reservations    int
	checks          int
}

func (b *assessmentPurposeBudget) Check(ctx context.Context) error {
	b.checks++
	if b.blocked {
		return store.ErrBotForbidden
	}
	return ctx.Err()
}

func (b *assessmentPurposeBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	b.reservations++
	if b.denyReservation {
		return r, store.ErrBotForbidden
	}
	return r, ctx.Err()
}

func TestBotAssessmentPurposeIsolation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, _ := controlContextFixture(t)
			f.req.Bot.Scope = scope
			budget := &assessmentPurposeBudget{}
			f.req.Budget = budget
			f.cfg.WrapModel = func(_ context.Context, llm model.LLM, shared execution.RunBudget) (model.LLM, error) {
				if shared != budget {
					t.Fatal("assessment replaced shared budget")
				}
				f.wrapped++
				return provider.NewBudgetLLM(llm, shared, provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true})
			}
			prepare := f.cfg.Prepare
			isolationChecks, denyIsolationAt := 0, 0
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, req, scope)
				if err != nil {
					return nil, err
				}
				safe.VerifyIsolation = func(context.Context) error {
					isolationChecks++
					if denyIsolationAt > 0 && isolationChecks >= denyIsolationAt {
						return store.ErrBotForbidden
					}
					return nil
				}
				return safe, nil
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			prompt := "Assess candidate only. Return JSON."
			request := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText(prompt, "system")}}
			viewer := store.WithUserID(store.WithServices(context.Background(), &store.Services{}), "foreign-viewer")
			assessment := bot.WithModelCallPurpose(viewer, bot.ModelCallPurposeAssessment)
			call := func(ctx context.Context) error {
				for _, err := range resources.Agent.LLM.GenerateContent(ctx, request, false) {
					if err != nil {
						return err
					}
				}
				return nil
			}
			checks := budget.checks
			if err := call(assessment); err != nil {
				t.Fatal(err)
			}
			if got := providerSystem(f.resolver.model.requests[0]); got != prompt {
				t.Fatalf("assessment system was enriched: got %d bytes, want %d", len(got), len(prompt))
			}
			if budget.checks-checks < 2 {
				t.Fatal("assessment bypassed binding checks")
			}
			if store.UserIDFromContext(f.resolver.model.contexts[0]) != f.req.Bot.OwnerID.String() {
				t.Fatal("assessment inherited viewer scope")
			}
			if execution.RunPolicyFromContext(f.resolver.model.contexts[0]).Identity() != f.req.Policy.Identity() {
				t.Fatal("assessment lost run policy")
			}
			if f.wrapped != 1 {
				t.Fatal("assessment replaced wrapped provider")
			}
			if err := call(viewer); err != nil {
				t.Fatal(err)
			}
			if got := providerSystem(f.resolver.model.requests[1]); !strings.Contains(got, "## Current Bot control state") {
				t.Fatal("producer lost controls")
			}
			if providerSystem(request) != prompt {
				t.Fatal("original request mutated")
			}
			f.req.Run.Lease.Fence++
			if err := call(assessment); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("revoked assessment reached provider: %v", err)
			}
			f.req.Run.Lease.Fence--
			budget.blocked = true
			if err := call(assessment); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("assessment bypassed budget: %v", err)
			}
			budget.blocked = false
			budget.denyReservation = true
			if err := call(assessment); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("assessment bypassed wrapped reservation: %v", err)
			}
			if budget.reservations != 3 {
				t.Fatalf("shared reservations = %d, want 3", budget.reservations)
			}
			budget.denyReservation = false
			// The first binding check passes; the second must still block dispatch.
			denyIsolationAt = isolationChecks + 2
			if err := call(assessment); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("assessment bypassed second isolation check: %v", err)
			}
			if f.resolver.model.calls != 2 {
				t.Fatal("blocked assessment reached provider")
			}
		})
	}
}
