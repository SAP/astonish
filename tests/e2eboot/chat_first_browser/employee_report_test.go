package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Sanitized shape of 9qq4d6gn: bold heading/labels, list, italic source,
// publication invocation and Run footer. Do not flatten Markdown in the fixture.
func employeeReportFixture(t *testing.T) (store.BotRun, string, string, employeeReportBubble) {
	t.Helper()
	run := store.BotRun{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Status: store.RunSucceeded}
	run.Report.Summary = "**Orlando weather**\n\n- **High:** 83 °F\n- **Wind:** North\n\n*Source: Weather service.*"
	home := "22222222-2222-4222-8222-222222222222"
	raw, err := json.Marshal(map[string]any{"id": home, "messages": []map[string]string{{"type": "agent", "invocationId": "bot-report:" + run.ID.String(), "content": bot.RenderReport(run)}}})
	if err != nil {
		t.Fatal(err)
	}
	bubble := employeeReportBubble{Visible: true, HTML: fmt.Sprintf(`<div class="markdown-body"><h2>Run report — succeeded</h2><p><strong>Orlando weather</strong></p><ul><li><strong>High:</strong> 83 °F</li><li><strong>Wind:</strong> North</li></ul><p><em>Source: Weather service.</em></p><p>Run: %s</p></div>`, run.ID), Text: "Run report — succeeded\n\nOrlando weather\n\nHigh: 83 °F\nWind: North\n\nSource: Weather service.\n\nRun: " + run.ID.String()}
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
	_, text, _, err := employeeReportDOM(rendered.String(), run.ID.String())
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
	bubble := employeeReportBubble{HTML: rendered.String(), Text: text, Visible: true}
	if err := employeeReportExactlyOnce(string(read("employee-reauth-history.json")), "5372e7dd-9a24-4c52-92f8-a3fc9ccbb7f9", run, []employeeReportBubble{bubble}); err != nil {
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
	otherID := uuid.NewString()
	other.HTML = strings.ReplaceAll(other.HTML, run.ID.String(), otherID)
	other.Text = strings.ReplaceAll(other.Text, run.ID.String(), otherID)
	if err := employeeReportExactlyOnce(history, home, run, []employeeReportBubble{other, bubble}); err != nil {
		t.Fatalf("same prose from a different run must not count as this publication: %v", err)
	}
}

func TestEmployeeReportRejectsWrongMissingDuplicate(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "hidden", "wrong-run", "wrong-content", "empty-body", "unformatted", "wrong-home", "missing-history", "wrong-invocation", "duplicate-history", "wrong-history-content", "empty-summary", "raw-markdown", "raw-duplicate", "hidden-duplicate", "footer-only", "duplicate-footer"} {
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
			case "wrong-run":
				bubbles[0].HTML = strings.ReplaceAll(bubble.HTML, run.ID.String(), uuid.NewString())
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
				bubbles = append(bubbles, employeeReportBubble{HTML: "<pre>" + bot.RenderReport(run) + "</pre>", Text: bot.RenderReport(run), Visible: true})
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
