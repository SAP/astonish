package bot

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotVersionAcceptsZeroHistoricRuntime(t *testing.T) {
	v := store.DefaultBotVersion()
	v.BotID, v.Charter = uuid.New(), "Research the assignment."
	v.Budget.MaxRuntimeSeconds = 0
	if err := ValidateVersion(v); err != nil {
		t.Fatalf("zero historic runtime rejected: %v", err)
	}
	v.Budget.MaxRuntimeSeconds = -1
	if ValidateVersion(v) == nil {
		t.Fatal("negative historic runtime accepted")
	}
}
