package sandbox

import (
	"context"
	"testing"
)

// Report the physical session gone once to exercise recreation from the
// original snapshot without running a container or replaying a tool call.
type botScopeRecoveryBackend struct {
	botHomePoolBackend
	inspected bool
}

func (b *botScopeRecoveryBackend) SessionState(context.Context, string) (SessionState, error) {
	if b.inspected {
		return SessionStateRunning, nil
	}
	b.inspected = true
	return SessionStateGone, nil
}

func TestBotHomePoolTrustedSessionScope(t *testing.T) {
	for _, scope := range []BotHomeScope{
		{OrgID: "owned-org", TeamID: "owned-team", UserID: "owner", BotID: "bot"},
		{UserID: "personal-owner", BotID: "bot"},
	} {
		t.Run(scope.UserID, func(t *testing.T) {
			b := &botScopeRecoveryBackend{botHomePoolBackend: botHomePoolBackend{specs: make(chan SessionSpec, 2)}}
			p := NewBackendPool(b, ResourceLimits{}).(*backendPool)
			binding, err := b.EnsureBotHome(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			original := binding.Clone()
			if err := BindBotHome(p, "owner-session", binding); err != nil {
				t.Fatal(err)
			}
			c := p.GetOrCreateWithTemplate("owner-session", "template").(*backendNodeClient)
			defer c.discard()
			binding.Scope.OrgID = "client-claimed-org"
			p.Alias("delegate", "owner-session")
			if err := c.EnsureReadyContext(t.Context(), "delegate"); err != nil {
				t.Fatal(err)
			}
			spec := <-b.specs
			if spec.OrgSlug != scope.OrgID || spec.TeamSlug != scope.TeamID || spec.UserID != scope.UserID {
				t.Fatalf("trusted scope lost: org=%q team=%q user=%q", spec.OrgSlug, spec.TeamSlug, spec.UserID)
			}
			if spec.SessionID != "owner-session" || !spec.BotHome.Equal(original) {
				t.Fatal("owner or retained home changed")
			}
			if err := c.EnsureReadyContext(t.Context(), "delegate"); err != nil {
				t.Fatal(err)
			}
			recreated := <-b.specs
			if recreated.OrgSlug != scope.OrgID || recreated.TeamSlug != scope.TeamID || recreated.UserID != scope.UserID || !recreated.BotHome.Equal(original) || recreated.SessionID != spec.SessionID {
				t.Fatal("recreation lost trusted scope or retained home")
			}
			if err := BindBotHome(p, "delegate", binding); err == nil {
				t.Fatal("accepted client-claimed replacement scope")
			}
		})
	}
}
