package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

func TestBotOwnerEmailControlContext(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, _ := controlContextFixture(t)
			f.req.Bot.Scope = scope
			load := f.cfg.LoadScope
			state := store.BotOwnerEmailDelivery{State: store.BotOwnerEmailReady, AccountEmail: "owner@example.test", Binding: &store.BotChannelBinding{ID: uuid.New(), Channel: store.BotDeliveryChannelEmail, ChatID: "owner@example.test"}, NextStep: "Review delivery"}
			var readErr error
			calls := 0
			f.cfg.LoadScope = func(ctx context.Context, q bot.RunRequest) (*BotChatScope, error) {
				s, err := load(ctx, q)
				if err != nil {
					return nil, err
				}
				s.OwnerEmail = func(c context.Context) (store.BotOwnerEmailDelivery, error) {
					p, ok := execution.PrincipalFromContext(c)
					if !ok || p.Subject != f.req.Principal.Subject || p.OrgSlug != f.req.Principal.OrgSlug || p.TeamSlug != f.req.Principal.TeamSlug {
						t.Fatal("lost captured owner scope")
					}
					calls++
					return state, readErr
				}
				return s, nil
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			invoke := func() error {
				var got error
				for _, e := range resources.Agent.LLM.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
					got = e
				}
				return got
			}
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			first := providerSystem(f.resolver.model.requests[0])
			if !strings.Contains(first, `"owner_email_delivery"`) || !strings.Contains(first, state.Binding.ID.String()) || !strings.Contains(first, state.AccountEmail) {
				t.Fatal("owner recipient missing")
			}
			if !strings.Contains(first, "owner_email_delivery is discovery, not approval") {
				t.Fatal("missing owner-email action/authority guidance")
			}
			state = store.BotOwnerEmailDelivery{State: store.BotOwnerEmailNeedsVerification, AccountEmail: "new@example.test", NextStep: "Verify known account"}
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			second := providerSystem(f.resolver.model.requests[1])
			if strings.Contains(second, "owner@example.test") || !strings.Contains(second, "new@example.test") || !strings.Contains(second, "verification_required") {
				t.Fatal("stale owner delivery target")
			}
			readErr = errors.New("PRIVATE storage details")
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			third := providerSystem(f.resolver.model.requests[2])
			if strings.Contains(third, "PRIVATE") || strings.Contains(third, "new@example.test") || !strings.Contains(third, `"state":"unavailable"`) {
				t.Fatal("failed lookup leaks or invents target")
			}
			readErr = store.ErrBotForbidden
			if err := invoke(); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("revoked discovery reached model")
			}
			if calls != 4 || len(f.resolver.model.requests) != 3 {
				t.Fatal("discovery cached or revoked model called")
			}
		})
	}
}
