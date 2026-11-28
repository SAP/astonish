package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBotResumeTargetError(t *testing.T) {
	id := uuid.New()
	err := fmt.Errorf("capture: %w", &BotResumeTargetError{ScheduleID: id})
	var target *BotResumeTargetError
	if !errors.Is(err, ErrBotInvalid) || !errors.As(err, &target) || target.ScheduleID != id {
		t.Fatal("typed invalid-target contract lost")
	}
	for _, text := range []string{BotResumeInvalidTargetCode, id.String(), "schedule_id", "not trigger id", "no operation was captured"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("missing %q: %v", text, err)
		}
	}
}
