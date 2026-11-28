package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotInteractiveAdmissionFailsClosed(t *testing.T) {
	policy := store.BotMandatePolicyResolver(func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{}, store.ErrBotUnsupported
	})
	for _, svc := range []*store.Services{nil, {}, {BotInteractiveReady: true}, {BotTurnRegistry: execution.NewBotTurnAuthorizationRegistry(1)}, {BotMandatePolicy: policy}} {
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
