package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

// This launches Run, not a hand-built worker/router fixture. Every launch owns
// its configuration and databases; no existing Bot is resumed and no LLM is used.
func TestDaemonBotStartupOrder(t *testing.T) {
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.Error(w, "unexpected provider call", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": []float32{1, 0, 0, 0}}}})
	}))
	defer embeddings.Close()
	for _, mode := range []string{config.DaemonModeDefault, config.DaemonModeWorker, config.DaemonModeAPI} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/channels-%t", mode, enabled), func(t *testing.T) {
				root := t.TempDir()
				configDir := filepath.Join(root, "config", "astonish")
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				dataDir := filepath.Join(root, "data")
				if err := os.MkdirAll(dataDir, 0700); err != nil {
					t.Fatal(err)
				}
				_, db, err := entstore.NewPlatformServices(t.Context(), entstore.Config{DSN: filepath.Join(dataDir, "platform.db"), DataDir: dataDir})
				if err != nil {
					t.Fatal(err)
				}
				err = db.PlatformSettings().Save(t.Context(), &store.PlatformSettings{Channels: &store.PlatformChannelSettings{Telegram: &store.PlatformTelegramConfig{Enabled: enabled}}})
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := listener.Addr().(*net.TCPAddr).Port
				_ = listener.Close()
				cfg := fmt.Sprintf("storage:\n  backend: sqlite\n  sqlite:\n    data_dir: %q\n  auth:\n    mode: none\nmemory:\n  embedding:\n    provider: openai-compat\n    model: startup-fixture\n    base_url: %q\nsandbox:\n  enabled: true\n  backend: docker\ndaemon:\n  port: %d\nscheduler:\n  enabled: true\n", dataDir, embeddings.URL, port)
				if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0600); err != nil {
					t.Fatal(err)
				}
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"-test.run=^TestDaemonBotStartupHelper$", "-test.timeout=60s"}
				if binary := os.Getenv("ASTONISH_STARTUP_TEST_BINARY"); binary != "" {
					executable = binary
					args = []string{"daemon", "run"}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, args...)
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if strings.HasPrefix(key, "ASTONISH_") || strings.HasSuffix(key, "API_KEY") || key == "HOME" || key == "XDG_CONFIG_HOME" || key == "SUDO_USER" {
						continue
					}
					cmd.Env = append(cmd.Env, entry)
				}
				cmd.Env = append(cmd.Env, "ASTONISH_BOT_STARTUP_HELPER=1", "ASTONISH_MODE="+mode, "ASTONISH_LOG_DEST=stdout", "HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"))
				logs := &startupLogBuffer{}
				cmd.Stdout, cmd.Stderr = logs, logs
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				defer func() { _ = cmd.Process.Kill() }()
				client := &http.Client{Timeout: time.Second}
				ready := false
				for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); {
					select {
					case err := <-done:
						t.Fatalf("daemon exited: %v\n%s", err, logs.String())
					default:
					}
					if strings.Contains(logs.String(), "HTTP server listening") {
						resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/readyz", port))
						if err == nil {
							_ = resp.Body.Close()
							ready = resp.StatusCode == http.StatusOK
						}
						if ready {
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
				if !ready {
					t.Fatalf("HTTP not ready\n%s", logs.String())
				}
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("shutdown: %v\n%s", err, logs.String())
					}
				case <-time.After(15 * time.Second):
					t.Fatalf("shutdown timeout\n%s", logs.String())
				}
				out := logs.String()
				if enabled && !strings.Contains(out, "[channels] DB config: Telegram enabled") {
					t.Fatalf("enabled-channel startup branch was not exercised\n%s", out)
				}
				warm := strings.Index(out, "Studio chat agent pre-warmed successfully")
				worker := strings.Index(out, "Bots: durable worker started on shared Chat runtime")
				schedulerStarted := strings.Index(out, "[scheduler] Multi-tenant scheduler started")
				if mode != config.DaemonModeAPI && schedulerStarted < worker {
					t.Fatalf("scheduler started before Bot callbacks were installed\n%s", out)
				}
				serve := strings.Index(out, "startup phase=serve-handoff")
				if warm < 0 || serve < warm {
					t.Fatalf("Studio not warm before serving\n%s", out)
				}
				if mode == config.DaemonModeAPI {
					if worker >= 0 {
						t.Fatalf("API mode started worker\n%s", out)
					}
				} else if worker < warm || worker > serve || strings.Contains(out, "Bots: execution unavailable") {
					t.Fatalf("Bot must start after Studio warm and before HTTP, regardless of channels\n%s", out)
				}
				t.Logf("isolated production launch pid=%d port=%d mode=%s channels=%t: warm=%d worker=%d serve=%d clean shutdown", cmd.Process.Pid, port, mode, enabled, warm, worker, serve)
			})
		}
	}
}

func TestDaemonBotStartupHelper(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_STARTUP_HELPER") != "1" {
		t.Skip("subprocess only")
	}
	if err := Run(RunConfig{}); err != nil {
		t.Fatal(err)
	}
}

type startupLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *startupLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *startupLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
