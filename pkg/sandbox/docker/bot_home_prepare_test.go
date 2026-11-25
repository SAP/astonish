package docker

import (
	"github.com/SAP/astonish/pkg/sandbox"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBotSessionPreparationWarmConcurrentAndMissingRecipe(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "docker")
	// Any lifecycle/exec command on the already running session fails this fixture.
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\n[ \"$1\" = inspect ] || exit 99\nprintf 'running\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: runtime, UppersDir: dir}}
	b := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "user", BotID: "bot"}, Backend: sandbox.BackendKindDocker}
	b.Ref = botHomeVolume(b.Scope)
	if err := db.PrepareBotHomeSession(t.Context(), b, "session"); err == nil {
		t.Fatal("missing recipe accepted")
	}
	if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: "session", Type: sandbox.SessionTypeChat, BotHome: b, TemplateID: "tools", LayerChain: []string{sandbox.BaseTemplateID, "tools"}, Image: "test"})); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.PrepareBotHomeSession(t.Context(), b, "session"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := db.PrepareBotHomeSession(t.Context(), b, "different"); err == nil {
		t.Fatal("session binding changed")
	}
}
