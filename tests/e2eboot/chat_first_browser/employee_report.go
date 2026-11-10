package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/net/html"
)

type employeeReportBubble struct {
	HTML    string `json:"html"`
	Text    string `json:"text"`
	Visible bool   `json:"visible"`
}

// Read only assistant bubbles in this Bot's Home, not sidebar/tool/debug text.
// The report footer is a persisted run identity, unlike shared weather prose.
func employeeReportBubbles(page *rod.Page) []employeeReportBubble {
	var bubbles []employeeReportBubble
	for _, el := range page.MustElements(`[aria-label="Bot workspace"] [data-testid="agent-message-bubble"]`) {
		visible, err := el.Visible()
		must(err)
		bubbles = append(bubbles, employeeReportBubble{
			HTML: el.MustEval(`() => this.innerHTML`).String(), Text: el.MustText(), Visible: visible,
		})
	}
	return bubbles
}

func employeeReportExactlyOnce(history string, home string, run store.BotRun, bubbles []employeeReportBubble) error {
	if err := employeeFinalOnlyHistory(history); err != nil {
		return err
	}
	if run.ID == uuid.Nil || strings.TrimSpace(run.Report.Summary) == "" {
		return fmt.Errorf("report has no run identity or summary")
	}
	var h struct {
		ID       string `json:"id"`
		Messages []struct {
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Content      string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(history), &h); err != nil {
		return fmt.Errorf("report history: %w", err)
	}
	if home == "" || h.ID != home {
		return fmt.Errorf("report history is not the expected Home")
	}
	expected := bot.RenderReport(run)
	count := 0
	for _, m := range h.Messages {
		if m.InvocationID != "bot-report:"+run.ID.String() {
			continue
		}
		count++
		if m.Type != "agent" || m.Content != expected {
			return fmt.Errorf("persisted report %s content/type mismatch", run.ID)
		}
	}
	if count != 1 {
		return fmt.Errorf("persisted report %s count = %d, want 1", run.ID, count)
	}
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(expected), &rendered); err != nil {
		return err
	}
	want, wantText, _, err := employeeReportDOM(rendered.String(), run.ID.String())
	if err != nil {
		return err
	}
	count = 0
	for _, bubble := range bubbles {
		got, gotText, identities, err := employeeReportDOM(bubble.HTML, run.ID.String())
		if err != nil {
			return err
		}
		// Count also a raw/malformed copy with the same visible Run footer;
		// it must not disappear from duplicate detection just because tags differ.
		textIdentities := 0
		for _, line := range strings.Split(bubble.Text, "\n") {
			if strings.TrimSpace(line) == "Run: "+run.ID.String() {
				textIdentities++
			}
		}
		identities = max(identities, textIdentities)
		if identities == 0 {
			// Other canonical runs may share the Home; an unlabelled assistant
			// draft is not another report and must not hide beside the final one.
			otherReport := false
			for _, line := range strings.Split(bubble.Text, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "Run: ") {
					_, err := uuid.Parse(strings.TrimPrefix(strings.TrimSpace(line), "Run: "))
					otherReport = otherReport || err == nil
				}
			}
			if !otherReport {
				return fmt.Errorf("private draft assistant bubble in Home")
			}
			continue
		}
		count += identities
		if !bubble.Visible || !reflect.DeepEqual(got, want) || gotText != wantText || employeeReportWhitespace(bubble.Text) != wantText {
			return fmt.Errorf("rendered report %s is hidden, incomplete, changed or not formatted", run.ID)
		}
	}
	if count != 1 {
		return fmt.Errorf("rendered report %s count = %d, want 1", run.ID, count)
	}
	return nil
}

func employeeReportWhitespace(s string) string { return strings.Join(strings.Fields(s), " ") }

// Compare semantic Markdown DOM, not raw Markdown against innerText and not
// regex-stripped prose. Keep tags/links/list structure so missing formatting,
// altered content and duplicate bodies fail. Ignore only layout wrappers and
// insignificant HTML whitespace. This is deliberately fail-closed for custom
// renderers (e.g. Mermaid), not a generic chat normalization layer.
func employeeReportDOM(source, runID string) ([]string, string, int, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, "", 0, err
	}
	var tokens []string
	var text strings.Builder
	identities := 0
	var content func(*html.Node) string
	content = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(content(c))
		}
		return b.String()
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			if s := employeeReportWhitespace(n.Data); s != "" {
				tokens = append(tokens, "text:"+s)
			}
			text.WriteString(n.Data)
			return
		}
		tag := n.Data
		semantic := n.Type == html.ElementNode && tag != "html" && tag != "head" && tag != "body" && tag != "div"
		block := strings.Contains("|p|h1|h2|h3|h4|h5|h6|li|ul|ol|pre|blockquote|br|tr|td|th|", "|"+tag+"|")
		if block {
			text.WriteByte(' ')
		}
		if n.Type == html.ElementNode && tag == "p" && strings.TrimSpace(content(n)) == "Run: "+runID {
			identities++
		}
		if semantic {
			tokens = append(tokens, "<"+tag+">")
			for _, a := range n.Attr {
				if a.Key == "href" || a.Key == "src" || a.Key == "alt" || a.Key == "start" {
					tokens = append(tokens, a.Key+"="+a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if semantic {
			tokens = append(tokens, "</"+tag+">")
		}
		if block {
			text.WriteByte(' ')
		}
	}
	walk(root)
	return tokens, employeeReportWhitespace(text.String()), identities, nil
}
