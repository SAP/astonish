package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

func TestBotHomeHelperInspectionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, state                                   string
		foreign, removeFailure, wantError, wantRemove bool
	}{
		{name: "success", state: `{"Running":false,"ExitCode":0}`, wantRemove: true},
		{name: "nonzero", state: `{"Running":false,"ExitCode":9}`, wantError: true, wantRemove: true},
		{name: "running", state: `{"Running":true,"ExitCode":0}`, wantError: true, wantRemove: true},
		{name: "missing-state", state: `null`, wantError: true, wantRemove: true},
		{name: "missing-code", state: `{"Running":false}`, wantError: true, wantRemove: true},
		{name: "foreign", state: `{"Running":false,"ExitCode":0}`, foreign: true, wantError: true},
		{name: "remove-failure", state: `{"Running":false,"ExitCode":0}`, removeFailure: true, wantError: true, wantRemove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "removed")
			wrapper := filepath.Join(dir, "docker")
			scope := "scope"
			if tc.foreign {
				scope = "foreign"
			}
			payload := `{"labels":{"` + botHomeReaderLabel + `":"owned","` + botHomeScopeLabel + `":"` + scope + `"},"state":` + tc.state + `}`
			status := "0"
			if tc.removeFailure {
				status = "9"
			}
			script := "#!/bin/sh\nif [ \"$1\" = inspect ]; then printf '%s\\n' '" + payload + "'; exit 0; fi\nprintf removed > '" + marker + "'\nexit " + status + "\n"
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: wrapper}}
			err := db.finishBotHomeHelper(t.Context(), "owned", "scope", true)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			_, err = os.Stat(marker)
			if (err == nil) != tc.wantRemove {
				t.Fatal("wrong cleanup ownership decision", err)
			}
		})
	}
}
func TestBotHomeHelperInspectionDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("physical Docker opt-in required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	trace := filepath.Join(t.TempDir(), "trace")
	wrapper := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + trace + "'\nexec docker \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	db := newBotRetainedSessionFixture(t, ctx, wrapper)
	home, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.DeleteBotHome(context.Background(), home); err != nil {
			t.Error(err)
		}
	})
	bindBotRetainedSessionFixture(t, ctx, db, home)
	taskPath := "tasks/" + uuid.NewString()
	if _, err := db.PublishBotHomeWorkspace(ctx, home, sandbox.BotHomeWorkspacePublishRequest{PublicationID: uuid.NewString(), TaskPath: taskPath, Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("fixture")}}}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"files", "workspace"} {
		if err := os.WriteFile(trace, nil, 0600); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for i := 0; i < 3; i++ {
			if mode == "files" {
				_, err = db.ListBotHomeFiles(ctx, home, ".", "", 10)
			} else {
				err = db.workspace(ctx, home, map[string]any{"op": "list", "path": taskPath, "after": "", "limit": 10}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		raw, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		inspections := strings.Count(string(raw), "inspect\n")
		t.Logf("%s operations=3 inspections=%d elapsed=%s", mode, inspections, time.Since(start))
		want := 6 // Files: state selection and pinned physical mount validation.
		if mode == "workspace" {
			want = 9
		} // Workspace also validates the retained-session mount recipe.
		if inspections != want {
			t.Errorf("%s trusted session inspections=%d want=%d", mode, inspections, want)
		}
		if strings.Contains(string(raw), "container\n") || strings.Contains(string(raw), "rm\n") {
			t.Error("routine operation created or removed compute")
		}
	}
}
