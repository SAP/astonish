package launcher

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotKnowledgeWorkspaceNoHostFallback(t *testing.T) {
	w := &BotKnowledgeWorkspace{}
	if _, e := w.Snapshot(context.Background(), store.BotID{}); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatalf("snapshot fallback %v", e)
	}
	if _, e := w.Publish(context.Background(), store.BotKnowledgeExtractionLease{}, store.BotKnowledgeExtraction{}, func(context.Context) error { return nil }); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatalf("publish fallback %v", e)
	}
	if e := w.Verify(context.Background(), store.BotKnowledgeExtractionLease{}, store.BotKnowledgeExtraction{}, nil); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatalf("verify fallback %v", e)
	}
}
