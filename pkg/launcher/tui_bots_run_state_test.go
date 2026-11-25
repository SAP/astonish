package launcher

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"testing"
)

func TestBotRunStateDoesNotEnableRuntimeProjection(t *testing.T) {
	m := botTranscriptMapper{}
	got, err := m.mapFrame(runtimeTranscriptFrame(t, store.BotRunStateKey, `{"schema_version":1,"work":{"schema_version":1,"protocol":"agent_led_v1","work_revision":3,"disposition":"partial"}}`, 1))
	if err != nil || len(got) != 1 || got[0].Kind != events.KindBotRunState {
		t.Fatalf("state: %+v %v", got, err)
	}
	if got[0].BotRunState.Work == nil || got[0].BotRunState.Work.WorkRevision != 3 {
		t.Fatalf("lost work: %+v", got)
	}
	got, err = m.mapFrame(runtimeTranscriptFrame(t, "text", `{"text":"Useful draft"}`, 2))
	if err != nil || len(got) != 1 || got[0].Text != "Useful draft" {
		t.Fatalf("text: %+v %v", got, err)
	}
}
