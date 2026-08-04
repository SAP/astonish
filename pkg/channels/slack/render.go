package slack

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/channels"

	"github.com/slack-go/slack"
)

const (
	maxBlocksPerMessage    = 45
	maxSectionTextLength   = 3000
	maxFieldTextLength     = 2000
	maxFieldsPerSection    = 10
	maxContextTextLength   = 300
	structuredMessageLines = 16
	maxStructuredLines     = 80
)

var (
	reHTMLBreak       = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`)
	reHTMLParagraph   = regexp.MustCompile(`(?i)</\s*p\s*>`)
	reHTMLBlockClose  = regexp.MustCompile(`(?i)</\s*(div|section|article|li|ul|ol|h[1-6])\s*>`)
	reHTMLStrongOpen  = regexp.MustCompile(`(?i)<\s*(strong|b)\s*>`)
	reHTMLStrongEnd   = regexp.MustCompile(`(?i)</\s*(strong|b)\s*>`)
	reHTMLEmOpen      = regexp.MustCompile(`(?i)<\s*(em|i)\s*>`)
	reHTMLEmEnd       = regexp.MustCompile(`(?i)</\s*(em|i)\s*>`)
	reHTMLCodeOpen    = regexp.MustCompile(`(?i)<\s*code\s*>`)
	reHTMLCodeEnd     = regexp.MustCompile(`(?i)</\s*code\s*>`)
	reHTMLLink        = regexp.MustCompile(`(?is)<a\s+[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reHTMLTag         = regexp.MustCompile(`<[^>]+>`)
	reMarkdownHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	reBoldLine        = regexp.MustCompile(`^\*\*(.+)\*\*$`)
	reStatusHeading   = regexp.MustCompile(`^\*?\s*((?::[a-z0-9_+-]+:\s*)?)([A-Z][A-Z0-9 _/-]{2,})\s*\(([^)]+)\)\*?$`)
	reKeyValueLine    = regexp.MustCompile(`^[-*]?\s*([^:—-]{2,40})\s*(?::|—| - )\s*(.{1,120})$`)
	reListItemLine    = regexp.MustCompile(`^[-*]\s+(.+)$`)
	reTableDivider    = regexp.MustCompile(`^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?$`)
)

type renderedSlackMessage struct {
	Text   string
	Blocks []slack.Block
}

func renderOutboundMessage(msg channels.OutboundMessage) []renderedSlackMessage {
	if strings.TrimSpace(msg.Text) == "" {
		return nil
	}

	mrkdwn := outboundTextToMrkdwn(msg)
	if strings.TrimSpace(mrkdwn) == "" {
		return nil
	}

	if blocks := buildStructuredBlocks(mrkdwn); len(blocks) > 0 {
		return []renderedSlackMessage{{
			Text:   truncateText(stripMrkdwn(mrkdwn), maxMessageLength),
			Blocks: blocks,
		}}
	}

	chunks := splitMessage(mrkdwn, maxMessageLength)
	rendered := make([]renderedSlackMessage, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		rendered = append(rendered, renderedSlackMessage{Text: chunk})
	}
	return rendered
}

func outboundTextToMrkdwn(msg channels.OutboundMessage) string {
	switch msg.Format {
	case channels.FormatText:
		return msg.Text
	case channels.FormatHTML:
		return MarkdownToMrkdwn(htmlToMarkdownLikeText(msg.Text))
	case channels.FormatMarkdown, "":
		return MarkdownToMrkdwn(msg.Text)
	default:
		return MarkdownToMrkdwn(msg.Text)
	}
}

func htmlToMarkdownLikeText(text string) string {
	if text == "" {
		return ""
	}
	text = reHTMLLink.ReplaceAllString(text, "[$2]($1)")
	text = reHTMLStrongOpen.ReplaceAllString(text, "**")
	text = reHTMLStrongEnd.ReplaceAllString(text, "**")
	text = reHTMLEmOpen.ReplaceAllString(text, "_")
	text = reHTMLEmEnd.ReplaceAllString(text, "_")
	text = reHTMLCodeOpen.ReplaceAllString(text, "`")
	text = reHTMLCodeEnd.ReplaceAllString(text, "`")
	text = reHTMLBreak.ReplaceAllString(text, "\n")
	text = reHTMLParagraph.ReplaceAllString(text, "\n\n")
	text = reHTMLBlockClose.ReplaceAllString(text, "\n")
	text = reHTMLTag.ReplaceAllString(text, "")
	return html.UnescapeString(text)
}

func buildStructuredBlocks(text string) []slack.Block {
	if shouldUsePlainText(text) {
		return nil
	}

	lines := nonEmptyLines(text)
	if len(lines) < 3 || len(lines) > maxStructuredLines {
		return nil
	}

	var blocks []slack.Block
	if title, ok := extractTitle(lines[0]); ok {
		blocks = append(blocks, sectionBlock("*"+title+"*"))
		lines = lines[1:]
	}

	if table := markdownTableBlock(lines); table != nil {
		blocks = append(blocks, table)
		blocks = append(blocks, contextBlock("Astonish · table summary"))
		return blocks
	}

	if groups := parseStatusGroups(lines); len(groups) > 0 {
		if table := groupedListTableBlock(groups); table != nil {
			if len(blocks) == 0 {
				blocks = append(blocks, sectionBlock(firstNonEmptyLine(text)))
			}
			blocks = append(blocks, statusCountFieldSection(groups))
			blocks = append(blocks, table)
			blocks = append(blocks, contextBlock("Astonish · table summary"))
			return blocks
		}

		if len(blocks) == 0 {
			blocks = append(blocks, sectionBlock(firstNonEmptyLine(text)))
		}
		blocks = append(blocks, statusCountFieldSection(groups))
		for i, group := range groups {
			if len(blocks) >= maxBlocksPerMessage-1 {
				return nil
			}
			if i > 0 {
				blocks = append(blocks, slack.NewDividerBlock())
			}
			blocks = append(blocks, statusGroupBlocks(group)...)
		}
		blocks = append(blocks, contextBlock("Astonish · structured Slack summary"))
		return blocks
	}

	fields := extractKeyValueFields(lines)
	if len(fields) >= 2 && len(lines) <= structuredMessageLines {
		if len(blocks) == 0 {
			blocks = append(blocks, sectionBlock("*Summary*"))
		}
		blocks = append(blocks, fieldSections(fields)...)
		blocks = append(blocks, contextBlock("Astonish · structured Slack summary"))
		if len(blocks) <= maxBlocksPerMessage {
			return blocks
		}
		return nil
	}

	return nil
}

func shouldUsePlainText(text string) bool {
	return strings.Contains(text, "```") || len(text) > maxSectionTextLength*2
}

func nonEmptyLines(text string) []string {
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func extractTitle(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if m := reMarkdownHeading.FindStringSubmatch(line); len(m) == 2 {
		return strings.TrimSpace(m[1]), true
	}
	if m := reBoldLine.FindStringSubmatch(line); len(m) == 2 {
		return strings.TrimSpace(m[1]), true
	}
	if strings.HasPrefix(line, "*") && strings.HasSuffix(line, "*") && len(line) > 2 {
		return strings.TrimSpace(strings.Trim(line, "*")), true
	}
	return "", false
}

func extractKeyValueFields(lines []string) []*slack.TextBlockObject {
	fields := make([]*slack.TextBlockObject, 0, maxFieldsPerSection)
	for _, line := range lines {
		m := reKeyValueLine.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		key := strings.TrimSpace(strings.Trim(m[1], "-* "))
		value := strings.TrimSpace(m[2])
		if key == "" || value == "" {
			continue
		}
		field := "*" + key + "*\n" + value
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, truncateText(field, maxFieldTextLength), false, false))
		if len(fields) == maxFieldsPerSection {
			break
		}
	}
	return fields
}

type statusGroup struct {
	Emoji string
	Name  string
	Count string
	Items []string
}

func parseStatusGroups(lines []string) []statusGroup {
	groups := make([]statusGroup, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		trimmed = strings.TrimSpace(trimmed)
		if m := reStatusHeading.FindStringSubmatch(trimmed); len(m) == 4 {
			groups = append(groups, statusGroup{
				Emoji: strings.TrimSpace(m[1]),
				Name:  strings.TrimSpace(m[2]),
				Count: strings.TrimSpace(m[3]),
			})
			continue
		}

		if len(groups) == 0 {
			continue
		}
		if m := reListItemLine.FindStringSubmatch(line); len(m) == 2 {
			groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, strings.TrimSpace(m[1]))
		}
	}
	return groups
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return truncateText(strings.TrimSpace(line), maxSectionTextLength)
		}
	}
	return "*Summary*"
}

func statusCountFieldSection(groups []statusGroup) slack.Block {
	fields := make([]*slack.TextBlockObject, 0, len(groups))
	for _, group := range groups {
		label := strings.TrimSpace(strings.Join([]string{group.Emoji, group.Name}, " "))
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, "*"+label+"*\n"+group.Count, false, false))
	}
	return slack.NewSectionBlock(nil, fields, nil)
}

func markdownTableBlock(lines []string) slack.Block {
	for i := 0; i+1 < len(lines); i++ {
		if !strings.Contains(lines[i], "|") || !reTableDivider.MatchString(lines[i+1]) {
			continue
		}
		headers := parseMarkdownTableRow(lines[i])
		if len(headers) < 2 || len(headers) > 6 {
			return nil
		}
		table := slack.NewTableBlock("astonish_markdown_table").WithColumnSettings(tableColumnSettings(len(headers))...)
		table.AddRow(tableCells(headers)...)
		for _, line := range lines[i+2:] {
			if !strings.Contains(line, "|") {
				break
			}
			cells := parseMarkdownTableRow(line)
			if len(cells) != len(headers) {
				break
			}
			table.AddRow(tableCells(cells)...)
			if len(table.Rows) >= 100 {
				break
			}
		}
		if len(table.Rows) > 1 {
			return table
		}
	}
	return nil
}

func parseMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, truncateText(strings.TrimSpace(part), 120))
	}
	return cells
}

func groupedListTableBlock(groups []statusGroup) slack.Block {
	rowCount := 0
	for _, group := range groups {
		rowCount += len(group.Items)
	}
	if rowCount == 0 || rowCount > 100 {
		return nil
	}

	table := slack.NewTableBlock("astonish_grouped_list_table").WithColumnSettings(tableColumnSettings(3)...)
	table.AddRow(
		slack.NewTableRawTextCell("Group"),
		slack.NewTableRawTextCell("Item"),
		slack.NewTableRawTextCell("Details"),
	)
	for _, group := range groups {
		groupLabel := strings.TrimSpace(strings.Join([]string{group.Emoji, group.Name}, " "))
		for _, item := range group.Items {
			name, details := splitGroupedListItem(item)
			table.AddRow(
				slack.NewTableRawTextCell(truncateText(groupLabel, 80)),
				slack.NewTableRawTextCell(truncateText(name, 120)),
				slack.NewTableRawTextCell(truncateText(details, 200)),
			)
		}
	}
	return table
}

func splitGroupedListItem(item string) (string, string) {
	parts := strings.SplitN(item, " — ", 2)
	name := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return name, ""
	}
	return name, strings.TrimSpace(parts[1])
}

func tableColumnSettings(count int) []slack.ColumnSetting {
	settings := make([]slack.ColumnSetting, 0, count)
	for i := 0; i < count; i++ {
		settings = append(settings, slack.ColumnSetting{Align: slack.ColumnAlignmentLeft, IsWrapped: true})
	}
	return settings
}

func tableCells(values []string) []slack.TableCell {
	cells := make([]slack.TableCell, 0, len(values))
	for _, value := range values {
		cells = append(cells, slack.NewTableRawTextCell(value))
	}
	return cells
}

func statusGroupBlocks(group statusGroup) []slack.Block {
	heading := strings.TrimSpace(strings.Join([]string{group.Emoji, group.Name + " (" + group.Count + ")"}, " "))
	blocks := []slack.Block{sectionBlock("*" + heading + "*")}
	if len(group.Items) == 0 {
		return blocks
	}

	fields := make([]*slack.TextBlockObject, 0, maxFieldsPerSection)
	for _, item := range group.Items {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, formatStatusItemField(item), false, false))
		if len(fields) == maxFieldsPerSection {
			blocks = append(blocks, slack.NewSectionBlock(nil, fields, nil))
			fields = nil
			if len(blocks) >= 4 {
				remaining := len(group.Items) - maxFieldsPerSection*(len(blocks)-1)
				if remaining > 0 {
					blocks = append(blocks, sectionBlock("_"+pluralizeMoreItems(remaining)+" not shown in this compact Slack summary._"))
				}
				return blocks
			}
		}
	}
	if len(fields) > 0 {
		blocks = append(blocks, slack.NewSectionBlock(nil, fields, nil))
	}
	return blocks
}

func formatStatusItemField(item string) string {
	parts := strings.SplitN(item, " — ", 2)
	name := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return truncateText("*"+name+"*", maxFieldTextLength)
	}
	return truncateText("*"+name+"*\n"+strings.TrimSpace(parts[1]), maxFieldTextLength)
}

func pluralizeMoreItems(count int) string {
	if count == 1 {
		return "1 more item"
	}
	return strconv.Itoa(count) + " more items"
}

func fieldSections(fields []*slack.TextBlockObject) []slack.Block {
	blocks := make([]slack.Block, 0, (len(fields)+maxFieldsPerSection-1)/maxFieldsPerSection)
	for len(fields) > 0 {
		count := len(fields)
		if count > maxFieldsPerSection {
			count = maxFieldsPerSection
		}
		blocks = append(blocks, slack.NewSectionBlock(nil, fields[:count], nil))
		fields = fields[count:]
	}
	return blocks
}

func sectionBlock(text string) slack.Block {
	return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, truncateText(text, maxSectionTextLength), false, false), nil, nil)
}

func contextBlock(text string) slack.Block {
	return slack.NewContextBlock("astonish_context", slack.NewTextBlockObject(slack.MarkdownType, truncateText(text, maxContextTextLength), false, false))
}

func stripMrkdwn(text string) string {
	replacements := []string{"*", "", "_", "", "~", "", "`", ""}
	return strings.NewReplacer(replacements...).Replace(text)
}

func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	if maxLen <= 1 {
		return text[:maxLen]
	}
	return strings.TrimSpace(text[:maxLen-1]) + "…"
}
