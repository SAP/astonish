package api

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestChatRunnerInjectFleetSetupStores(t *testing.T) {
	runner := newChatRunner("session-setup", studioChatUserID, true)
	profileStore := getSetupProfileStore(&store.Services{})
	draftStore := getSetupDraftStore(&store.Services{})

	runner.InjectFleetSetupStores(profileStore, draftStore)

	if got := store.FleetSetupProfileStoreFromContext(runner.ctx); got == nil {
		t.Fatal("expected setup profile store in runner context")
	}
	if got := store.FleetSetupDraftStoreFromContext(runner.ctx); got == nil {
		t.Fatal("expected setup draft store in runner context")
	}
}
