package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Report evidence is a selected subset, not the run's tool-call ledger. Neither
// the subset size nor usage.ToolCalls attests completed external actions.
func TestAgentFallbackDoesNotPresentEvidenceCountAsToolActivity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		observations int
	}{
		{name: "no_observations", observations: 0},
		{name: "one_observation", observations: 1},
		{name: "two_discovery_observations_fifteen_calls", observations: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, report, _ := agentEmployeeFixture(t, store.AgentWorkPartial)
			report.Work.Phase = store.BotWorkWorking
			report.Changed, report.Unchanged = nil, nil
			for i := 0; i < tc.observations; i++ {
				evidence := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "private discovery result", Source: "private tool source", ObservedAt: time.Now(), ActorID: uuid.New()}
				if i%2 == 0 {
					report.Changed = append(report.Changed, evidence)
				} else {
					report.Unchanged = append(report.Unchanged, evidence)
				}
			}
			usage := store.BotUsage{ToolCalls: 15}
			message := ComposeEmployeeMessage(context.Background(), store.RunFailed, report, nil, EmployeeMessageOptions{Usage: &usage})
			for _, forbidden := range []string{"tool observation(s)", "15 tool", "private discovery result", "private tool source"} {
				if strings.Contains(message.Text, forbidden) {
					t.Errorf("misleading or private fallback text %q: %s", forbidden, message.Text)
				}
			}
			if !strings.Contains(message.Text, "No publishable final answer") {
				t.Errorf("fallback did not explain missing answer: %s", message.Text)
			}
			if tc.observations > 0 && !strings.Contains(message.Text, "does not confirm that all requested actions completed") {
				t.Errorf("fallback lost nonconfirmation warning: %s", message.Text)
			}
			report.EmployeeMessage = &message
			if err := report.ValidateEmployeeMessageIntegrity(store.RunFailed); err != nil {
				t.Fatalf("fallback publication integrity: %v", err)
			}
			if usage.ToolCalls != 15 {
				t.Fatalf("fallback changed accounting: %+v", usage)
			}
		})
	}
}
