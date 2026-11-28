package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

type boundaryOnceProbe struct {
	progressDispatchProbe
	calls int
	err   error
}

func (p *boundaryOnceProbe) Boundary(context.Context) error { p.calls++; return p.err }

func TestBotProgressInstalledBoundaryExactlyOnce(t *testing.T) {
	p := &boundaryOnceProbe{}
	controller := &bot.ProgressController{Boundary: p.Boundary}
	owner := bot.WithProgressController(bot.WithProgressDispatch(context.Background(), p), controller)
	b := &botFactoryBinding{owner: owner}
	b.installProgressBridge()
	settled := 0
	b.progress.pending = append(b.progress.pending, func(context.Context) error { settled++; return nil })
	if err := controller.Boundary(owner); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || settled != 1 {
		t.Fatalf("boundary=%d settlement=%d; want exactly one each", p.calls, settled)
	}
	p.err = store.ErrBotForbidden
	if err := controller.Boundary(owner); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revocation lost: %v", err)
	}
	if p.calls != 2 || settled != 1 {
		t.Fatalf("subsequent boundary cached authority or replayed settlement: %d %d", p.calls, settled)
	}
	b.progress.active = 1
	if err := controller.Boundary(owner); !errors.Is(err, bot.ErrProgressBoundaryUnsafe) {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatal("authorized while tool was active")
	}
}

func TestBotProgressSettlementReadOperationLifetime(t *testing.T) {
	var retained context.Context
	p := &botProgressBridge{}
	p.pending = append(p.pending, func(ctx context.Context) error {
		retained = ctx
		if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
			t.Fatal("settlement missing operation scope")
		}
		return nil
	})
	if err := p.drain(context.Background(), func(ctx context.Context) error {
		if ctx != retained {
			t.Fatal("settlement and boundary did not share operation")
		}
		return store.ErrBotForbidden
	}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
		t.Fatal("operation leaked beyond failed boundary")
	}
}
