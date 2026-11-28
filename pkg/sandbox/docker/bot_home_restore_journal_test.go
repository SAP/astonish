package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

func TestBotHomeRestoreJournalBeforeAllocation(t *testing.T) {
	b := &DockerBackend{}
	scope := sandbox.BotHomeScope{OrgID: "test", TeamID: "team", UserID: uuid.NewString(), BotID: uuid.NewString()}
	sentinel := errors.New("journal write failed")
	var reserved *sandbox.BotHomeBinding
	h, err := b.CreateBotHomeRestoreTargetJournaled(context.Background(), scope, func(h *sandbox.BotHomeBinding) error { reserved = h; return sentinel })
	if !errors.Is(err, sentinel) || h != nil || reserved == nil || !validBotHomeVolume(reserved) || reserved.Ref == botHomeVolume(scope) {
		t.Fatalf("allocation ran before durable fresh reservation: %v %v", h, err)
	}
}
