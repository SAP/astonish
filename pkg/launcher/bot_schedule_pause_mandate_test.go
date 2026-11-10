package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The live employee-pause run selected schedule_pause in propose_bot_control,
// creating a legacy review instead of using the existing mandated pause tools.
func TestMandatedSchedulePauseCannotCreateLegacyReview(t *testing.T) {
	for _, mandated := range []bool{false, true} {
		name := "legacy"
		if mandated {
			name = "mandated"
		}
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
			f.services.Bots, f.services.PersonalBots = repo, repo
			if mandated {
				id := f.req.Policy.Identity()
				mandate := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control"}}}}}
				auth, err := execution.VerifyBotMandateAuthorization(context.Background(), mandate, mandate.saved.Ref, execution.BotMandatePurposeTask)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: mandate, Identity: id}
			}
			owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
			binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
			raw, err := newBotProposalTool(binding)
			if err != nil {
				t.Fatal(err)
			}
			proposal := raw.(*botProposalTool)
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: owner}, sid: f.req.Run.SessionID.String()}
			payload := store.BotControlPayload{Operation: store.BotControlSchedulePause, Reason: "Pause background weather reports", Schedule: &store.BotControlSchedule{ExpectedRevision: 8, Trigger: store.TriggerSpec{ID: uuid.New(), ScheduleID: new(uuid.New()), Kind: store.TriggerCron, Enabled: false}}}
			result, err := proposal.capture(ctx, botProposalArgs{Payload: payload})
			if !mandated {
				if err != nil || result["proposal_id"] == nil || repo.calls != 1 {
					t.Fatalf("legacy review changed: %v %v calls=%d", result, err, repo.calls)
				}
				return
			}
			if !errors.Is(err, store.ErrBotInvalid) || repo.calls != 0 || result != nil {
				t.Fatalf("mandated pause created review: %v %v calls=%d", result, err, repo.calls)
			}
			for _, name := range []string{"priority_pause", "update_scheduled_job"} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("missing existing pause route %q: %v", name, err)
				}
			}
			wire, err := json.Marshal(botProposalArgs{Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var args map[string]any
			if err := json.Unmarshal(wire, &args); err != nil {
				t.Fatal(err)
			}
			// Use the production schema-validation path, not only capture.
			result, err = proposal.Run(ctx, args)
			if !errors.Is(err, store.ErrBotInvalid) || result != nil || repo.calls != 0 {
				t.Fatalf("wire pause created review: %v %v calls=%d", result, err, repo.calls)
			}
		})
	}
}

func TestMandatedProposalSchemaExcludesLegacySchedulePause(t *testing.T) {
	for _, value := range botMandatedProposalInputSchema().Properties["payload"].Properties["operation"].Enum {
		if value == store.BotControlSchedulePause {
			t.Fatal("mandated schema advertises legacy schedule_pause review")
		}
	}
	text := botMandatedProposalInputSchema().Properties["payload"].Properties["operation"].Description
	for _, route := range []string{"priority_pause", "update_scheduled_job"} {
		if !strings.Contains(text, route) {
			t.Errorf("schema missing pause guidance: %s", route)
		}
	}
}
