package agent

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestNewTimestampedUserContentWithAttachmentsEmptyCaption(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n"))
	got := NewTimestampedUserContentWithAttachments("", []Attachment{{
		Filename: "logo.png",
		MimeType: "image/png",
		Data:     png,
	}})
	if got == nil || got.Role != genai.RoleUser {
		t.Fatalf("want user content, got %#v", got)
	}
	if len(got.Parts) < 2 {
		t.Fatalf("want text + image parts, got %d", len(got.Parts))
	}
	if !strings.Contains(got.Parts[0].Text, "[attached logo.png]") {
		t.Fatalf("empty caption should name the file, got %q", got.Parts[0].Text)
	}
	if got.Parts[1].InlineData == nil || got.Parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("missing image part: %#v", got.Parts[1])
	}
}

func TestNewTimestampedUserContentWithAttachmentsKeepsCaption(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n"))
	got := NewTimestampedUserContentWithAttachments("here you go", []Attachment{{
		Filename: "logo.png",
		MimeType: "image/png",
		Data:     png,
	}})
	if !strings.Contains(got.Parts[0].Text, "here you go") {
		t.Fatalf("caption missing: %q", got.Parts[0].Text)
	}
	if strings.Contains(got.Parts[0].Text, "[attached") {
		t.Fatalf("should not replace a real caption: %q", got.Parts[0].Text)
	}
}
