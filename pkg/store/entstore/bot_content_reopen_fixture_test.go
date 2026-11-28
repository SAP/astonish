package entstore

import (
	"os"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Reopen fixtures provision their retained root explicitly, just like production
// provisioning; constructing the codec must not create a missing Home implicitly.
func newReopenBotContentCodec(t *testing.T, root string, scope store.BotContentScope) *botContentCodec {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return newBotContentCodec(root, scope)
}
