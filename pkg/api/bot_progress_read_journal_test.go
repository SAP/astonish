package api

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotProgressReadJournalRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		journal *botAgentReadJournal
		want    store.BotReportEffectState
	}{
		{name: "settled read", journal: &botAgentReadJournal{seen: map[string]bool{"read": true}, completed: map[string]bool{"read": true}}, want: store.BotReportEffectsReadOnly},
		{name: "confirmed effect", journal: &botAgentReadJournal{seen: map[string]bool{"write": true}, completed: map[string]bool{"write": true}, confirmed: true}, want: store.BotReportEffectsConfirmed},
		{name: "uncertain effect", journal: &botAgentReadJournal{seen: map[string]bool{"write": true}, completed: map[string]bool{"write": true}, uncertain: true}, want: store.BotReportEffectsUncertain},
		{name: "unfinished call", journal: &botAgentReadJournal{seen: map[string]bool{"read": true}, calls: map[string]botAgentReadCall{"read": {name: "read_file", read: true}}}, want: store.BotReportEffectsUncertain},
		{name: "no evidence", want: store.BotReportEffectsUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := tc.journal.progressSnapshot()
			raw, err := json.Marshal(botProgressWorking{Assignment: "original assignment", SessionID: "same session", ReadJournal: snapshot})
			if err != nil {
				t.Fatal(err)
			}
			var saved botProgressWorking
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			var restored botAgentReadJournal
			restored.restoreProgress(saved.ReadJournal)
			if got := restored.effectState(); got != tc.want {
				t.Fatalf("restored effect=%s want=%s", got, tc.want)
			}
			if tc.journal != nil && len(tc.journal.seen) > 0 {
				delete(tc.journal.seen, "read")
				if tc.name == "settled read" && !snapshot.Seen["read"] {
					t.Fatal("snapshot aliases mutable journal")
				}
			}
		})
	}
}
