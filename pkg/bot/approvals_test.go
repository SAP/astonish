package bot

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestApprovalCheckpointExactBinding(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 2}
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: json.RawMessage(`{"body":"exact","token":"{{CREDENTIAL:mail:token}}"}`)}
	digest, err := action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	a := store.BotApproval{ID: uuid.New(), BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest}
	c := ApprovalCheckpoint{Format: ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: 1}
	raw, _ := json.Marshal(c)
	decoded, err := DecodeApprovalCheckpoint(raw, run)
	if err != nil || !decoded.Matches(a) {
		t.Fatal("valid binding rejected", err)
	}
	for _, name := range []string{"bot", "run", "session", "version", "format", "fence", "digest", "unknown", "trailing"} {
		t.Run(name, func(t *testing.T) {
			bad := c
			switch name {
			case "bot":
				bad.BotID = uuid.New()
			case "run":
				bad.RunID = uuid.New()
			case "session":
				bad.SessionID = uuid.New()
			case "version":
				bad.Version++
			case "format":
				bad.Format = "arbitrary"
			case "fence":
				bad.Fence = 0
			case "digest":
				bad.ActionDigest = ""
			}
			b, _ := json.Marshal(bad)
			if name == "unknown" {
				b = append(b[:len(b)-1], []byte(`,"authority":true}`)...)
			}
			if name == "trailing" {
				b = append(b, []byte(` {}`)...)
			}
			if _, err := DecodeApprovalCheckpoint(b, run); err == nil {
				t.Fatal("accepted invalid checkpoint")
			}
		})
	}
	for _, name := range []string{"approval", "recipient", "tool", "arguments", "version", "digest"} {
		t.Run(name, func(t *testing.T) {
			bad := a
			switch name {
			case "approval":
				bad.ID = uuid.New()
			case "recipient":
				bad.Action.Recipient = "mallory"
			case "tool":
				bad.Action.Tool = "shell"
			case "arguments":
				bad.Action.Arguments = json.RawMessage(`{"body":"changed"}`)
			case "version":
				bad.Version++
			case "digest":
				bad.ActionDigest = "other"
			}
			if c.Matches(bad) {
				t.Fatal("accepted changed action")
			}
		})
	}
}
