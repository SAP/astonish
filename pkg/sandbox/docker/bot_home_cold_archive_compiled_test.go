package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// verifyDetachedCompiledArchive verifies storage transport after test-owned
// compute eviction. A merely stopped, still-attached container is not accepted
// by archiveDetached; this is not evidence for backup in that state. SQL restore
// ordering, schedule quiescence and live Kubernetes require separate evidence.
func verifyDetachedCompiledArchive(t *testing.T, db *DockerBackend, source *sandbox.BotHomeBinding, sessionID string, want []byte) {
	t.Helper()
	ctx := t.Context()
	target, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := db.DeleteBotHome(cleanup, target); err != nil {
			t.Error(err)
		}
	})
	barrier := func(c context.Context, h *sandbox.BotHomeBinding, commit func(context.Context) error) error {
		if !h.Equal(source) && !h.Equal(target) {
			return fmt.Errorf("archive target is not fixture-owned")
		}
		return commit(c)
	}
	var original bytes.Buffer
	manifest, err := db.ExportBotHomeArchive(ctx, source, &original, barrier)
	if err != nil {
		t.Fatalf("detached-compute export: %v", err)
	}
	if err := db.RestoreBotHomeArchive(ctx, target, manifest, bytes.NewReader(original.Bytes()), barrier); err != nil {
		t.Fatalf("fresh-Home cold restore: %v", err)
	}
	var restored bytes.Buffer
	again, err := db.ExportBotHomeArchive(ctx, target, &restored, barrier)
	if err != nil {
		t.Fatal(err)
	}
	again.Binding = manifest.Binding // Ownership is freshly issued; file identities are unchanged.
	beforeJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := json.Marshal(again)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) || !bytes.Equal(original.Bytes(), restored.Bytes()) {
		t.Fatalf("cold restore changed manifest or ordinary/private bytes: %v", err)
	}
	stream, err := db.OpenBotHomeFile(ctx, target, "proof.bin", store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("restored download differs: read=%v close=%v", readErr, closeErr)
	}
	remaining, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=name=^/"+containerName(sessionID)+"$")
	if err != nil || strings.TrimSpace(string(remaining)) != "" {
		t.Fatalf("archive recreated evicted compute: %s %v", remaining, err)
	}
	for _, h := range []*sandbox.BotHomeBinding{source, target} {
		for _, volume := range []string{h.Ref, workspaceVolume(h)} {
			attached, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+volume)
			if err != nil || strings.TrimSpace(string(attached)) != "" {
				t.Fatalf("cold archive leaked attachment or started target compute: %s %v", attached, err)
			}
		}
	}
	t.Logf("detached-compute cold export/restore: %d exact ordinary/private bytes, unchanged manifest, fresh target ownership, exact binary download, no resumed source or target compute", original.Len())
}
