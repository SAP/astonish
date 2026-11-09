package launcher

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func reportTranscriptFrame(t *testing.T, invocation, id, text string) client.SSEEvent {
	t.Helper()
	row := session.Event{ID: id, InvocationID: invocation, Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}}}}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(raw)}
}

func TestBotScheduledReportsMapperLiveAndReplay(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			journal := []client.SSEEvent{
				runtimeTranscriptFrame(t, "session", `{}`, 1),
				runtimeTranscriptFrame(t, "tool_call", `{"name":"schedule_job","id":"s","args":{}}`, 2),
				runtimeTranscriptFrame(t, "tool_result", `{"name":"schedule_job","id":"s","result":"created"}`, 3),
				runtimeTranscriptFrame(t, "text", `{"text":"Scheduled."}`, 4),
				runtimeTranscriptFrame(t, "turn_complete", `{}`, 5),
				reportTranscriptFrame(t, "22222222-2222-2222-2222-222222222222", "raw-reply", "Scheduled."),
			}
			mapper := botTranscriptMapper{}
			live := events.NewTranscript()
			apply := func(m *botTranscriptMapper, tr *events.Transcript, f client.SSEEvent) {
				t.Helper()
				mapped, err := m.mapFrame(f)
				if err != nil {
					t.Fatal(err)
				}
				for _, ev := range mapped {
					tr.Apply(ev)
				}
			}
			for _, frame := range journal {
				apply(&mapper, live, frame)
			}
			want := []string{"Scheduled."}
			for i := 1; i <= 3; i++ {
				body := fmt.Sprintf("Report %d.", i)
				if identical {
					body = "Same report."
				}
				id := fmt.Sprintf("bot-report:00000000-0000-0000-0000-%012d", i)
				frame := reportTranscriptFrame(t, id, fmt.Sprintf("row-%d", i), body)
				journal = append(journal, frame)
				apply(&mapper, live, frame)
				want = append(want, body)
				restored := events.NewTranscript()
				replay := botTranscriptMapper{}
				for _, f := range journal {
					apply(&replay, restored, f)
				}
				// An old retransmitted row cannot append another body or move chronology.
				apply(&mapper, live, journal[6])
				if !reflect.DeepEqual(live.Items, restored.Items) {
					t.Fatalf("live/replay differ: %+v / %+v", live.Items, restored.Items)
				}
				var got []string
				for _, item := range live.Items {
					if item.Kind == events.ItemAgent {
						got = append(got, item.Content)
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %q want %q", got, want)
				}
				item := live.Items[len(live.Items)-1]
				if item.InvocationID != id || item.EventID != fmt.Sprintf("row-%d", i) || item.Provisional {
					t.Fatalf("lost identity/boundary: %+v", item)
				}
			}
			for _, f := range []client.SSEEvent{runtimeTranscriptFrame(t, "tool_call", `{"name":"read_file","id":"later"}`, 6), runtimeTranscriptFrame(t, "tool_result", `{"name":"read_file","id":"later","result":"ok"}`, 7), runtimeTranscriptFrame(t, "text", `{"text":"Later."}`, 8), runtimeTranscriptFrame(t, "turn_complete", `{}`, 9)} {
				apply(&mapper, live, f)
			}
			var got []string
			for _, item := range live.Items {
				if item.Kind == events.ItemAgent {
					got = append(got, item.Content)
				}
			}
			if !reflect.DeepEqual(got, append(want, "Later.")) {
				t.Fatalf("later tool overwrote reports: %q", got)
			}
		})
	}
}

func TestBotRuntimeSuppressionIsInvocationScoped(t *testing.T) {
	for _, kind := range []string{"session", "text", "bot_review", "turn_complete"} {
		t.Run(kind, func(t *testing.T) {
			mapper := botTranscriptMapper{}
			if _, err := mapper.mapFrame(runtimeTranscriptFrame(t, kind, `{}`, 1)); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"22222222-2222-2222-2222-222222222222", "legacy-other", ""} {
				got, err := mapper.mapFrame(reportTranscriptFrame(t, id, "raw-"+id, "ordinary"))
				if err != nil {
					t.Fatal(err)
				}
				suppressed := ((kind == "session" || kind == "text") && id == "22222222-2222-2222-2222-222222222222") || (kind == "session" && id == "")
				if (len(got) == 0) != suppressed {
					t.Fatalf("kind %s invocation %q: %+v", kind, id, got)
				}
			}
		})
	}
}

func TestBotRuntimeInvocationBindingWindow(t *testing.T) {
	mapper := botTranscriptMapper{}
	mapRow := func(frame client.SSEEvent) []events.Event {
		t.Helper()
		got, err := mapper.mapFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	mapRow(runtimeTranscriptFrame(t, "session", `{}`, 1))
	user := reportTranscriptFrame(t, "adk-invocation", "user-row", "ask")
	var row session.Event
	if err := json.Unmarshal([]byte(user.Data), &row); err != nil {
		t.Fatal(err)
	}
	row.Author = "user"
	row.Content.Role = "user"
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	user.Data = string(raw)
	mapRow(user)
	if got := mapRow(reportTranscriptFrame(t, "adk-invocation", "raw", "answer")); len(got) != 0 {
		t.Fatalf("ADK alias duplicated: %+v", got)
	}
	// A real publication during runtime is independent of the active turn.
	if got := mapRow(reportTranscriptFrame(t, "bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "report", "same")); len(got) != 1 || got[0].Kind != events.KindBotReport {
		t.Fatalf("active runtime hid publication: %+v", got)
	}
	mapRow(runtimeTranscriptFrame(t, "done", `{}`, 2))
	for _, id := range []string{"legacy-next", ""} {
		if got := mapRow(reportTranscriptFrame(t, id, "next", "legacy")); len(got) == 0 {
			t.Fatalf("completed runtime suppressed %q", id)
		}
	}
	// Already-bound old rows still dedup even after another turn completes.
	if got := mapRow(reportTranscriptFrame(t, "adk-invocation", "raw", "answer")); len(got) != 0 {
		t.Fatal("old ADK alias lost")
	}
	mapRow(runtimeTranscriptFrame(t, "text", `{"invocationId":"explicit-alias","text":"wire"}`, 3))
	if got := mapRow(reportTranscriptFrame(t, "explicit-alias", "raw2", "wire")); len(got) != 0 {
		t.Fatal("payload invocation alias ignored")
	}
}

func TestBotScheduledReportMappingIdentityControls(t *testing.T) {
	for _, id := range []string{"bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "bot-report:run", "bot-report:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", ""} {
		got, err := botTranscriptEvents(reportTranscriptFrame(t, id, "stable-row", "# Scheduled report"))
		if err != nil {
			t.Fatal(err)
		}
		valid := id == "bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		if len(got) == 0 || (got[0].Kind == events.KindBotReport) != valid {
			t.Fatalf("%q: %+v", id, got)
		}
		if valid && (len(got) != 1 || got[0].EventID != "stable-row" || got[0].InvocationID != id) {
			t.Fatalf("identity: %+v", got)
		}
	}
}
