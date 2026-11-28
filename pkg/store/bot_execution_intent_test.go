package store

import (
	"encoding/json"
	"testing"
)

func TestBotExecutionIntentLegacyAndStatus(t *testing.T) {
	var old BotRun
	if err := json.Unmarshal([]byte(`{"status":"queued"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Intent != BotIntentRegular {
		t.Fatal("legacy run gained authority")
	}
	for _, intent := range []BotExecutionIntent{BotIntentRegular, BotIntentRegular, BotIntentConversation, BotExecutionIntent("unknown")} {
		for _, status := range []BotStatus{BotStatus("draft"), BotActive, BotPaused, BotStopped, BotDeleted, BotStatus("")} {
			want := intent == BotIntentRegular && status == BotActive || intent == BotIntentConversation && status == BotPaused
			if intent.AllowsStatus(status) != want {
				t.Fatalf("%q in %q: want %v", intent, status, want)
			}
		}
	}
}
