package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestPauseFreshInstructions(t *testing.T) {
	cp := smokeCheckpoint{}
	message := pauseTaskInstructions(cp)
	for _, required := range []string{"load_file exactly once", "path /root/chat-first-proof.txt", "title Approval check", "No writes"} {
		if !strings.Contains(message, required) {
			t.Fatalf("missing %q", required)
		}
	}
	if strings.Contains(message, "previous") || strings.Contains(message, "Replace") {
		t.Fatal("fresh task assumes historical refusal")
	}
	cp.IDs = map[string]string{"pause-task": "retained"}
	if !strings.Contains(pauseTaskInstructions(cp), "previous task finished without tools") {
		t.Fatal("replacement lost historical safety context")
	}
}

func TestPauseReadonlyExactAction(t *testing.T) {
	for _, tc := range []struct {
		name, tool, path, title string
		extra, valid            bool
	}{
		{"exact", "load_file", retainedArtifactPath, pauseReadonlyTitle, false, true},
		{"write", "write_file", retainedArtifactPath, pauseReadonlyTitle, false, false},
		{"path", "load_file", "/root/other", pauseReadonlyTitle, false, false},
		{"title", "load_file", retainedArtifactPath, "other", false, false},
		{"extra", "load_file", retainedArtifactPath, pauseReadonlyTitle, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"path": tc.path, "title": tc.title}
			if tc.extra {
				args["content"] = "bad"
			}
			raw, _ := json.Marshal(map[string]any{"action": map[string]any{"tool": tc.tool, "arguments": args}, "action_digest": "exact"})
			var a store.BotApproval
			if err := json.Unmarshal(raw, &a); err != nil {
				t.Fatal(err)
			}
			if err := validatePauseAction(a); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
func TestPauseOriginalPreservesHistory(t *testing.T) {
	id := uuid.New().String()
	r := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, IdempotencyKey: "control-proposal:" + id}
	cp := smokeCheckpoint{IDs: map[string]string{"pause-task": id}, Sent: map[string]bool{"pause-task": true}, Consents: map[string]string{id: "original"}}
	before, _ := json.Marshal(cp)
	if _, err := validatePauseOriginal(cp, []store.BotRun{r}, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(cp)
	if string(before) != string(after) {
		t.Fatal("history modified")
	}
	if _, err := validatePauseOriginal(cp, []store.BotRun{r}, []store.BotApproval{{RunID: r.ID}}); err == nil {
		t.Fatal("accepted original approval")
	}
	r.Status = store.RunWaitingApproval
	if _, err := validatePauseOriginal(cp, []store.BotRun{r}, nil); err == nil {
		t.Fatal("accepted nonterminal original")
	}
	r.Status = store.RunSucceeded
	r.Usage.ToolCalls = 1
	if _, err := validatePauseOriginal(cp, []store.BotRun{r}, nil); err == nil {
		t.Fatal("accepted executed original")
	}
}
