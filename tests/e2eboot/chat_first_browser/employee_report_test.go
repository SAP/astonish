package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Current Home format: committed employee prose without diagnostic heading/footer.
func employeeReportFixture(t *testing.T) (store.BotRun, string, string, employeeReportBubble) {
	t.Helper()
	run := store.BotRun{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Status: store.RunSucceeded}
	run.Report.Summary = "**Orlando weather**\n\n- **High:** 83 °F\n- **Wind:** North\n\n*Source: Weather service.*"
	run.Report.EmployeeMessage = &store.BotEmployeeMessage{Origin: store.BotEmployeeMessageAssessed, Text: run.Report.Summary, SourceDigest: run.Report.EmployeeMessageSourceDigest(run.Status), CandidateDigest: "assessed-candidate"}
	if err := run.Report.ValidateEmployeeMessageIntegrity(run.Status); err != nil {
		t.Fatal(err)
	}
	home := "22222222-2222-4222-8222-222222222222"
	raw, err := json.Marshal(map[string]any{"id": home, "messages": []map[string]string{{"type": "agent", "invocationId": "bot-report:" + run.ID.String(), "content": bot.RenderReport(run)}}})
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	bubble := employeeReportBubble{MessageIndex: &index, Visible: true, HTML: `<div class="markdown-body"><p><strong>Orlando weather</strong></p><ul><li><strong>High:</strong> 83 °F</li><li><strong>Wind:</strong> North</li></ul><p><em>Source: Weather service.</em></p></div>`, Text: "Orlando weather\n\nHigh: 83 °F\nWind: North\n\nSource: Weather service."}
	return run, home, string(raw), bubble
}

func TestEmployeeReportSavedEvidence(t *testing.T) {
	root := os.Getenv("ASTONISH_EMPLOYEE_REPORT_EVIDENCE")
	if root == "" {
		t.Skip("optional read-only failed-run evidence")
	}
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var disk employeeDisk
	if err := json.Unmarshal(read("employee-offline-restart-tick.json"), &disk); err != nil {
		t.Fatal(err)
	}
	var run store.BotRun
	for _, row := range disk.Runs {
		if row.ID == "691fdc11-71f7-4332-898c-44a14efe2091" {
			run = employeeRun(row)
		}
	}
	if run.ID == uuid.Nil {
		t.Fatal("missing captured run")
	}
	var saved struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(read("browser-blocked.txt"), &saved); err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(bot.RenderReport(run)), &rendered); err != nil {
		t.Fatal(err)
	}
	_, text, err := employeeReportDOM(rendered.String())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(employeeReportWhitespace(saved.Text), text); n != 1 {
		t.Fatalf("saved report rendered-text count=%d", n)
	}
	if strings.Contains(saved.Text, run.Report.Summary) {
		t.Fatal("raw Markdown unexpectedly equals rendered text")
	}
	// Saved HTML was truncated in the failure diagnostic: synthetic DOM below
	// verifies the assertion against real history/text, NOT the live DOM/receipt.
	history := string(read("employee-reauth-history.json"))
	var h struct {
		Messages []struct {
			InvocationID string `json:"invocationId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(history), &h); err != nil {
		t.Fatal(err)
	}
	var index *int
	for i, m := range h.Messages {
		if m.InvocationID == "bot-report:"+run.ID.String() {
			n := i
			index = &n
		}
	}
	bubble := employeeReportBubble{MessageIndex: index, HTML: rendered.String(), Text: text, Visible: true}
	if err := employeeReportExactlyOnce(history, "5372e7dd-9a24-4c52-92f8-a3fc9ccbb7f9", run, []employeeReportBubble{bubble}); err != nil {
		t.Fatal(err)
	}
	t.Log("saved history: exactly one matching publication; saved text: exactly one full rendered report, zero raw summary matches; DOM synthesized, not live acceptance")
}

func TestEmployeeReportFormattedExactlyOnce(t *testing.T) {
	run, home, history, bubble := employeeReportFixture(t)
	if strings.Contains(bubble.Text, run.Report.Summary) {
		t.Fatal("fixture lost Markdown regression")
	}
	if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{bubble}); err != nil {
		t.Fatal(err)
	}
	other := bubble
	index := 1
	other.MessageIndex = &index
	history = employeeReportAppendHistory(t, history, "agent", "bot-report:"+uuid.NewString(), bot.RenderReport(run))
	if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{other, bubble}); err != nil {
		t.Fatalf("same prose from a different run must not count as this publication: %v", err)
	}
}

func TestEmployeeReportCurrentFormats(t *testing.T) {
	for _, fixture := range []struct{ name, markdown, html, text string }{
		{"headings-links", "## Today's weather\n\nRead the [full forecast](https://weather.example.test/forecast).\n\n1. Bring **water**.\n2. Expect *sun*.", `<h2>Today's weather</h2><p>Read the <a href="https://weather.example.test/forecast">full forecast</a>.</p><ol><li>Bring <strong>water</strong>.</li><li>Expect <em>sun</em>.</li></ol>`, "Today's weather\nRead the full forecast.\nBring water.\nExpect sun."},
		{"table", "| Day | High |\n| --- | --- |\n| Monday | 83 °F |", `<table><thead><tr><th>Day</th><th>High</th></tr></thead><tbody><tr><td>Monday</td><td>83 °F</td></tr></tbody></table>`, "Day\tHigh\nMonday\t83 °F"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			run, home, _, bubble := employeeReportFixture(t)
			run.Report.Summary = fixture.markdown
			run.Report.EmployeeMessage.Text = fixture.markdown
			run.Report.EmployeeMessage.SourceDigest = run.Report.EmployeeMessageSourceDigest(run.Status)
			if got := bot.RenderReport(run); got != fixture.markdown+"\n" {
				t.Fatalf("unexpected current projection: %q", got)
			}
			history := employeeReportAppendHistory(t, `{"id":"`+home+`","messages":[]}`, "user", "", "Please check the weather.")
			history = employeeReportAppendHistory(t, history, "agent", "bot-report:"+run.ID.String(), bot.RenderReport(run))
			index := 1
			bubble.MessageIndex, bubble.HTML, bubble.Text = &index, fixture.html, fixture.text
			if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{bubble}); err != nil {
				t.Fatal(err)
			}
			if fixture.name == "headings-links" {
				bubble.HTML = strings.ReplaceAll(bubble.HTML, "https://weather.example.test/forecast", "https://wrong.example.test")
				if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{bubble}); err == nil {
					t.Fatal("changed link accepted despite identical visible prose")
				}
			}
		})
	}
}

func employeeReportAppendHistory(t *testing.T, history, kind, invocation, content string) string {
	t.Helper()
	var h map[string]any
	if err := json.Unmarshal([]byte(history), &h); err != nil {
		t.Fatal(err)
	}
	h["messages"] = append(h["messages"].([]any), map[string]string{"type": kind, "invocationId": invocation, "content": content})
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEmployeeReportRejectsWrongMissingDuplicate(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "hidden", "wrong-run", "wrong-content", "empty-body", "unformatted", "wrong-home", "missing-history", "wrong-invocation", "duplicate-history", "wrong-history-content", "empty-summary", "raw-markdown", "raw-duplicate", "hidden-duplicate", "footer-only", "duplicate-footer", "missing-index", "negative-index", "out-of-range-index", "user-index", "draft-index", "malformed-id", "nil-id", "wrong-text", "incomplete-text", "duplicate-body", "unlabelled-draft"} {
		t.Run(name, func(t *testing.T) {
			run, home, history, bubble := employeeReportFixture(t)
			bubbles := []employeeReportBubble{bubble}
			switch name {
			case "missing":
				bubbles = nil
			case "duplicate":
				bubbles = append(bubbles, bubble)
			case "hidden":
				bubbles[0].Visible = false
			case "wrong-run", "user-index", "draft-index", "malformed-id", "nil-id":
				kind, invocation := "agent", "bot-report:"+uuid.NewString()
				switch name {
				case "user-index":
					kind = "user"
				case "draft-index":
					invocation = "private-draft"
				case "malformed-id":
					invocation = "bot-report:not-a-uuid"
				case "nil-id":
					invocation = "bot-report:" + uuid.Nil.String()
				}
				history = employeeReportAppendHistory(t, history, kind, invocation, bot.RenderReport(run))
				index := 1
				bubbles[0].MessageIndex = &index
			case "missing-index":
				bubbles[0].MessageIndex = nil
			case "negative-index", "out-of-range-index":
				index := -1
				if name == "out-of-range-index" {
					index = 100
				}
				bubbles[0].MessageIndex = &index
			case "wrong-text":
				bubbles[0].Text = strings.ReplaceAll(bubble.Text, "83", "99")
			case "incomplete-text":
				bubbles[0].Text = "Orlando weather"
			case "duplicate-body":
				bubbles[0].HTML += bubble.HTML
				bubbles[0].Text += bubble.Text
			case "unlabelled-draft":
				bubble.MessageIndex = nil
				bubbles = append(bubbles, bubble)
			case "wrong-content":
				bubbles[0].HTML = strings.ReplaceAll(bubble.HTML, "83", "99")
			case "empty-body":
				bubbles[0].HTML = ""
			case "unformatted":
				bubbles[0].HTML = strings.NewReplacer("<strong>", "", "</strong>", "", "<em>", "", "</em>", "").Replace(bubble.HTML)
			case "wrong-home":
				home = uuid.NewString()
			case "missing-history":
				history = `{"id":"` + home + `","messages":[]}`
			case "wrong-invocation":
				history = strings.ReplaceAll(history, "bot-report:", "wrong:")
			case "duplicate-history":
				var h map[string]any
				if err := json.Unmarshal([]byte(history), &h); err != nil {
					t.Fatal(err)
				}
				m := h["messages"].([]any)
				h["messages"] = append(m, m[0])
				b, _ := json.Marshal(h)
				history = string(b)
			case "wrong-history-content":
				history = strings.ReplaceAll(history, "83", "99")
			case "empty-summary":
				run.Report.Summary = ""
			case "raw-markdown":
				bubbles[0].HTML = "<pre>" + bot.RenderReport(run) + "</pre>"
				bubbles[0].Text = bot.RenderReport(run)
			case "raw-duplicate":
				bubbles = append(bubbles, employeeReportBubble{MessageIndex: bubble.MessageIndex, HTML: "<pre>" + bot.RenderReport(run) + "</pre>", Text: bot.RenderReport(run), Visible: true})
			case "hidden-duplicate":
				bubble.Visible = false
				bubbles = append(bubbles, bubble)
			case "footer-only":
				bubbles[0].HTML = "<p>Run: " + run.ID.String() + "</p>"
			case "duplicate-footer":
				bubbles[0].HTML += "<p>Run: " + run.ID.String() + "</p>"
			}
			if err := employeeReportExactlyOnce(history, home, run, bubbles); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}
