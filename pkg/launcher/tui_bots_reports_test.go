package launcher

import (
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui/events"
	"reflect"
	"testing"
)

func reportTranscriptFrame(t *testing.T, invocation, id, text string) client.SSEEvent {
	t.Helper()
	return canonicalBotFrame(t, "11111111-1111-1111-1111-111111111111", id, "text", fmt.Sprintf(`{"text":%q,"invocationId":%q,"eventId":%q}`, text, invocation, id), 1)
}

func TestBotScheduledReportsMapperLiveAndReplay(t *testing.T) {
	journal := []client.SSEEvent{
		runtimeTranscriptFrame(t, "tool_call", `{"name":"schedule_job","id":"s","args":{}}`, 1),
		runtimeTranscriptFrame(t, "tool_result", `{"name":"schedule_job","id":"s","result":"created"}`, 2),
		runtimeTranscriptFrame(t, "text", `{"text":"Scheduled."}`, 3),
		runtimeTranscriptFrame(t, "turn_complete", `{}`, 4),
	}
	for i := 0; i < 3; i++ {
		journal = append(journal, reportTranscriptFrame(t, "bot-report:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", fmt.Sprintf("report/%d", i), "Same report."), runtimeTranscriptFrame(t, "turn_complete", `{}`, int64(10+i)))
	}
	apply := func(m *botTranscriptMapper, tr *events.Transcript, f client.SSEEvent) {
		t.Helper()
		mapped, err := m.mapFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range mapped {
			tr.Apply(e)
		}
	}
	live, restored := events.NewTranscript(), events.NewTranscript()
	m, r := botTranscriptMapper{}, botTranscriptMapper{}
	for _, frame := range journal {
		apply(&m, live, frame)
		apply(&r, restored, frame)
	}
	for _, frame := range journal {
		apply(&m, live, frame)
	}
	if !reflect.DeepEqual(live.Items, restored.Items) {
		t.Fatalf("live/replay mismatch: %+v / %+v", live.Items, restored.Items)
	}
	var texts []string
	for _, item := range live.Items {
		if item.Kind == events.ItemAgent {
			texts = append(texts, item.Content)
		}
	}
	if !reflect.DeepEqual(texts, []string{"Scheduled.", "Same report.", "Same report.", "Same report."}) {
		t.Fatalf("reports merged or lost: %q", texts)
	}
}

func TestBotRuntimeIdentityNotInvocationOrCursor(t *testing.T) {
	m := botTranscriptMapper{}
	a := reportTranscriptFrame(t, "same-turn", "a", "same")
	b := reportTranscriptFrame(t, "same-turn", "b", "same")
	for _, f := range []client.SSEEvent{a, b} {
		got, err := m.mapFrame(f)
		if err != nil || len(got) != 1 {
			t.Fatalf("distinct identity lost: %+v %v", got, err)
		}
	}
	a.ID = "11111111-1111-1111-1111-111111111111:99"
	got, err := m.mapFrame(a)
	if err != nil || len(got) != 0 {
		t.Fatalf("identity repeated at new cursor: %+v %v", got, err)
	}
}
