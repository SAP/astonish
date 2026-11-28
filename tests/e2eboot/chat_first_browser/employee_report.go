package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
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
	// StudioChat preserves history order in data-message-index. A pointer keeps
	// missing DOM evidence distinct from the first message (index zero).
	MessageIndex *int   `json:"messageIndex"`
	HTML         string `json:"html"`
	Text         string `json:"text"`
	Visible      bool   `json:"visible"`
}

// Read only assistant bubbles in this Bot's Home, not sidebar/tool/debug text.
// Bind DOM observations to canonical history publications, never visible prose.
func employeeReportBubbles(page *rod.Page) []employeeReportBubble {
	var bubbles []employeeReportBubble
	for _, el := range page.MustElements(`[aria-label="Bot workspace"] [data-testid="agent-message-bubble"]`) {
		visible, err := el.Visible()
		must(err)
		var messageIndex *int
		index := el.MustEval(`() => this.closest('[data-message-index]')?.getAttribute('data-message-index') ?? ''`).String()
		if n, err := strconv.Atoi(index); err == nil && n >= 0 {
			messageIndex = &n
		}
		bubbles = append(bubbles, employeeReportBubble{
			MessageIndex: messageIndex,
			HTML:         el.MustEval(`() => this.innerHTML`).String(), Text: el.MustText(), Visible: visible,
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
	want, wantText, err := employeeReportDOM(rendered.String())
	if err != nil {
		return err
	}
	count = 0
	seen := make(map[int]bool)
	for _, bubble := range bubbles {
		if bubble.MessageIndex == nil || *bubble.MessageIndex < 0 || *bubble.MessageIndex >= len(h.Messages) {
			return fmt.Errorf("assistant bubble has no canonical history identity")
		}
		index := *bubble.MessageIndex
		m := h.Messages[index]
		id, err := uuid.Parse(strings.TrimPrefix(m.InvocationID, "bot-report:"))
		if m.Type != "agent" || err != nil || id == uuid.Nil || m.InvocationID != "bot-report:"+id.String() {
			return fmt.Errorf("private draft assistant bubble in Home")
		}
		if seen[index] {
			return fmt.Errorf("duplicate rendered publication %s", m.InvocationID)
		}
		seen[index] = true
		if id != run.ID {
			continue
		}
		count++
		got, gotText, err := employeeReportDOM(bubble.HTML)
		if err != nil {
			return err
		}
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
func employeeReportDOM(source string) ([]string, string, error) {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, "", err
	}
	var tokens []string
	var text strings.Builder
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
	return tokens, employeeReportWhitespace(text.String()), nil
}
