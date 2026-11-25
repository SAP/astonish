package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/api"
	"reflect"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

// runtimeTranscriptFrame is the public canonical Chat frame, not a storage row.
func runtimeTranscriptFrame(t *testing.T, typ, payload string, sequence int64) client.SSEEvent {
	t.Helper()
	return canonicalBotFrame(t, "11111111-1111-1111-1111-111111111111", fmt.Sprintf("runtime/%d", sequence), typ, payload, sequence)
}

func canonicalBotFrame(t *testing.T, sessionID, eventID, typ, payload string, cursor int64) client.SSEEvent {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%s:%d", sessionID, cursor)
	data["_projection"] = map[string]any{"session_id": sessionID, "event_id": eventID, "cursor": id}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return client.SSEEvent{Type: typ, ID: id, Data: string(raw)}
}

func TestBotTranscriptTurnContextMarker(t *testing.T) {
	mapper := botTranscriptMapper{}
	for _, typ := range []string{"cursor", "replay_complete"} {
		got, err := mapper.mapFrame(client.SSEEvent{Type: typ, Data: `{}`})
		if err != nil || len(got) != 0 {
			t.Fatalf("control frame rendered: %v %v", got, err)
		}
	}
	got, err := mapper.mapFrame(runtimeTranscriptFrame(t, "user_message", `{"text":"[Astonish Per-Turn Context — not user-authored]"}`, 1))
	if err != nil || len(got) != 1 || got[0].Kind != events.KindUser {
		t.Fatalf("user quote lost: %v %v", got, err)
	}
	for _, frame := range []client.SSEEvent{{Type: "transcript", Data: `{"author":"user","content":{"parts":[{"text":"private"}]}}`}, {Type: "text", Data: `{"text":"unbound"}`}, {Type: "text", Data: `{`}} {
		if _, err := mapper.mapFrame(frame); err == nil {
			t.Fatalf("accepted noncanonical frame: %+v", frame)
		}
	}
}

func mapStoredBotFixture(t *testing.T, mapper *botTranscriptMapper, frame client.SSEEvent) ([]events.Event, error) {
	t.Helper()
	var row session.Event
	if err := json.Unmarshal([]byte(frame.Data), &row); err != nil {
		return nil, err
	}
	if row.ID == "" {
		row.ID = uuid.NewString()
	}
	sid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	projector := api.SessionEventProjector{BotID: uuid.New(), SessionID: sid, HomeSessionID: sid}
	projection, err := projector.ProjectBot(context.Background(), store.BotTranscriptEvent{Cursor: 1, Event: &row}, nil)
	if err != nil {
		return nil, err
	}
	frames, err := projection.BotFrames(0)
	if err != nil {
		return nil, err
	}
	var out []events.Event
	for _, frame := range frames {
		mapped, err := mapper.mapFrame(client.SSEEvent{Type: frame.Type, Data: string(frame.Data)})
		if err != nil {
			return nil, err
		}
		out = append(out, mapped...)
	}
	return out, nil
}

func TestBotCanonicalReviewIsObservation(t *testing.T) {
	m := botTranscriptMapper{}
	frame := runtimeTranscriptFrame(t, "bot_review", `{"reviewId":"review","eventId":"saved","invocationId":"turn","question":{"text":"Which city?"}}`, 1)
	got, err := m.mapFrame(frame)
	if err != nil || len(got) != 1 || got[0].Kind != events.KindSystem || got[0].EventID != "saved" || got[0].InvocationID != "turn" {
		t.Fatalf("review: %+v %v", got, err)
	}
	got, err = m.mapFrame(frame)
	if err != nil || len(got) != 0 {
		t.Fatalf("duplicate review: %+v %v", got, err)
	}
}

func TestBotRuntimeArtifactEnvelope(t *testing.T) {
	frame := runtimeTranscriptFrame(t, "artifact", `{"path":"/workspace/report.md","fileName":"report.md","fileType":"text/markdown","toolName":"write_file","isReport":true,"reportTitle":"Findings"}`, 1)
	got, err := botTranscriptEvents(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != events.KindArtifact || got[0].Artifact == nil || got[0].Artifact.ReportTitle != "Findings" || got[0].Artifact.ToolName != "write_file" {
		t.Fatalf("production artifact envelope lost: %+v", got)
	}
}

func TestBotStudioHashRoutes(t *testing.T) {
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig("http://localhost:9393")}, "team")
	if got := b.BotStudioURL(); got != "http://localhost:9393/#/chat" {
		t.Fatal(got)
	}
	b.selected = &store.Bot{ID: uuid.New()}
	if got := b.BotStudioURL(); got != "http://localhost:9393/#/chat/bots/team/"+b.selected.ID.String() {
		t.Fatal(got)
	}
}

func TestBotRuntimeProductionCapture(t *testing.T) {
	f := newBotFactoryFixture(t)
	capture := &botWireCaptureSessions{Service: f.sessions}
	f.sessions = capture
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(_ context.Context, wire bot.RuntimeEvent) error {
		// Exactly the daemon's storage envelope; capture it in the same ordered
		// journal as actual ADK appends, not a hand-reordered wire-only fixture.
		capture.record(&session.Event{ID: uuid.NewSHA1(wire.TurnID, []byte(fmt.Sprint(wire.Sequence))).String(), InvocationID: wire.TurnID.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": wire}}})
		return nil
	})
	if err != nil || result.Status != store.RunSucceeded {
		t.Fatalf("run %+v: %v", result, err)
	}
	mapper := botTranscriptMapper{}
	texts, users, adkModels := 0, 0, 0
	projector := api.SessionEventProjector{BotID: f.req.Bot.ID, HomeSessionID: f.req.Bot.HomeSessionID, SessionID: f.req.Run.SessionID}
	for i, row := range capture.rows {
		if row.Author != "bot_runtime" && row.Content != nil && row.Content.Role == "model" {
			adkModels++
		}
		if row.ID == "" {
			row.ID = uuid.NewString()
		}
		projection, err := projector.ProjectBot(f.ctx, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: row}, nil)
		if err != nil {
			t.Fatal(err)
		}
		frames, err := projection.BotFrames(int64(i))
		if err != nil {
			t.Fatal(err)
		}
		var got []events.Event
		for _, frame := range frames {
			mapped, err := mapper.mapFrame(client.SSEEvent{Type: frame.Type, Data: string(frame.Data)})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, mapped...)
		}
		for _, ev := range got {
			if ev.Kind == events.KindText {
				texts++
				if ev.Text != "controlled result" {
					t.Fatal(ev.Text)
				}
			}
			if ev.Kind == events.KindUser {
				users++
			}
		}
	}
	if texts != 1 || users != 1 || adkModels == 0 {
		t.Fatalf("text=%d users=%d actual ADK models=%d", texts, users, adkModels)
	}
}

type botWireCaptureSessions struct {
	session.Service
	mu   sync.Mutex
	rows []*session.Event
}

func (s *botWireCaptureSessions) record(row *session.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
}
func (s *botWireCaptureSessions) AppendEvent(ctx context.Context, sess session.Session, row *session.Event) error {
	if err := s.Service.AppendEvent(ctx, sess, row); err != nil {
		return err
	}
	s.record(row)
	return nil
}

func TestBotRuntimeRepeatedChunksAndApprovalSafety(t *testing.T) {
	mapper := botTranscriptMapper{}
	textCount := 0
	for _, sequence := range []int64{1, 2, 2, 3} {
		got, err := mapper.mapFrame(runtimeTranscriptFrame(t, "text", `{"text":"same"}`, sequence))
		if err != nil {
			t.Fatal(err)
		}
		textCount += len(got)
	}
	if textCount != 3 {
		t.Fatalf("legitimate identical chunks lost or retry repeated: %d", textCount)
	}
	for _, typ := range []string{"approval", "plan_approval", "auto_approved"} {
		got, err := botTranscriptEvents(runtimeTranscriptFrame(t, typ, `{"name":"shell_command","args":{"command":"danger"}}`, 5))
		if err != nil || len(got) != 0 {
			t.Fatalf("replay became decision: %s %+v %v", typ, got, err)
		}
	}
}

func TestBotRuntimeEnvelopeChatContracts(t *testing.T) {
	for _, tc := range []struct{ typ, payload string }{
		{"thinking", `{"text":"Waiting for model"}`},
		{"report_marker", `{"path":"report.md","title":"Findings"}`},
		{"delegation", `{"type":"task_tool_call","batch_id":"b","task_id":"t","child_session_id":"child","sequence":4,"tool_name":"read_file"}`},
		{"usage", `{"input_tokens":3,"output_tokens":2,"total_tokens":5}`},
		{"error", `{"error":"controlled failure"}`},
		{"done", `{"done":true}`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			got, err := botTranscriptEvents(runtimeTranscriptFrame(t, tc.typ, tc.payload, 1))
			want := mapSSEToEvents(&client.SSEEvent{Type: tc.typ, Data: tc.payload}, false)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v err %v want %+v", got, err, want)
			}
		})
	}
}
