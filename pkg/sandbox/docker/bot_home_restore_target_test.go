package docker

import (
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
	"testing"
)

func TestBotHomeRestoreGenerationIdentity(t *testing.T) {
	scope := sandbox.BotHomeScope{OrgID: "restore", TeamID: "team", UserID: uuid.NewString(), BotID: uuid.NewString()}
	base := botHomeVolume(scope)
	suffix := uuid.NewString()
	h := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindDocker, Ref: base + "-restore-" + suffix}
	if !validBotHomeVolume(h) || workspaceVolume(h) != "astonish-bot-workspace-"+scope.Key()+"-restore-"+suffix {
		t.Fatal("fresh ordinary and private generations not paired")
	}
	for _, invalid := range []string{base + "-restore-", base + "-restore-" + uuid.Nil.String(), base + "-restore-../../foreign", "foreign-restore-" + suffix} {
		h.Ref = invalid
		if validBotHomeVolume(h) {
			t.Fatalf("accepted %q", invalid)
		}
	}
	h.Ref = base
	if !validBotHomeVolume(h) || workspaceVolume(h) != "astonish-bot-workspace-"+scope.Key() {
		t.Fatal("ordinary Home mapping changed")
	}
}
