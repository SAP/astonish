package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"testing"
	"time"
)

type proposalTestRepo struct {
	botFactoryTestRepo
	store.BotControlProposalStore
	calls   int
	request store.BotControlProposalRequest
	actor   string
}

func (r *proposalTestRepo) ProposeControl(ctx context.Context, q store.BotControlProposalRequest) (*store.BotControlProposal, error) {
	r.calls++
	r.request = q
	p, _ := execution.PrincipalFromContext(ctx)
	r.actor = p.Subject
	return &store.BotControlProposal{ID: uuid.New(), BotID: q.Lease.BotID, Digest: "saved", Status: store.BotControlProposed}, nil
}

func (r *proposalTestRepo) ReadControlProposal(_ context.Context, id store.BotID, pid uuid.UUID) (*store.BotControlProposal, error) {
	return &store.BotControlProposal{ID: pid, BotID: id, Digest: "saved", Status: store.BotControlProposed}, nil
}

type proposalTestContext struct {
	botFactoryToolContext
	sid string
}

func (c proposalTestContext) SessionID() string      { return c.sid }
func (c proposalTestContext) FunctionCallID() string { return "call-id" }
func TestBotProposalCapabilityStrictBoundAndCapped(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, intent := range []store.BotExecutionIntent{store.BotIntentRegular, store.BotIntentDraftConversation} {
			t.Run(string(scope)+string(intent), func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				f.req.Run.Intent = intent
				if intent == store.BotIntentDraftConversation {
					f.req.Bot.Status = store.BotDraft
				}
				repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
				f.services.Bots, f.services.PersonalBots = repo, repo
				owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
				binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				raw, err := newBotProposalTool(binding)
				if err != nil {
					t.Fatal(err)
				}
				tool := raw.(*botProposalTool)
				ledgerCtx, _ := bot.WithToolOutcomes(context.Background())
				binding.owner = bot.WithToolOutcomesFromContext(binding.owner, ledgerCtx)
				ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ledgerCtx}, sid: f.req.Run.SessionID.String()}
				req := &model.LLMRequest{}
				if err = tool.ProcessRequest(ctx, req); err != nil {
					t.Fatal(err)
				}
				if req.Tools[botProposalToolName] != tool {
					t.Fatal("unguarded dispatch")
				}
				valid := map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "review", "responsibility": "new"}}
				bad := map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "review", "responsibility": "new", "actor_id": "forged"}}
				if _, err = tool.Run(ctx, bad); err == nil || repo.calls != 0 {
					t.Fatal("unknown payload accepted")
				}
				for i := 0; i < 3; i++ {
					out, e := tool.Run(ctx, valid)
					outcome, ok := bot.TakeToolOutcome(ledgerCtx, ctx.FunctionCallID())
					if !ok || !outcome.Verified || len(outcome.Changed) != 1 || outcome.Changed[0].Summary != "Saved control proposal for review; not applied" {
						t.Fatalf("capture evidence: %+v", outcome)
					}
					if e != nil || out["proposal_id"] == nil || out["payload"] != nil {
						t.Fatalf("reference: %+v %v", out, e)
					}
				}
				if _, err = tool.Run(ctx, valid); err == nil || repo.calls != 3 {
					t.Fatal("proposal spam not capped")
				}
				if repo.actor != f.req.Bot.OwnerID.String() || repo.request.Lease != f.req.Run.Lease || time.Until(repo.request.ExpiresAt) < 23*time.Hour {
					t.Fatal("unbound provenance or expiry")
				}
				tool.attempts = 0 // isolate live-lease refusal from the already exhausted cap
				f.req.Run.Lease.Fence++
				if _, err = tool.Run(ctx, valid); err == nil || repo.calls != 3 {
					t.Fatal("stale lease accepted")
				}
			})
		}
	}
}
