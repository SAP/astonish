package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/store/entstore"
	"testing"
)

func TestBotProgressDrainBorrowsBookkeepingScope(t *testing.T) {
	ctx, end := entstore.WithBotContentReadOperation(context.Background())
	defer end()
	p := &botProgressBridge{}
	if err := p.drain(ctx, func(seen context.Context) error {
		if seen != ctx {
			t.Fatal("drain replaced shared bookkeeping scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
		t.Fatal("borrower closed bookkeeping owner's scope")
	}
}
