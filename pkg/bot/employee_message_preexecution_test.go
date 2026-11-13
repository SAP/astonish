package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestEmployeePreparationFailureMessage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		alter    func(*store.BotWorkState, *store.BotReport, *store.BotUsage)
		noResult bool
	}{
		{name: "preparation", noResult: true},
		{name: "candidate", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "draft", Text: "partial draft"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
		}},
		{name: "changed", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {
			r.Changed = []store.BotEvidence{{}}
		}},
		{name: "unchanged", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {
			r.Unchanged = []store.BotEvidence{{}}
		}},
		{name: "artifact", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {
			r.Artifacts = []store.BotArtifact{{}}
		}},
		{name: "tools", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) { u.ToolCalls = 1 }},
		{name: "prepared", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {
			w.Criteria = []store.BotWorkCriterion{{ID: "prepared"}}
		}},
		{name: "unknown-usage", alter: func(w *store.BotWorkState, r *store.BotReport, u *store.BotUsage) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := store.NewBotWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Write a report"})
			w.Phase = store.BotWorkBlocked
			w.Unresolved = []string{"preparation failed"}
			r := store.BotReport{Summary: "PRIVATE preparation diagnostics", Failure: &store.BotReportFailure{Kind: store.BotReportFailureOperational, TechnicalDetails: "PRIVATE malformed JSON"}}
			u := store.BotUsage{}
			if tc.alter != nil {
				tc.alter(w, &r, &u)
			}
			opts := EmployeeMessageOptions{Work: w, Usage: &u}
			if tc.name == "unknown-usage" {
				opts.Usage = nil
			}
			m := ComposeEmployeeMessage(context.Background(), store.RunPartial, r, nil, opts)
			if got := strings.Contains(m.Text, "No result was produced"); got != tc.noResult {
				t.Fatalf("no-result=%v want %v: %s", got, tc.noResult, m.Text)
			}
			if tc.noResult && (!strings.Contains(m.Text, "couldn't start the requested work") || strings.Contains(m.Text, "incomplete result") || m.Reason != store.BotEmployeeReasonSetup) {
				t.Fatalf("misleading preparation message: %+v", m)
			}
			if strings.Contains(m.Text, "PRIVATE") {
				t.Fatal(m.Text)
			}
			r.EmployeeMessage = &m
			if err := r.ValidateEmployeeMessageIntegrity(store.RunPartial); err != nil {
				t.Fatal(err)
			}
		})
	}
}
