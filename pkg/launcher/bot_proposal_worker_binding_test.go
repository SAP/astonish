package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Capture receives a private conversation but only its checked server binding
// may restore the original execution session. Caller session spoofing never does.
func TestBotProposalPrivateConversationBinding(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.Bots, f.services.PersonalBots = repo, repo
	owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
	binding := &botFactoryBinding{owner: owner, req: f.req, workerOnly: true, conversationID: "private-worker", verifyIsolation: func(context.Context) error { return nil }}
	raw, err := newBotProposalTool(binding)
	if err != nil {
		t.Fatal(err)
	}
	capture := raw.(*botProposalTool)
	args := map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "review", "responsibility": "new"}}
	for _, sid := range []string{"", "other-worker", f.req.Run.SessionID.String()} {
		ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: context.Background()}, sid: sid}
		if _, err := capture.Run(ctx, args); !errors.Is(err, store.ErrBotForbidden) || repo.calls != 0 {
			t.Fatalf("untrusted conversation %q reached capture: %v", sid, err)
		}
	}
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: context.Background()}, sid: binding.conversationID}
	if _, err := capture.Run(ctx, args); err != nil || repo.calls != 1 {
		t.Fatalf("bound private capture: calls=%d err=%v", repo.calls, err)
	}
	f.req.Run.Lease.Fence++
	if _, err := capture.Run(ctx, args); err == nil || repo.calls != 1 {
		t.Fatal("private binding bypassed stale lease")
	}
}
