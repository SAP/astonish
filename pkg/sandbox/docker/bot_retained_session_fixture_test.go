package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// newBotRetainedSessionFixture uses the same explicit binding and persisted
// launch recipe as Chat. It never substitutes host IO or a storage-only helper
// for workspace operations that require trusted session compute.
func newBotRetainedSessionFixture(t *testing.T, ctx context.Context, runtime string) *DockerBackend {
	t.Helper()
	image, layers, template := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS"), os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE")
	if image == "" || !filepath.IsAbs(layers) || template == "" {
		t.Fatal("explicit current image, absolute retained layers and provisioned tool template required")
	}
	for _, layer := range []string{sandbox.BaseTemplateID, template} {
		if info, err := os.Stat(filepath.Join(layers, layer, "rootfs")); err != nil || !info.IsDir() {
			t.Fatalf("provisioned layer %q unavailable: %v", layer, err)
		}
	}
	root, err := os.MkdirTemp(filepath.Dir(layers), "bot-retained-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := New(Config{ContainerRuntimePath: runtime, Sessions: sandbox.NewSessionRegistryFromStore(st), SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func bindBotRetainedSessionFixture(t *testing.T, ctx context.Context, db *DockerBackend, home *sandbox.BotHomeBinding) string {
	t.Helper()
	id := uuid.NewString()
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := db.DestroySession(c, id); err != nil {
			t.Error(err)
		}
	})
	if err := db.BindBotHomeSession(home, id); err != nil {
		t.Fatal(err)
	}
	template := os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE")
	if _, err := db.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: home.Scope.UserID, BotHome: home.Clone(), TemplateID: template, LayerChain: []string{sandbox.BaseTemplateID, template}}); err != nil {
		t.Fatal(err)
	}
	return id
}
