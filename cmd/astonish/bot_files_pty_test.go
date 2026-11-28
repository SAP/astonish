package astonish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

// Native Execute/Bubble Tea and authenticated HTTP only. It intentionally does
// not claim worker, provider, Docker or Kubernetes acceptance.
func TestBotFileWorkspacePTYAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SUDO_USER", "")
			if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
				t.Fatal(err)
			}
			b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "File workspace", Scope: scope, Status: store.BotPaused, Version: 1, Revision: 1}
			b.Home.ID = uuid.New()
			run := uuid.New()
			binary := bytes.Repeat([]byte{0, 255, 42, 5}, (17<<20)/4)
			digest := sha256.Sum256(binary)
			p := store.BotFilePublication{SchemaVersion: 1, BotID: b.ID, HomeID: b.Home.ID, HomeSessionID: b.HomeSessionID, RunID: run, ReportSequence: 1, EventID: store.BotPublicationEventID(run, 1), Summary: "Workspace delivery ready", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: b.Home.ID, RelativePath: "report.md", SHA256: strings.Repeat("a", 64), Preview: store.BotFilePreviewText}, {ID: uuid.New(), HomeID: b.Home.ID, RelativePath: "large.bin", SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(binary)), Preview: store.BotFilePreviewDownload}}}
			base := "/api/studio/bots/" + b.ID.String()
			var mutations, downloads, subscriptions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer files-token" {
					http.Error(w, "auth", 401)
					return
				}
				if strings.HasPrefix(r.URL.Path, base) && r.URL.Query().Get("scope") != string(scope) {
					t.Error("lost scope")
				}
				if r.Method != "GET" {
					mutations.Add(1)
					http.Error(w, "no mutations", 405)
					return
				}
				write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
				switch {
				case r.URL.Path == base:
					write(b)
				case r.URL.Path == base+"/snapshot":
					write(store.BotSnapshot{Bot: b})
				case r.URL.Path == base+"/runs":
					write(map[string]any{"runs": []any{}})
				case r.URL.Path == base+"/files":
					write(store.BotFilePage{Entries: []store.BotFileEntry{{Path: "MEMORY.md", Kind: store.BotFileRegular, Preview: store.BotFilePreviewText, Freshness: "v1"}}})
				case strings.HasSuffix(r.URL.Path, "/preview"):
					if r.URL.Query().Get("path") == "MEMORY.md" {
						write(client.BotFilePreview{Entry: store.BotFileEntry{Freshness: "v1"}, Content: "MEMORY retained goals and paths"})
					} else {
						write(client.BotFilePreview{Content: "REPORT immutable source preview"})
					}
				case strings.HasSuffix(r.URL.Path, "/download"):
					if !strings.Contains(r.URL.Path, "/artifacts/"+p.Artifacts[1].ID.String()+"/") {
						t.Error("not immutable artifact")
					}
					downloads.Add(1)
					w.Header().Set("Content-Length", fmt.Sprint(len(binary)))
					_, _ = w.Write(binary)
				case strings.HasSuffix(r.URL.Path, "/events"):
					n := subscriptions.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					// Repeat the durable publication under a fresh cursor as well as reconnect:
					// the transcript reducer must deduplicate by publication identity.
					writeBotCanonicalPTYFrame(t, w, b.HomeSessionID, run, 1, p.EventID, store.BotFilePublicationKey, p)
					writeBotCanonicalPTYFrame(t, w, b.HomeSessionID, run, 2, p.EventID, store.BotFilePublicationKey, p)
					if n == 1 {
						return
					}
					<-r.Context().Done()
				default:
					write(map[string]any{})
				}
			}))
			defer server.Close()
			if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
				t.Fatal(err)
			}
			tokens, err := client.NewTokenStore()
			if err != nil {
				t.Fatal(err)
			}
			if err = tokens.Save(&client.Tokens{AccessToken: "files-token"}); err != nil {
				t.Fatal(err)
			}
			for restart := 0; restart < 2; restart++ {
				cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat", "--bot", b.ID.String(), "--bot-scope", string(scope))
				cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
				pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 42, Cols: 150})
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				var output strings.Builder
				done := make(chan struct{})
				go func() {
					defer close(done)
					var terminal botPTYTerminal
					buf := make([]byte, 8192)
					for {
						n, e := pt.Read(buf)
						if n > 0 {
							_ = terminal.respond(pt, buf[:n])
							mu.Lock()
							output.Write(buf[:n])
							mu.Unlock()
						}
						if e != nil {
							return
						}
					}
				}()
				stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = pt.Close(); <-done }
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = pt.Close() })
				wait := func(text string) {
					t.Helper()
					until := time.Now().Add(12 * time.Second)
					for time.Now().Before(until) {
						mu.Lock()
						s := ansi.Strip(output.String())
						mu.Unlock()
						if strings.Contains(s, text) {
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
					mu.Lock()
					s := ansi.Strip(output.String())
					mu.Unlock()
					t.Fatalf("missing %q: %s", text, s)
				}
				send := func(text string) {
					t.Helper()
					if _, err := io.WriteString(pt, text); err != nil {
						t.Fatal(err)
					}
					time.Sleep(200 * time.Millisecond)
				}
				wait("Workspace delivery ready")
				wait("File: report.md")
				if restart == 1 {
					stop()
					break
				}
				send("/bots files\r")
				wait("MEMORY.md")
				send("\r")
				wait("MEMORY retained goals and paths")
				send("\x1b")
				send("p")
				wait("immutable published files")
				send("\r")
				wait("REPORT immutable source preview")
				send("\x1b")
				send("\x1b[B")
				send("\r")
				wait("Save to a new local destination")
				if downloads.Load() != 0 {
					t.Fatal("download before consent")
				}
				send("\x1b")
				send("d")
				wait("Save to a new local destination")
				dest := filepath.Join(home, "download.bin")
				send(dest)
				send("\r")
				wait("Saved to ")
				got, err := os.ReadFile(dest)
				if err != nil || !bytes.Equal(got, binary) {
					t.Fatal("large byte-exact save failed", err)
				}
				send("\x1b")
				time.Sleep(400 * time.Millisecond)
				mu.Lock()
				text := ansi.Strip(output.String())
				mu.Unlock()
				if strings.Contains(text, "shell_command") || strings.Contains(text, "FunctionCall") {
					t.Fatal("raw traces exposed")
				}
				stop()
			}
			if mutations.Load() != 0 || downloads.Load() != 1 || subscriptions.Load() < 3 {
				t.Fatalf("mutations=%d downloads=%d subscriptions=%d", mutations.Load(), downloads.Load(), subscriptions.Load())
			}
		})
	}
}
