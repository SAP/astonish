package main

import (
	"os"
	"strings"
	"testing"

	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestEmployeePeerHistoryResponseShapes(t *testing.T) {
	peer := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	run := store.BotRun{ID: uuid.New(), BotID: peer.ID, Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, UserEventID: "title-event", HistoryCursor: 1, Status: store.RunSucceeded}
	summary, err := os.ReadFile("testdata/employee-peer-session-summary.json")
	if err != nil {
		t.Fatal(err)
	}
	exact := admissionHistory(run.UserEventID, "user", employeePeerTitle)
	for _, tc := range []struct {
		name, raw          string
		pending, malformed bool
	}{
		{"saved-json-summary-is-not-event-evidence", string(summary), false, true},
		{"empty-transient", "", true, false},
		{"snapshot-before-user-event", "event: done\ndata: {\"persisted\":true,\"count\":0}\n\n", true, false},
		{"truncated-snapshot", strings.Split(exact, "event: done")[0], true, false},
		{"persisted-user-before-assistant", exact, false, false},
		{"malformed-json", "event: transcript\ndata: {bad}\n\n", false, true},
		{"wrong-done-shape", "event: done\ndata: {}\n\n", false, true},
		{"forged-persisted-transcript", "event: transcript\ndata: {\"persisted\":true}\n\n", false, true},
		{"duplicate-title-events-one-run", strings.Replace(exact, "event: done", "event: transcript\ndata: {\"id\":\"extra-title\",\"author\":\"user\",\"content\":{\"role\":\"user\",\"parts\":[{\"text\":\"Preservation witness\"}]}}\n\nevent: done", 1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := employeePeerTitleRun(tc.raw, peer, []store.BotRun{run})
			if tc.malformed {
				if err == nil {
					t.Fatal("invalid evidence accepted")
				}
				if tc.name == "saved-json-summary-is-not-event-evidence" && !strings.Contains(err.Error(), "malformed") {
					t.Fatalf("summary must fail as shape mismatch, not pending: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.pending && got.ID != uuid.Nil {
				t.Fatal("unverified title admitted")
			}
			if !tc.pending && got.ID != run.ID {
				t.Fatal("persisted exact title not admitted")
			}
		})
	}
}

func TestEmployeePeerHistorySDKEventAndRequest(t *testing.T) {
	peer := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	run := store.BotRun{ID: uuid.New(), BotID: peer.ID, Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, UserEventID: "title-event", HistoryCursor: 1}
	// BotHistoryStreamHandler marshals the stored ADK event directly. Exercise
	// that wire shape rather than the display-only SessionDetails response.
	event := &session.Event{ID: run.UserEventID, Author: "user"}
	event.Content = &genai.Content{Role: "user", Parts: []*genai.Part{{Text: employeePeerTitle}}}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	raw := "id: 1\nevent: transcript\ndata: " + string(body) + "\n\nevent: done\ndata: {\"persisted\":true,\"count\":1}\n\n"
	got, err := employeePeerTitleRun(raw, peer, []store.BotRun{run})
	if err != nil || got.ID != run.ID {
		t.Fatalf("SDK event rejected: %v %v", got.ID, err)
	}
	want := "/api/studio/bots/" + peer.ID.String() + "/sessions/" + peer.HomeSessionID.String() + "/stream?scope=personal"
	if path := employeePeerHistoryPath(peer); path != want {
		t.Fatalf("wrong peer path: %s", path)
	}
	for _, bad := range []string{strings.Replace(raw, `"count":1`, `"count":2`, 1), raw + raw, strings.Replace(raw, `"Author":"user"`, `"Author":"model"`, 1)} {
		if _, err := employeePeerTitleRun(bad, peer, []store.BotRun{run}); err == nil {
			t.Fatalf("invalid count, duplicate snapshot, or assistant echo admitted: %s", bad)
		}
	}
}

func TestEmployeePeerHistoryRequestWiring(t *testing.T) {
	raw, err := os.ReadFile("newemployee.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, `employeePeerHistoryPath(peerBot)`) {
		t.Fatal("peer must request its canonical persisted Home event stream, not generic session summary")
	}
}
