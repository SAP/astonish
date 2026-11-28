package agent

import "testing"

func TestCodePlanBookkeepingAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wantConsent bool
	}{
		{"announce_plan", false},
		{"update_plan", false},
		{"announce_completion", false},
		{"shell_command", true},
		{"write_file", true},
		{"edit_file", true},
		{"delegate_tasks", true},
		{"memory_save", true},
		{"unknown_tool", true},
		{"announce_completion_external", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiresToolAuthorization(tc.name, false); got != tc.wantConsent {
				t.Fatalf("RequiresToolAuthorization(%q, false) = %v, want %v", tc.name, got, tc.wantConsent)
			}
		})
	}
}
