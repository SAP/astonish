package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
)

// installProgressBridge composes the dispatch drain into the controller's trusted
// boundary before any model/tool work starts. The API must invoke controller.Boundary
// once more after persisting the terminal response (there is no next model call).
func (b *botFactoryBinding) installProgressBridge() {
	b.progress = &botProgressBridge{}
	if controller := bot.ProgressControllerFromContext(b.owner); controller != nil {
		previous := controller.Boundary
		controller.Boundary = func(ctx context.Context) error {
			if err := b.progress.boundary(b.bind(ctx)); err != nil {
				return err
			}
			if previous != nil {
				return previous(ctx)
			}
			return nil
		}
	}
}
