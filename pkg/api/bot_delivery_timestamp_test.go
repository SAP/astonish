package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotFramesDeliveryTimestamp(t *testing.T) {
	stamp := time.Date(2026, 11, 27, 12, 34, 56, 123000000, time.FixedZone("saved", -5*60*60))
	for _, tc := range []struct {
		name  string
		stamp time.Time
		spoof any
	}{
		{"saved", stamp, nil},
		{"saved overrides payload", stamp, "2099-01-01T00:00:00Z"},
		{"legacy unknown", time.Time{}, nil},
		{"unknown discards payload", time.Time{}, "2099-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{"text": "saved report", "invocationId": "bot-report:saved"}
			if tc.spoof != nil {
				data["timestamp"] = tc.spoof
			}
			before := make(map[string]any, len(data))
			for key, value := range data {
				before[key] = value
			}
			p := SessionEventProjection{SessionID: uuid.New(), Cursor: 8, Events: []ChatEvent{
				{ID: "saved/0", Type: "text", Data: data, Timestamp: tc.stamp},
				{ID: "saved/1", Type: "text", Data: data, Timestamp: tc.stamp},
			}}
			frames, err := p.BotFrames(7)
			if err != nil || len(frames) != 2 {
				t.Fatalf("frames: %+v %v", frames, err)
			}
			for i, frame := range frames {
				var payload map[string]any
				if err := json.Unmarshal(frame.Data, &payload); err != nil {
					t.Fatal(err)
				}
				got, exists := payload["timestamp"]
				if tc.stamp.IsZero() {
					if exists {
						t.Fatalf("unknown time invented: %v", got)
					}
				} else if got != tc.stamp.Format(time.RFC3339Nano) {
					t.Fatalf("delivery timestamp = %v, want %s", got, tc.stamp.Format(time.RFC3339Nano))
				}
				meta := payload["_projection"].(map[string]any)
				if meta["event_id"] != p.Events[i].ID || frame.Cursor != int64(7+i) || meta["cursor"] != botEventCursor(p.SessionID, frame.Cursor) {
					t.Fatalf("identity/cursor changed: %+v", frame)
				}
			}
			if !reflect.DeepEqual(data, before) {
				t.Fatalf("durable payload mutated: %+v != %+v", data, before)
			}
			replayed, err := p.BotFrames(7)
			if err != nil || !reflect.DeepEqual(replayed, frames) {
				t.Fatalf("replay changed: %+v %v", replayed, err)
			}
		})
	}
	frames, err := (SessionEventProjection{SessionID: uuid.New(), Cursor: 1}).BotFrames(0)
	if err != nil || len(frames) != 1 || frames[0].Type != botCursorEvent {
		t.Fatalf("hidden cursor: %+v %v", frames, err)
	}
	var hidden map[string]any
	if err := json.Unmarshal(frames[0].Data, &hidden); err != nil {
		t.Fatal(err)
	}
	if _, ok := hidden["timestamp"]; ok {
		t.Fatal("hidden cursor gained timestamp")
	}
}

// This appendable journal is synchronized because real HTTP delivery polls it.
// Rows deliberately have no runtime provenance: saved report invocations remain
// presentation identities, never execution authority.
type deliveryTimestampRows struct {
	*botSessionMetaFixture
	mu   sync.Mutex
	rows []store.BotTranscriptEvent
}

func (s *deliveryTimestampRows) ReadBotTranscript(_ context.Context, _ uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.BotTranscriptEvent
	for _, row := range s.rows {
		if row.Cursor > after && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
}

func TestBotDeliveryTimestampHTTP(t *testing.T) {
	owner, bid, sid, home, run := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &botSessionFixtureStore{b: store.Bot{ID: bid, OwnerID: owner, Scope: store.BotScopePersonal, HomeSessionID: home}, run: store.BotRun{ID: run, BotID: bid, SessionID: sid}}
	ss := &deliveryTimestampRows{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: bid.String(), BotRunID: run.String(), Purpose: store.SessionPurposeBotRun, Retention: store.SessionRetentionBot}}}
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo, PersonalSessions: ss})
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		defer cancel()
		router.ServeHTTP(w, r.WithContext(bound))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	open := func(endpoint, after string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+"/api/studio/bots/"+bid.String()+"/sessions/"+sid.String()+"/"+endpoint+"?scope=personal", nil)
		if err != nil {
			t.Fatal(err)
		}
		if after != "" {
			req.Header.Set("Last-Event-ID", after)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			t.Fatal(res.Status)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	stamps := []time.Time{time.Date(2026, 11, 27, 17, 0, 0, 123000000, time.UTC), time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC), {}}
	appendRow := func(i int) {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		suffix := []string{"first", "second", "legacy-unknown"}[i]
		ss.rows = append(ss.rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: &adksession.Event{ID: suffix, InvocationID: "bot-report:" + suffix, Author: "assistant", Timestamp: stamps[i], LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Saved report "+suffix, "model")}}})
	}
	check := func(frame botSSEFrame, i int) {
		t.Helper()
		var data map[string]any
		if err := json.Unmarshal([]byte(frame.data), &data); err != nil {
			t.Fatal(err)
		}
		if frame.event != "text" || frame.id != botEventCursor(sid, int64(i+1)) {
			t.Fatalf("delivery: %+v", frame)
		}
		if stamps[i].IsZero() {
			if _, ok := data["timestamp"]; ok {
				t.Fatalf("unknown time invented: %s", frame.data)
			}
		} else if data["timestamp"] != stamps[i].Format(time.RFC3339Nano) {
			t.Fatalf("saved time omitted: %s", frame.data)
		}
		meta := data["_projection"].(map[string]any)
		if meta["event_id"] == "" || meta["run_id"] != uuid.Nil.String() {
			t.Fatalf("legacy provenance changed: %s", frame.data)
		}
	}
	live := open("events", "")
	reader := bufio.NewReader(live.Body)
	if f := readBotSSE(t, reader); f.event != botReplayComplete {
		t.Fatal(f)
	}
	appendRow(0)
	first := readBotSSE(t, reader)
	check(first, 0)
	live.Body.Close()
	appendRow(1)
	reconnected := open("events", first.id)
	rr := bufio.NewReader(reconnected.Body)
	second := readBotSSE(t, rr)
	check(second, 1)
	if f := readBotSSE(t, rr); f.event != botReplayComplete {
		t.Fatal("unexpected synthetic completion", f)
	}
	appendRow(2)
	unknown := readBotSSE(t, rr)
	check(unknown, 2)
	reconnected.Body.Close()
	for _, endpoint := range []string{"events", "stream"} {
		res := open(endpoint, "")
		rd := bufio.NewReader(res.Body)
		for i, want := range []botSSEFrame{first, second, unknown} {
			got := readBotSSE(t, rd)
			check(got, i)
			if got != want {
				t.Fatalf("%s changed saved delivery: %+v != %+v", endpoint, got, want)
			}
		}
		boundary := readBotSSE(t, rd)
		want := botReplayComplete
		if endpoint == "stream" {
			want = botTranscriptDone
		}
		if boundary.event != want {
			t.Fatalf("%s boundary: %+v", endpoint, boundary)
		}
		res.Body.Close()
	}
}
