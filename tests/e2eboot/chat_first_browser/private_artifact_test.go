package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestPrivateLoadClarificationPreservesCheckpoint(t *testing.T) {
	now := time.Now()
	w := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, FinishedAt: &now}
	r := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, Intent: store.BotIntentConversation, CreatedAt: now.Add(time.Second)}
	r.Report.Summary = "Cannot use load_file to publish externally"
	cp := smokeCheckpoint{Candidate: "unchanged", Sent: map[string]bool{"artifact-load": true}, Before: map[string]map[string]bool{"artifact-load": {}}, IDs: map[string]string{"artifact-write-run": w.ID.String()}, Consents: map[string]string{"consumed": "exact"}}
	before, _ := json.Marshal(cp)
	if got, err := loadRefusal(cp, []store.BotRun{w, r}, nil); err != nil || got.ID != r.ID {
		t.Fatalf("%v %v", got, err)
	}
	after, _ := json.Marshal(cp)
	if string(before) != string(after) {
		t.Fatal("checkpoint changed")
	}
	for _, rs := range [][]store.BotRun{{w}, {w, r, r}} {
		if _, err := loadRefusal(cp, rs, nil); err == nil {
			t.Fatal("ambiguous refusal accepted")
		}
	}
	bad := r
	bad.Status = store.RunPartial
	if _, err := loadRefusal(cp, []store.BotRun{w, bad}, nil); err == nil {
		t.Fatal("partial accepted")
	}
	bad = r
	bad.Usage.ToolCalls = 1
	if _, err := loadRefusal(cp, []store.BotRun{w, bad}, nil); err == nil {
		t.Fatal("action-bearing run accepted")
	}
	if _, err := loadRefusal(cp, []store.BotRun{w, r}, []store.BotControlProposal{{ID: uuid.New()}}); err == nil {
		t.Fatal("proposal absence fabricated")
	}
}
