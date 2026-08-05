package slack

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/channels"

	"github.com/slack-go/slack"
)

func TestRenderOutboundMessageConvertsHTMLToMrkdwn(t *testing.T) {
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text:   `<p>Here are <strong>18 items</strong>. See <a href="https://example.test/items">details</a>.</p>`,
		Format: channels.FormatHTML,
	})

	if len(rendered) != 1 {
		t.Fatalf("rendered messages = %d, want 1", len(rendered))
	}
	want := "Here are *18 items*. See <https://example.test/items|details>."
	if strings.TrimSpace(rendered[0].Text) != want {
		t.Fatalf("rendered text = %q, want %q", rendered[0].Text, want)
	}
	if len(rendered[0].Blocks) != 0 {
		t.Fatalf("blocks = %d, want plain mrkdwn", len(rendered[0].Blocks))
	}
}

func TestRenderOutboundMessageBuildsFieldsForKeyValueSummary(t *testing.T) {
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text: strings.Join([]string{
			"**Deployment Summary**",
			"Region: qa-de-1",
			"Status: healthy",
			"Duration: 42s",
		}, "\n"),
		Format: channels.FormatMarkdown,
	})

	if len(rendered) != 1 {
		t.Fatalf("rendered messages = %d, want 1", len(rendered))
	}
	if len(rendered[0].Blocks) != 3 {
		t.Fatalf("blocks = %d, want title, fields, context", len(rendered[0].Blocks))
	}

	fieldsBlock, ok := rendered[0].Blocks[1].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("second block type = %T, want *slack.SectionBlock", rendered[0].Blocks[1])
	}
	if len(fieldsBlock.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(fieldsBlock.Fields))
	}
	if fieldsBlock.Fields[0].Text != "*Region*\nqa-de-1" {
		t.Fatalf("first field = %q", fieldsBlock.Fields[0].Text)
	}
	if _, ok := rendered[0].Blocks[2].(*slack.ContextBlock); !ok {
		t.Fatalf("third block type = %T, want *slack.ContextBlock", rendered[0].Blocks[2])
	}
}

func TestRenderOutboundMessageBuildsTableForMarkdownTable(t *testing.T) {
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text: strings.Join([]string{
			"**Inventory**",
			"| Status | Name | Owner |",
			"| --- | --- | --- |",
			"| Active | item-1 | team-a |",
			"| Paused | item-2 | team-b |",
		}, "\n"),
		Format: channels.FormatMarkdown,
	})

	if len(rendered) != 1 {
		t.Fatalf("rendered messages = %d, want 1", len(rendered))
	}
	if len(rendered[0].Blocks) != 3 {
		t.Fatalf("blocks = %d, want title, table, context", len(rendered[0].Blocks))
	}
	tableBlock, ok := rendered[0].Blocks[1].(*slack.TableBlock)
	if !ok {
		t.Fatalf("second block type = %T, want *slack.TableBlock", rendered[0].Blocks[1])
	}
	if len(tableBlock.Rows) != 3 {
		t.Fatalf("table rows = %d, want header plus 2 data rows", len(tableBlock.Rows))
	}
	firstHeader, ok := tableBlock.Rows[0][0].(*slack.TableRawTextCell)
	if !ok {
		t.Fatalf("first header cell type = %T, want *slack.TableRawTextCell", tableBlock.Rows[0][0])
	}
	if firstHeader.Text != "Status" {
		t.Fatalf("first header = %q", firstHeader.Text)
	}
}

func TestRenderOutboundMessageBuildsGenericTableForGroupedList(t *testing.T) {
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text: strings.Join([]string{
			"Inventory summary:",
			"",
			":large_green_circle: READY (2)",
			"- item-1 — owner team-a — updated today",
			"- item-2 — owner team-b — updated yesterday",
			"",
			":red_circle: BLOCKED (1)",
			"- item-3 — waiting for approval",
		}, "\n"),
		Format: channels.FormatMarkdown,
	})

	if len(rendered) != 1 {
		t.Fatalf("rendered messages = %d, want 1", len(rendered))
	}
	if len(rendered[0].Blocks) != 4 {
		t.Fatalf("blocks = %d, want title, counts, table, context", len(rendered[0].Blocks))
	}
	countBlock, ok := rendered[0].Blocks[1].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("second block type = %T, want *slack.SectionBlock", rendered[0].Blocks[1])
	}
	if countBlock.Fields[0].Text != "*:large_green_circle: READY*\n2" {
		t.Fatalf("first count field = %q", countBlock.Fields[0].Text)
	}
	tableBlock, ok := rendered[0].Blocks[2].(*slack.TableBlock)
	if !ok {
		t.Fatalf("third block type = %T, want *slack.TableBlock", rendered[0].Blocks[2])
	}
	if len(tableBlock.Rows) != 4 {
		t.Fatalf("table rows = %d, want header plus 3 item rows", len(tableBlock.Rows))
	}
	if len(tableBlock.Rows[0]) != 3 {
		t.Fatalf("table columns = %d, want group, item, details", len(tableBlock.Rows[0]))
	}
	itemCell, ok := tableBlock.Rows[1][1].(*slack.TableRawTextCell)
	if !ok {
		t.Fatalf("item cell type = %T, want *slack.TableRawTextCell", tableBlock.Rows[1][1])
	}
	if itemCell.Text != "item-1" {
		t.Fatalf("first item = %q", itemCell.Text)
	}
	detailsCell, ok := tableBlock.Rows[1][2].(*slack.TableRawTextCell)
	if !ok {
		t.Fatalf("details cell type = %T, want *slack.TableRawTextCell", tableBlock.Rows[1][2])
	}
	if detailsCell.Text != "owner team-a — updated today" {
		t.Fatalf("first details = %q", detailsCell.Text)
	}
}

func TestRenderOutboundMessageFallsBackForCodeBlocks(t *testing.T) {
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text: strings.Join([]string{
			"**Example**",
			"```",
			"go test ./pkg/channels/slack",
			"```",
		}, "\n"),
		Format: channels.FormatMarkdown,
	})

	if len(rendered) != 1 {
		t.Fatalf("rendered messages = %d, want 1", len(rendered))
	}
	if len(rendered[0].Blocks) != 0 {
		t.Fatalf("blocks = %d, want plain text fallback for code blocks", len(rendered[0].Blocks))
	}
	if !strings.Contains(rendered[0].Text, "```") {
		t.Fatalf("rendered text should preserve code fence, got %q", rendered[0].Text)
	}
}

func TestRenderOutboundMessageChunksLongPlainText(t *testing.T) {
	text := strings.Repeat("a", maxMessageLength+20)
	rendered := renderOutboundMessage(channels.OutboundMessage{
		Text:   text,
		Format: channels.FormatText,
	})

	if len(rendered) != 2 {
		t.Fatalf("rendered messages = %d, want 2 chunks", len(rendered))
	}
	for i, msg := range rendered {
		if len(msg.Text) > maxMessageLength {
			t.Fatalf("chunk %d length = %d, want <= %d", i, len(msg.Text), maxMessageLength)
		}
		if len(msg.Blocks) != 0 {
			t.Fatalf("chunk %d has blocks; long plain text should not use Block Kit", i)
		}
	}
}
