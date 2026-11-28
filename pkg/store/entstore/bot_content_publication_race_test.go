package entstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPublicationLockConcurrentFirstOpen(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 100; i++ {
		scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), IntakeID: uuid.New()}
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d, err := newBotDiskContent(root, scope)
				if err != nil {
					t.Error(err)
					return
				}
				defer d.Close()
				f, err := d.lockPublication(context.Background())
				if err != nil {
					if !errors.Is(err, store.ErrBotBusy) {
						t.Error(err)
					}
					return
				}
				f.Close()
			}()
		}
		wg.Wait()
	}
}
