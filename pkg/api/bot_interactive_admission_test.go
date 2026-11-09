package api

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"net/http/httptest"
	"testing"
)

func TestBotInteractiveAdmissionFailsClosed(t *testing.T) {
	for _, svc := range []*store.Services{nil, {}, {BotInteractiveReady: true}, {BotTurnRegistry: execution.NewBotTurnAuthorizationRegistry(1)}} {
		ctx := context.Background()
		if svc != nil {
			ctx = store.WithServices(ctx, svc)
		}
		req := httptest.NewRequest("POST", "/api/studio/bots/id/messages", nil).WithContext(ctx)
		if _, err := botInteractiveAdmissionContext(req); err == nil {
			t.Fatal("missing runtime fell back to legacy admission")
		}
	}
}
