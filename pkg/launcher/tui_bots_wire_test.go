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

// Same wire shape as daemon bot_worker.persistRuntimeEvent: the complete Chat
// payload is in StateDelta, not ArtifactDelta or Content.
func runtimeTranscriptFrame(t *testing.T, typ, payload string, sequence int64) client.SSEEvent {
	t.Helper()
	wire := bot.RuntimeEvent{SessionID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), TurnID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Sequence: sequence, Type: typ, Payload: json.RawMessage(payload)}
	row := session.Event{ID: uuid.NewString(), InvocationID: wire.TurnID.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": wire}}}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(data)}
}

func TestBotTranscriptTurnContextMarker(t *testing.T) {
	const quote = "[Astonish Per-Turn Context — not user-authored]\n## Relevant Tools For This Request\nannounce_plan / delegate_tasks"
	for _, camel := range []bool{false, true} {
		for _, runtime := range []bool{false, true} {
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("camel=%t/runtime=%t/legacy=%t", camel, runtime, legacy), func(t *testing.T) {
					mapper := botTranscriptMapper{}
					mapRow := mapper.mapFrame
					if legacy {
						mapRow = botTranscriptEvents
					}
					if runtime {
						if _, err := mapRow(runtimeTranscriptFrame(t, "session", `{}`, 1)); err != nil {
							t.Fatal(err)
						}
					}
					for _, tc := range []struct {
						name, text string
						marker     any
						hidden     bool
					}{
						{"real", "real input", nil, false},
						{"hidden", quote, true, true},
						{"quote", quote, nil, false},
						{"false", quote, false, false},
						{"string", quote, "true", false},
						{"equal quote", quote, nil, false},
					} {
						t.Run(tc.name, func(t *testing.T) {
							delta := map[string]any{}
							if tc.marker != nil {
								delta["_astonish_turn_context"] = tc.marker
							}
							content := map[string]any{"role": "user", "parts": []any{map[string]any{"text": tc.text}}}
							row := map[string]any{"Author": "user", "InvocationID": "22222222-2222-2222-2222-222222222222", "Content": content, "Actions": map[string]any{"StateDelta": delta}}
							if camel {
								row = map[string]any{"author": "user", "invocationId": "22222222-2222-2222-2222-222222222222", "content": content, "actions": map[string]any{"stateDelta": delta}}
							}
							raw, err := json.Marshal(row)
							if err != nil {
								t.Fatal(err)
							}
							got, err := mapRow(client.SSEEvent{Type: client.BotTranscriptEvent, Data: string(raw)})
							if err != nil {
								t.Fatal(err)
							}
							if tc.hidden {
								if len(got) != 0 {
									t.Fatalf("hidden context emitted events (including completion): %+v", got)
								}
							} else if len(got) != 2 || got[0].Kind != events.KindUser || got[0].Text != tc.text || got[1].Kind != events.KindDone {
								t.Fatalf("real user input lost: %+v", got)
							}
						})
					}
				})
			}
		}
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
		capture.record(&session.Event{ID: uuid.NewSHA1(wire.TurnID, []byte(fmt.Sprint(wire.Sequence))).String(), InvocationID: wire.TurnID.String(), Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{botRuntimeStateKey: wire}}})
		return nil
	})
	if err != nil || result.Status != store.RunSucceeded {
		t.Fatalf("run %+v: %v", result, err)
	}
	mapper := botTranscriptMapper{}
	texts, users, adkModels := 0, 0, 0
	for _, row := range capture.rows {
		if row.Author != "bot_runtime" && row.Content != nil && row.Content.Role == "model" {
			adkModels++
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		got, err := mapper.mapFrame(client.SSEEvent{Data: string(raw)})
		if err != nil {
			t.Fatal(err)
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
