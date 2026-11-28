package api

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotOperationDecisionRejectsAuthority(t *testing.T) {
	for _, body := range []string{`{"mandate_ref":{}}`, `{"principal":{}}`, `{"payload":{}}`} {
		w := httptest.NewRecorder()
		BotOperationDecisionHandler(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("authority body %s accepted: %d", body, w.Code)
		}
	}
}
func TestBotOperationWakeupsPreserveNotifier(t *testing.T) {
	count := 0
	ctx := store.WithBotOperationNotifier(context.Background(), func(context.Context, store.BotOperationView) { count++ })
	wrapped := WithBotOperationWakeups(ctx)
	store.BotOperationNotifierFromContext(wrapped)(wrapped, store.BotOperationView{})
	if count != 1 {
		t.Fatal("existing notifier lost")
	}
}
