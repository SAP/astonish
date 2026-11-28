package agent

import (
	"strings"
	"testing"
)

func TestSchedulingGuidanceScopesExternalDeliveryToOrdinaryChat(t *testing.T) {
	for _, clause := range []string{"Bot conversations override the generic workflow", "same Bot's home chat", "do not ask for recipient", "never test, activate, or enable recurrence without the required authority", "separate reviewed configuration", "ordinary non-Bot Chat"} {
		if !strings.Contains(guidanceJobScheduling, clause) {
			t.Errorf("missing %q", clause)
		}
	}
}
