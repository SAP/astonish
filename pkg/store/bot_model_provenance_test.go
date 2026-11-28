package store

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBotRunCommitModelSelectionLegacyEncoding(t *testing.T) {
	old := struct {
		Lease      BotLease
		Status     RunStatus
		Report     BotReport
		Usage      BotUsage
		Todos      []BotTodo
		Checkpoint json.RawMessage
		Activity   []BotActivity
		Outbox     []BotOutboxEntry
	}{Status: RunFailed, Report: BotReport{Summary: "Failed"}}
	want, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(BotRunCommit{Status: old.Status, Report: old.Report})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy commit hash input changed:\n%s\n%s", got, want)
	}
}
