package launcher

import (
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"testing"
)

func TestBotHomeNarrativeTerminalBeforeFinal(t *testing.T) {
	const ack = "I'll prepare a concise weekly AI roundup."
	frames := []client.SSEEvent{
		runtimeTranscriptFrame(t, "user_message", `{"text":"Weekly roundup"}`, 1),
		runtimeTranscriptFrame(t, "text", `{"text":"I'll prepare a concise weekly AI roundup."}`, 2),
		runtimeTranscriptFrame(t, "turn_complete", `{}`, 3),
		runtimeTranscriptFrame(t, "cursor", `{}`, 4),
	}
	for _, mode := range []string{"live", "reload"} {
		t.Run(mode, func(t *testing.T) {
			m := botTranscriptMapper{}
			tr := events.NewTranscript()
			for _, f := range frames {
				mapped, err := m.mapFrame(f)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range mapped {
					tr.Apply(e)
				}
			}
			if len(tr.Items) != 2 || tr.Items[1].Kind != events.ItemAgent || tr.Items[1].Content != ack || tr.Items[1].Provisional {
				t.Fatalf("ack hidden/tool leaked: %+v", tr.Items)
			}
		})
	}
}
