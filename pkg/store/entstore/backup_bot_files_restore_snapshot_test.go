package entstore

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotFileRestoreFreezesValidatedArchive(t *testing.T) {
	archive, opts, refs := botRestoreFixture(t)
	target := opts.Targets[refs[0].Scope]
	replaced := false
	target.authorize = func(context.Context) error {
		if !replaced {
			replaced = true
			// First target authorization is after snapshot validation. A hostile
			// replacement of the caller's path must not affect later extraction passes.
			return os.WriteFile(archive, []byte("not an archive anymore"), 0600)
		}
		return nil
	}
	mapping, err := restoreBotFileContent(t.Context(), archive, opts)
	if err != nil || !replaced || len(mapping) != len(refs) {
		t.Fatalf("snapshot restore: %v", err)
	}
	for _, ref := range refs {
		stream, e := target.Open(t.Context(), ref, store.BotFileRange{})
		if e != nil {
			t.Fatal(e)
		}
		n, e := io.Copy(io.Discard, stream)
		if e != nil || n != ref.SizeBytes {
			t.Fatalf("changed archive bytes: %d %v", n, e)
		}
		if e = stream.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
