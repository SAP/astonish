package main

import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateTraceFixture(t *testing.T, tool string) (store.Bot, store.BotRun, string, string) {
	t.Helper()
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	var r store.BotRun
	raw := fmt.Sprintf(`{"id":%q,"bot_id":%q,"session_id":%q,"user_event_id":"source-event","status":"succeeded","report":{"summary":"Orlando weather is sunny","unchanged":[{"source":"https://weather.example/Orlando"}]}}`, uuid.NewString(), b.ID, uuid.NewString())
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	h, _ := json.Marshal(map[string]any{"id": b.HomeSessionID.String(), "messages": []map[string]string{{"type": "user", "content": "Weather in Orlando"}, {"type": "agent", "invocationId": "bot-report:" + r.ID.String(), "content": bot.RenderReport(r)}}})
	stream := fmt.Sprintf(`event: transcript
data: {"Author":"bot_runtime","Actions":{"StateDelta":{"bot_run_state":{"bot_id":%q,"run_id":%q,"session_id":%q,"home_session_id":%q,"user_event_id":"source-event"}}}}

event: transcript
data: {"ID":"source-event","Author":"user","InvocationID":"source-invocation","Content":{"parts":[{"text":"Weather in Orlando"}]}}

event: transcript
data: {"InvocationID":"source-invocation","Content":{"parts":[{"functionCall":{"id":"call-id","name":%q,"args":{"url":"https://weather.example/Orlando"}}}]}}

event: transcript
data: {"InvocationID":"source-invocation","Content":{"parts":[{"functionResponse":{"id":"call-id","name":%q,"response":{"status":200,"url":"https://weather.example/Orlando","body":"Orlando weather is sunny and warm with a gentle breeze"}}}]}}

event: done
data: {"persisted":true}

`, b.ID, r.ID, r.SessionID, b.HomeSessionID, tool, tool)
	return b, r, string(h), stream
}

func TestEmployeePrivateTraceCorrelation(t *testing.T) {
	for _, tool := range []string{"http_request", "web_fetch"} {
		t.Run(tool, func(t *testing.T) {
			b, r, h, s := privateTraceFixture(t, tool)
			if employeeWebEvidence(h) {
				t.Fatal("fixture must have no Home tool traces")
			}
			if err := employeeTitleEvidence(h, s, b, r); err != nil {
				t.Fatal(err)
			}
			// Actual ADK http_request response wraps the body/status_code under result.
			wrapped := strings.Replace(s, `"response":{"status":200,`, `"response":{"result":{"status":"200 OK","status_code":200,`, 1)
			wrapped = strings.Replace(wrapped, `gentle breeze"}}}]`, `gentle breeze"}}}}]`, 1)
			if err := employeeTitleEvidence(h, wrapped, b, r); err != nil {
				t.Fatal(err)
			}
			if err := employeeTitleEvidence(h, strings.Replace(wrapped, `"status_code":200`, `"status_code":500`, 1), b, r); err == nil {
				t.Fatal("wrapped HTTP failure accepted")
			}
			for name, bad := range map[string]string{
				"wrong-run":        strings.ReplaceAll(s, r.ID.String(), uuid.NewString()),
				"wrong-session":    strings.ReplaceAll(s, r.SessionID.String(), uuid.NewString()),
				"wrong-source":     strings.ReplaceAll(s, "source-event", "another-event"),
				"cross-invocation": strings.Replace(s, `"InvocationID":"source-invocation","Content":{"parts":[{"functionResponse"`, `"InvocationID":"other","Content":{"parts":[{"functionResponse"`, 1),
				"unpaired":         strings.Replace(s, `"id":"call-id"`, `"id":"other-call"`, 1),
				"wrong-name":       strings.Replace(s, `"name":"`+tool+`"`, `"name":"other-tool"`, 1),
				"http-error":       strings.ReplaceAll(s, `"status":200`, `"status":500`),
				"wrong-url":        strings.Replace(s, "https://weather.example/Orlando", "https://other.example/", 1),
				"incomplete":       strings.ReplaceAll(s, `"persisted":true`, `"persisted":false`),
				"model-only":       `event: done` + "\n" + `data: {"persisted":true}` + "\n\n",
			} {
				t.Run(name, func(t *testing.T) {
					if err := employeeTitleEvidence(h, bad, b, r); err == nil {
						t.Fatal("uncorrelated evidence accepted")
					}
				})
			}
			if err := employeeTitleEvidence(strings.Replace(h, "bot-report:", "model-claim:", 1), s, b, r); err == nil {
				t.Fatal("model publication accepted")
			}
		})
	}
}

func TestEmployeePrivateSavedEvidence(t *testing.T) {
	root := os.Getenv("ASTONISH_EMPLOYEE_PRIVATE_EVIDENCE")
	if root == "" {
		t.Skip("optional read-only saved title evidence")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join("../../..", root)
	}
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	var rr struct{ Runs []store.BotRun }
	if err := json.Unmarshal([]byte(read("employee-runs.json")), &rr); err != nil {
		t.Fatal(err)
	}
	var ss store.BotSnapshot
	if err := json.Unmarshal([]byte(read("employee-snapshot.json")), &ss); err != nil {
		t.Fatal(err)
	}
	if len(rr.Runs) != 1 {
		t.Fatal("expected one title run")
	}
	h, s := read("employee-title-history.json"), read("employee-title-events.json")
	if employeeWebEvidence(h) {
		t.Fatal("saved Home must be final-only")
	}
	if err := employeeTitleEvidence(h, s, ss.Bot, rr.Runs[0]); err != nil {
		t.Fatal(err)
	}
	t.Log("saved private trace and canonical Home report correlated; no live DOM claim")
}

func TestEmployeePrivateDraftMustNotLeakIntoHome(t *testing.T) {
	run, home, history, bubble := employeeReportFixture(t)
	if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{bubble, {Visible: true, HTML: "<p>Private candidate</p>", Text: "Private candidate"}}); err == nil {
		t.Fatal("private live draft accepted")
	}
	history = strings.Replace(history, `"messages":[`, `"messages":[{"type":"agent","invocationId":"private-draft","content":"I fetched the weather"},`, 1)
	if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{bubble}); err == nil {
		t.Fatal("private draft in Home accepted alongside canonical report")
	}
}
