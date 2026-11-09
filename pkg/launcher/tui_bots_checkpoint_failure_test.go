package launcher

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

// The canonical backend fixture exercises the terminal's real mapper. No second
// terminal-specific paragraph or synthetic error event is substituted here.
func TestBotCheckpointFailureMapperRenderLiveAndReplay(t *testing.T) {
	raw, err := os.ReadFile("../api/testdata/bot_checkpoint_failure.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Event           json.RawMessage `json:"event"`
		ExpectedReports int             `json:"expected_reports"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	frame := client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(fixture.Event)}
	var live *events.Transcript
	for _, mode := range []string{"live", "restored"} {
		t.Run(mode, func(t *testing.T) {
			mapper := botTranscriptMapper{}
			transcript := events.NewTranscript()
			// Reconnect repeats the last frame; identity, never paragraph equality,
			// must prevent a second report.
			for range 2 {
				mapped, err := mapper.mapFrame(frame)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range mapped {
					if event.Kind != events.KindBotReport {
						t.Fatalf("unexpected mapped event: %+v", event)
					}
					transcript.Apply(event)
				}
			}
			if len(transcript.Items) != fixture.ExpectedReports {
				t.Fatalf("items: %+v", transcript.Items)
			}
			item := transcript.Items[0]
			if item.Kind != events.ItemAgent || item.Provisional {
				t.Fatalf("not a normal finalized report: %+v", item)
			}
			for _, width := range []int{80, 120} {
				text := strings.Join(strings.Fields(render.Markdown(item.Content, width, render.Styles{NoColor: true})), " ")
				for _, fact := range []string{"Run report", "failed", "Model response could not be processed", "multiple tool requests in one response", "one tool request at a time", "None of the tool requests from that response were executed", "Earlier work may already have completed", "No approval is needed", "has not been automatically retried", "Technical details", "count=2"} {
					if !strings.Contains(text, fact) {
						t.Errorf("width %d missing %q: %s", width, fact, text)
					}
				}
				if width == 120 {
					t.Logf("%s: report_items=1 raw_errors=0 no_approval=true count=2\n%s", mode, text)
				}
				if strings.Count(text, "Run report") != 1 || strings.Contains(text, "private-rejected-argument") || strings.Contains(text, "[Error:") || strings.Contains(text, "Run did not complete; execution requires review") {
					t.Fatal(text)
				}
			}
			if mode == "live" {
				live = transcript
			} else if !reflect.DeepEqual(live.Items, transcript.Items) {
				t.Fatal("live/replay diverged")
			}
		})
	}
}
