package api

import (
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides"
)

func TestTryParseDocsUpdateMessage(t *testing.T) {
	text := docsUpdatePrefix + `{"type":"slides","deckSlug":"migration","action":"slide_written","slideIndex":2,"totalSlides":3,"title":"Risk","deckTitle":"Migration","schemaVersion":1,"validation":{"errors":0,"warnings":1},"pptxCapability":{"native":2,"vector":1,"raster":0,"unsupported":0}}`
	msg := tryParseDocsUpdateMessage(text)
	if msg == nil || msg.Type != "docs_update" || msg.DocsUpdate == nil {
		t.Fatalf("message = %#v", msg)
	}
	if msg.DocsUpdate.DeckSlug != "migration" || msg.DocsUpdate.SlideIndex == nil || *msg.DocsUpdate.SlideIndex != 2 {
		t.Fatalf("update = %#v", msg.DocsUpdate)
	}
	if msg.DocsUpdate.Validation.Warnings != 1 || msg.DocsUpdate.PPTXCapability.Native != 2 {
		t.Fatalf("nested metadata = %#v", msg.DocsUpdate)
	}
}

func TestTryParseDocsUpdateMessageRejectsInvalidMarkers(t *testing.T) {
	for _, text := range []string{
		"ordinary text",
		docsUpdatePrefix + `{not-json}`,
		docsUpdatePrefix + `{"type":"slides"}`,
	} {
		if msg := tryParseDocsUpdateMessage(text); msg != nil {
			t.Fatalf("tryParseDocsUpdateMessage(%q) = %#v, want nil", text, msg)
		}
	}
}

func TestMaybeEmitDocsUpdate(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		result     map[string]any
		wantAction string
	}{
		{
			name:     "fill slides batch",
			toolName: "fill_slides",
			result: map[string]any{
				"deck":       map[string]any{"slug": "migration", "title": "Migration", "schemaVersion": 1},
				"slideCount": 4,
			},
			wantAction: slides.ActionSlideWritten,
		},
		{
			name:     "fill one slide",
			toolName: "fill_slide",
			result: map[string]any{
				"deck":       map[string]any{"slug": "migration", "title": "Migration", "schemaVersion": 1},
				"slideCount": 1,
				"position":   0,
			},
			wantAction: slides.ActionSlideWritten,
		},
		{
			name:     "write slide",
			toolName: "write_slide",
			result: map[string]any{
				"deck":       map[string]any{"slug": "migration", "title": "Migration", "schemaVersion": 1},
				"slide":      map[string]any{"position": 0, "title": "Overview", "schemaVersion": 1},
				"slideCount": 1,
			},
			wantAction: slides.ActionSlideWritten,
		},
		{
			name:     "show existing deck",
			toolName: "get_deck",
			result: map[string]any{
				"deck":       map[string]any{"slug": "migration", "title": "Migration", "schemaVersion": 1},
				"slideCount": float64(3),
			},
			wantAction: slides.ActionDeckViewed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newChatRunner("test-docs-update", studioChatUserID, true)
			ch := runner.Subscribe("test")
			defer runner.Unsubscribe("test")

			runner.maybeEmitDocsUpdate(nil, tt.toolName, tt.result)

			ev := <-ch
			if ev.Type != "docs_update" {
				t.Fatalf("event type = %q, want docs_update", ev.Type)
			}
			if ev.Data["deckSlug"] != "migration" || ev.Data["action"] != tt.wantAction {
				t.Fatalf("event data = %#v", ev.Data)
			}
		})
	}
}

func TestMaybeEmitDocsUpdateIgnoresFailedToolResult(t *testing.T) {
	runner := newChatRunner("test-docs-update-error", studioChatUserID, true)
	ch := runner.Subscribe("test")
	defer runner.Unsubscribe("test")

	runner.maybeEmitDocsUpdate(nil, "write_slide", map[string]any{"error": "invalid markup"})
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %#v", ev)
	default:
	}
}
