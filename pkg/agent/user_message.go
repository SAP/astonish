package agent

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/genai"
)

// userTimestampRe matches the timestamp prefix injected by NewTimestampedUserContent.
// Format: "[2026-03-20 14:30:05 UTC]\n"
var userTimestampRe = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \w+\]\n`)

var turnContextTextRe = regexp.MustCompile(`(?s)^\[Astonish Per-Turn Context — not user-authored\]\n\n.*$`)

// Attachment represents a file attachment to include in a user message.
type Attachment struct {
	Filename string // Original filename
	MimeType string // IANA MIME type (e.g., "image/png", "application/pdf")
	Data     string // Base64-encoded file content
}

// NewTimestampedUserContent creates a user message with an absolute timestamp
// prepended. The timestamp is baked in at creation time and never changes,
// keeping the conversation history stable for provider KV-cache reuse.
//
// Format: [2026-03-20 14:30:05 UTC]\n<text>
//
// Because every provider (Anthropic, OpenAI, Gemini) caches based on a
// prefix match of the message array, the key requirements are:
//   - The system prompt must be 100% static (no per-turn date/time).
//   - Historical messages must be immutable once created.
//
// Placing the timestamp in each user message satisfies both: the system
// prompt never changes, and each historical message — including its
// timestamp — is frozen as part of the cacheable prefix.
func NewTimestampedUserContent(text string) *genai.Content {
	ts := time.Now().UTC().Format("2006-01-02 15:04:05 MST")
	stamped := fmt.Sprintf("[%s]\n%s", ts, text)
	return genai.NewContentFromText(stamped, genai.RoleUser)
}

// NewTimestampedUserContentWithAttachments creates a multipart user message
// containing timestamped text plus inline data parts for each file attachment.
// The attachments are sent as InlineData blobs which multimodal models
// (Claude 3.5, GPT-4o, Gemini) can interpret directly.
func NewTimestampedUserContentWithAttachments(text string, attachments []Attachment) *genai.Content {
	ts := time.Now().UTC().Format("2006-01-02 15:04:05 MST")
	text = strings.TrimSpace(text)
	if text == "" {
		text = attachmentCaption(attachments)
	}
	stamped := fmt.Sprintf("[%s]\n%s", ts, text)

	parts := []*genai.Part{genai.NewPartFromText(stamped)}

	for _, att := range attachments {
		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			// If decoding fails, skip this attachment and add a note
			parts = append(parts, genai.NewPartFromText(fmt.Sprintf("[Failed to decode attachment: %s]", att.Filename)))
			continue
		}
		part := genai.NewPartFromBytes(data, att.MimeType)
		part.InlineData.DisplayName = att.Filename
		parts = append(parts, part)
	}

	return &genai.Content{
		Parts: parts,
		Role:  genai.RoleUser,
	}
}

func attachmentCaption(attachments []Attachment) string {
	var names []string
	for _, a := range attachments {
		n := strings.TrimSpace(a.Filename)
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "[attached file]"
	}
	if len(names) == 1 {
		return "[attached " + names[0] + "]"
	}
	return "[attached " + strings.Join(names, ", ") + "]"
}

// StripTimestamp removes the timestamp prefix injected by NewTimestampedUserContent.
// Use this when the flow engine needs the raw user text for programmatic purposes
// (state storage, approval checks) rather than passing it to the LLM.
func StripTimestamp(text string) string {
	return userTimestampRe.ReplaceAllString(text, "")
}

// CleanUserText removes Astonish-owned model context from text consumers that
// need the user's authored words, such as memory queries and transcript views.
func CleanUserText(text string) string {
	text = StripTimestamp(text)
	if turnContextTextRe.MatchString(text) {
		return ""
	}
	return text
}
