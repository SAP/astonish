package launcher

import (
	"github.com/SAP/astonish/pkg/tui/events"
	"testing"
)

func TestBotTranscriptReasoningUsage(t *testing.T) {
	for _, payload := range []string{`{"input":10,"output":4096,"total":4106}`, `{"input_tokens":10,"output_tokens":4096,"total_tokens":4106}`} {
		got, err := botTranscriptEvents(runtimeTranscriptFrame(t, "usage", payload, 1))
		if err != nil || len(got) != 1 || got[0].Kind != events.KindUsage || got[0].Usage.Input != 10 || got[0].Usage.Output != 4096 || got[0].Usage.Total != 4106 {
			t.Fatalf("usage: %+v %v", got, err)
		}
	}
}
