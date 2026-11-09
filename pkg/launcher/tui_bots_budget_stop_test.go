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

// The same canonical backend fixture exercises Studio's real mapper. No second
// terminal-specific paragraph or synthetic error event is substituted here.
func TestBotBudgetStopMapperRenderLiveAndReplay(t *testing.T) {
	raw, err := os.ReadFile("../api/testdata/bot_budget_stop.json")
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
				for _, fact := range []string{"Run report", "failed", "Budget limit reached", "run budget: 71 tokens", "Used or reserved: 0 tokens", "Remaining: 71 tokens", "Estimated next input: 80 tokens", "increase this Bot’s token budget", "Technical details"} {
					if !strings.Contains(text, fact) {
						t.Errorf("width %d missing %q: %s", width, fact, text)
					}
				}
				if strings.Count(text, "Run report") != 1 || strings.Contains(text, "[Error:") || strings.Contains(text, "Run did not complete; execution requires review") {
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
