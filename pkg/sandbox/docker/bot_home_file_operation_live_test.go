package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Real Docker transport, not the runtime-double test: every traced invocation
// delegates to Docker and resources are confined to disposable UUID volumes.
func TestHomeFileTransactionHelperDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	runtime, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace")
	wrapper := filepath.Join(dir, "docker")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" >> %q\nexec %q \"$@\"\n", trace, runtime)), 0700); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ASTONISH_SANDBOX_IMAGE") == "" {
		t.Fatal("explicit current compiled runtime image required")
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: wrapper, SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	b, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if e := db.DeleteBotHome(c, b); e != nil {
			t.Error(e)
		}
	})
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprint(scoped), func(t *testing.T) {
			if err := os.WriteFile(trace, nil, 0600); err != nil {
				t.Fatal(err)
			}
			c := ctx
			end := func() error { return nil }
			if scoped {
				c, end = sandbox.WithBotHomeFileOperation(ctx)
			}
			defer end()
			start := time.Now()
			for i := 0; i < 3; i++ {
				if _, err := db.ListBotHomeFiles(c, b, ".", "", 10); err != nil {
					t.Fatal(err)
				}
			}
			if scoped {
				raw, e := runDocker(ctx, runtime, "ps", "-aq", "--filter=volume="+b.Ref)
				if e != nil {
					t.Fatal(e)
				}
				ids := strings.Fields(string(raw))
				if len(ids) != 1 {
					t.Fatalf("scoped attached helpers=%v", ids)
				}
				raw, e = runDocker(ctx, runtime, "inspect", ids[0])
				if e != nil {
					t.Fatal(e)
				}
				var rows []struct {
					Config     struct{ Cmd []string }
					HostConfig struct {
						NetworkMode    string
						ReadonlyRootfs bool
					}
					Mounts []struct {
						Type, Destination string
						RW                bool
					}
					State struct{ Running bool }
				}
				if e = json.Unmarshal(raw, &rows); e != nil || len(rows) != 1 {
					t.Fatalf("inspect %s %v", raw, e)
				}
				r := rows[0]
				if !r.State.Running || !r.HostConfig.ReadonlyRootfs || r.HostConfig.NetworkMode != "none" || len(r.Mounts) != 2 || strings.Join(r.Config.Cmd, " ") != "infinity" {
					t.Fatalf("compiled attachment confinement: %s", raw)
				}
				mounts := map[string]string{botHomeMount: "volume", botPrivateMount: "tmpfs"}
				for _, mount := range r.Mounts {
					if mounts[mount.Destination] != mount.Type || mount.RW {
						t.Fatalf("compiled attachment mount confinement: %s", raw)
					}
					delete(mounts, mount.Destination)
				}
				if len(mounts) != 0 {
					t.Fatalf("compiled attachment missing mounts: %s", raw)
				}
				if e = db.DeleteBotHome(ctx, b); e == nil {
					t.Fatal("purged attached transaction helper")
				}
			}
			if err := end(); err != nil {
				t.Fatal(err)
			}
			raw, e := os.ReadFile(trace)
			if e != nil {
				t.Fatal(e)
			}
			creates := strings.Count(string(raw), "container\n")
			removes := strings.Count(string(raw), "rm\n")
			want := 3
			if scoped {
				want = 1
			}
			if creates != want || removes != want {
				t.Fatalf("creates=%d removes=%d want=%d", creates, removes, want)
			}
			t.Logf("physical scoped=%t creates=%d removes=%d elapsed=%s", scoped, creates, removes, time.Since(start))
		})
	}
	c, end := sandbox.WithBotHomeFileOperation(ctx)
	defer end()
	if _, err := db.WriteBotHomeFile(c, b, store.BotFileWrite{Path: "fixture", Body: strings.NewReader("safe"), SizeBytes: 4, CreateOnly: true, BeforeCommit: func(commit func() error) error { return commit() }}); err != nil {
		t.Fatal(err)
	}
	stream, err := db.OpenBotHomeFile(c, b, "fixture", store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream.ReadCloser)
	closeErr := stream.ReadCloser.Close()
	if err != nil || closeErr != nil || string(data) != "safe" {
		t.Fatalf("read=%q err=%v close=%v", data, err, closeErr)
	}
	if _, err := db.StatBotHomeFile(c, b, "missing"); err == nil {
		t.Fatal("lost terminal missing error")
	}
	if _, err := db.ListBotHomeFiles(c, b, ".", "", 10); err != nil {
		t.Fatal(err)
	}
	if err := end(); err != nil {
		t.Fatal(err)
	}
	// End must cancel and join an unread active exec, then remove its helper.
	activeCtx, activeEnd := sandbox.WithBotHomeFileOperation(ctx)
	activeStream, err := db.OpenBotHomeFile(activeCtx, b, "fixture", store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	if err := activeEnd(); err == nil {
		t.Fatal("active borrower was not reported")
	}
	_ = activeStream.Close()
	raw, err := runDocker(ctx, runtime, "ps", "-aq", "--filter=volume="+b.Ref)
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		t.Fatalf("helper leak %s %v", raw, err)
	}
}
