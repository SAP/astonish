package api

import (
	"bufio"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// Linked sessions use the ordinary REST restore route. It must expose the same
// sanitized ordered projection as Bot SSE, not reinterpret private ADK rows.
func TestBotLinkedSessionRestoreProjectionHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			for _, home := range []bool{true, false} {
				name := "linkedRun"
				if home {
					name = "linkedHome"
				}
				t.Run(name, func(t *testing.T) {
					f := newPresentationMatrixFixture(t, scope, home)
					route := "/api/studio/sessions/" + f.sid.String()
					read := func(t *testing.T) []ChatEvent {
						t.Helper()
						res := f.get(t, route, "", 200)
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err != nil {
							t.Fatal(err)
						}
						matrixAssertSafe(t, string(raw))
						var got struct {
							PresentationEvents []ChatEvent `json:"presentationEvents"`
						}
						if err := json.Unmarshal(raw, &got); err != nil {
							t.Fatal(err)
						}
						if got.PresentationEvents == nil {
							t.Fatal("ordinary linked restore omitted canonical identities/payloads")
						}
						return got.PresentationEvents
					}
					t.Run("empty", func(t *testing.T) {
						if got := read(t); len(got) != 0 {
							t.Fatal("empty linked session fabricated events")
						}
					})
					f.append(t, f.rows)
					t.Run("populated", func(t *testing.T) {
						got := read(t)
						res := f.get(t, f.route("/stream"), "", 200)
						defer res.Body.Close()
						frames := matrixReadUntil(t, bufio.NewReader(res.Body), botTranscriptDone)
						var want []ChatEvent
						for _, frame := range frames {
							if frame.event == botCursorEvent {
								continue
							}
							var data map[string]any
							if err := json.Unmarshal([]byte(frame.data), &data); err != nil {
								t.Fatal(err)
							}
							meta := data["_projection"].(map[string]any)
							delete(data, "_projection")
							// SSE flattens committed delivery time into the payload;
							// REST carries the same time on the ChatEvent envelope.
							var timestamp time.Time
							if value, ok := data["timestamp"]; ok {
								text, ok := value.(string)
								if !ok {
									t.Fatalf("invalid SSE delivery timestamp: %v", value)
								}
								var err error
								timestamp, err = time.Parse(time.RFC3339Nano, text)
								if err != nil {
									t.Fatal(err)
								}
								delete(data, "timestamp")
							}
							want = append(want, ChatEvent{ID: meta["event_id"].(string), Type: frame.event, Data: data, Timestamp: timestamp})
						}
						if len(got) != len(want) {
							t.Fatalf("ordinary restore event count diverged from Bot SSE: got %d want %d", len(got), len(want))
						}
						for i := range got {
							if !got[i].Timestamp.Equal(want[i].Timestamp) {
								t.Fatalf("ordinary restore delivery time diverged from Bot SSE: got %v want %v", got[i].Timestamp, want[i].Timestamp)
							}
							got[i].Timestamp = got[i].Timestamp.UTC()
							want[i].Timestamp = want[i].Timestamp.UTC()
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("ordinary restore diverged from Bot identities/payloads: got %+v want %+v", got, want)
						}
					})
					t.Run("revoked", func(t *testing.T) {
						f.repo.revoked.Store(true)
						defer f.repo.revoked.Store(false)
						res := f.get(t, route, "", 404)
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err != nil {
							t.Fatal(err)
						}
						matrixAssertSafe(t, string(raw))
					})
					t.Run("uncommittedPublication", func(t *testing.T) {
						summary := f.repo.publication.Summary
						f.repo.publication.Summary = "Not the stored receipt"
						defer func() { f.repo.publication.Summary = summary }()
						res := f.get(t, route, "", 500)
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err != nil {
							t.Fatal(err)
						}
						matrixAssertSafe(t, string(raw))
					})
					t.Run("unavailable", func(t *testing.T) {
						f.ss.unavailable.Store(true)
						defer f.ss.unavailable.Store(false)
						res := f.get(t, route, "", 500)
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						if err != nil {
							t.Fatal(err)
						}
						matrixAssertSafe(t, string(raw))
					})
					t.Run("wrongApp", func(t *testing.T) { res := f.get(t, route+"?app=other", "", 404); res.Body.Close() })
				})
			}
		})
	}
}
