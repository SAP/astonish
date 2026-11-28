package daemon

import "testing"

// These are preserved behavior contracts, not a catalogue-only parity claim.
// HTTP fixtures use scoped temporary SQLite, the real worker and scripted models;
// the recovery/budget cases are explicitly narrower lifecycle unit evidence.
func TestBotEnhancedChatBaseline(t *testing.T) {
	t.Run("title_configured_search", TestBotTitleConfiguredSearchHTTP)
	t.Run("schedule_confirmation_then_execution", TestBotAuthorizedScheduleHTTPAcceptance)
	t.Run("owner_priority_and_control", TestBotEmployeeOperationsHTTPAcceptance)
	t.Run("paused_recovery", TestBotWorkerPauseRecoveryAfterDatabaseReopen)
	t.Run("continuation_preserves_budget", TestBotWorkerResumedBudgetExhausted)
}
