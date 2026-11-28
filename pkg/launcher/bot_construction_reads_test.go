package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store/entstore"
)

func TestBotFactoryConstructionReadsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[fail], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			// A generic ambient read operation is not permission to extend the
			// factory lifetime. Only the daemon's typed startup scope is borrowed.
			var endAmbient func()
			f.ctx, endAmbient = entstore.WithBotContentReadOperation(f.ctx)
			defer endAmbient()
			var retained context.Context
			load := f.cfg.LoadScope
			f.cfg.LoadScope = func(ctx context.Context, req bot.RunRequest) (*BotChatScope, error) {
				retained = ctx
				if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
					t.Fatal("construction owner lost immutable read operation")
				}
				if fail {
					return nil, errors.New("scope unavailable")
				}
				return load(ctx, req)
			}
			resources, err := f.construct(t)
			if resources != nil {
				defer resources.Cleanup()
			}
			if (err != nil) != fail {
				t.Fatalf("construction: %v", err)
			}
			if retained == nil || entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
				t.Fatal("construction operation survived return")
			}
		})
	}
}
