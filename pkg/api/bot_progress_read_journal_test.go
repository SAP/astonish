package api

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/genai"
)

func TestBotProgressReadJournalPendingReadSettlesAfterRestore(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}})
	call := &genai.FunctionCall{ID: "retained-search", Name: "execute_tool", Args: map[string]any{"name": "perplexity_web_search", "arguments": map[string]any{"query": "Tesla pricing"}}}
	journal := &botAgentReadJournal{}
	journal.capture(ctx, call)

	raw, err := json.Marshal(botProgressWorking{Assignment: "Tesla news", SessionID: req.Run.SessionID.String(), ReadJournal: journal.progressSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	var saved botProgressWorking
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	var restored botAgentReadJournal
	restored.restoreProgress(saved.ReadJournal)
	read, source := restored.take(&genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"state": "ready", "output": map[string]any{"results": []any{"Tesla result"}}}})
	if !read || !source || restored.effectState() != store.BotReportEffectsReadOnly {
		t.Fatalf("successful retained read stayed uncertain: read=%v source=%v state=%s snapshot=%+v", read, source, restored.effectState(), saved.ReadJournal)
	}
}

func TestBotProgressReadJournalRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		journal *botAgentReadJournal
		want    store.BotReportEffectState
	}{
		{name: "settled read", journal: &botAgentReadJournal{seen: map[string]bool{"read": true}, completed: map[string]bool{"read": true}}, want: store.BotReportEffectsReadOnly},
		{name: "confirmed effect", journal: &botAgentReadJournal{seen: map[string]bool{"write": true}, completed: map[string]bool{"write": true}, confirmed: true}, want: store.BotReportEffectsConfirmed},
		{name: "uncertain effect", journal: &botAgentReadJournal{seen: map[string]bool{"write": true}, completed: map[string]bool{"write": true}, uncertain: true}, want: store.BotReportEffectsUncertain},
		{name: "unfinished call", journal: &botAgentReadJournal{seen: map[string]bool{"read": true}, calls: map[string]botAgentReadCall{"read": {name: "read_file", concrete: "read_file", read: true, source: true}}}, want: store.BotReportEffectsUncertain},
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
			if tc.name == "unfinished call" {
				if _, ok := restored.calls["read"]; !ok || restored.uncertain {
					t.Fatalf("pending invocation binding was not retained: calls=%v uncertain=%v", restored.calls, restored.uncertain)
				}
			}
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
