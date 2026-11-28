package launcher

import (
	"context"

	"github.com/SAP/astonish/pkg/bot"
)

// installProgressBridge composes dispatch drain with the controller's existing
// authoritative boundary. Do not also call dispatch.Boundary: the API installed
// that same check as the controller callback. The API invokes this once more
// after persisting a terminal response, when there is no next model call.
func (b *botFactoryBinding) installProgressBridge() {
	b.progress = &botProgressBridge{}
	if controller := bot.ProgressControllerFromContext(b.owner); controller != nil {
		previous := controller.Boundary
		controller.Boundary = func(ctx context.Context) error {
			if previous == nil {
				return b.progress.boundary(b.bind(ctx))
			}
			return b.progress.drain(b.bind(ctx), previous)
		}
	}
}
