package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestBotFileSessionDoesNotResumeFromCodec(t *testing.T) {
	for _, state := range []string{"paused", "exited"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			runtime := filepath.Join(dir, "docker")
			if err := os.WriteFile(runtime, []byte("#!/bin/sh\nif [ \"$1\" != inspect ]; then exit 99; fi\nprintf '%s\\n' "+quoteShell(state)+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: runtime, UppersDir: dir}}
			b := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "user", BotID: "bot"}, Backend: sandbox.BackendKindDocker}
			b.Ref = botHomeVolume(b.Scope)
			if err := db.BindBotHomeSession(b, "session"); err != nil {
				t.Fatal(err)
			}
			if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: "session", Type: sandbox.SessionTypeChat, BotHome: b, TemplateID: "tools", LayerChain: []string{sandbox.BaseTemplateID, "tools"}, Image: "test"})); err != nil {
				t.Fatal(err)
			}
			_, err := db.botFileSession(context.Background(), b)
			if err == nil || !strings.Contains(err.Error(), "trusted lifecycle preparation") {
				t.Fatalf("codec attempted lifecycle recovery: %v", err)
			}
		})
	}
}

func TestBotFileSessionMountsExact(t *testing.T) {
	b := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "user", BotID: "bot"}, Backend: sandbox.BackendKindDocker}
	b.Ref = botHomeVolume(b.Scope)
	type mount struct {
		Type, Name, Destination string
		RW                      bool
	}
	home := mount{"volume", b.Ref, botHomeMount, true}
	private := mount{"volume", workspaceVolume(b), botPrivateMount, true}
	for _, tc := range []struct {
		name   string
		mounts []mount
		ok     bool
	}{
		{"exact", []mount{home, private}, true},
		{"missing-home", []mount{private}, false},
		{"missing-private", []mount{home}, false},
		{"wrong-home", []mount{{"volume", "other", botHomeMount, true}, private}, false},
		{"readonly-home", []mount{{"volume", b.Ref, botHomeMount, false}, private}, false},
		{"duplicate-home", []mount{home, home, private}, false},
		{"wrong-private", []mount{home, {"volume", "other", botPrivateMount, true}}, false},
		{"bind-private", []mount{home, {"bind", workspaceVolume(b), botPrivateMount, true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.mounts)
			if err != nil {
				t.Fatal(err)
			}
			runtime := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' "+quoteShell(string(raw))+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: runtime}}
			err = db.validateBotFileMount(context.Background(), "test", b)
			if (err == nil) != tc.ok {
				t.Fatalf("valid=%v err=%v", tc.ok, err)
			}
		})
	}
}
