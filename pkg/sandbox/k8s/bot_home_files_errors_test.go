package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Force both sides of the exec-status/protocol-error ordering without sleeps or
// scheduler-dependent repeated downloads. A successful transport exit must never
// erase a locally detected framing error, including when finish already ran.
func TestBotHomeFilesProtocolErrorOrdering(t *testing.T) {
	for _, protocol := range []string{"overlong-body", "oversized-metadata"} {
		for _, order := range []string{"status-ready", "finish-first", "cancel-before-status"} {
			for _, failedExit := range []bool{false, true} {
				name := protocol + "/" + order + "/success-exit"
				var terminal error
				if failedExit {
					name = protocol + "/" + order + "/failed-exit"
					terminal = errors.New("exec terminal sentinel")
				}
				t.Run(name, func(t *testing.T) {
					parent, stopParent := context.WithCancel(t.Context())
					defer stopParent()
					operation, cancel := context.WithCancel(parent)
					defer cancel()
					// No backend is available: session-owned cleanup must not try
					// to delete the pod, release its admission or replay the exec.
					p := &homeFileProcess{sessionOwned: true, cancel: cancel, done: make(chan error, 1), remaining: 3}
					wire, want := "abcd", "file stream exceeds range"
					if protocol == "oversized-metadata" {
						wire, want = strings.Repeat(" ", (4<<20)+1)+"\n", "file metadata exceeds bound"
					}
					p.reader = bufio.NewReader(strings.NewReader(wire))
					if order != "cancel-before-status" {
						p.done <- terminal
						if order == "finish-first" {
							// Model protocol bytes buffered before another waiter
							// finishes the process and closes its output pipe.
							if err := p.finish(false); !errors.Is(err, terminal) {
								t.Fatalf("finish lost terminal status: %v", err)
							}
						}
					}
					result := make(chan error, 1)
					go func() {
						if protocol == "oversized-metadata" {
							var entry store.BotFileEntry
							result <- p.response(&entry)
							return
						}
						_, err := io.Copy(io.Discard, p)
						result <- err
					}()
					if order == "cancel-before-status" {
						select {
						case <-operation.Done():
						case <-time.After(5 * time.Second):
							p.done <- terminal
							t.Fatal("protocol rejection did not cancel its operation")
						}
						select {
						case err := <-result:
							p.done <- terminal
							t.Fatalf("returned before joining executor status: %v", err)
						default:
						}
						p.done <- terminal
					}
					select {
					case err := <-result:
						if err == nil || !strings.Contains(err.Error(), want) {
							t.Fatalf("protocol error lost after %s: %v", order, err)
						}
						if terminal != nil && !errors.Is(err, terminal) {
							t.Fatalf("terminal status discarded: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("protocol rejection did not join")
					}
					if operation.Err() == nil || parent.Err() != nil {
						t.Fatalf("wrong cancellation scope: operation=%v parent=%v", operation.Err(), parent.Err())
					}
					if err := p.Close(); !errors.Is(err, terminal) {
						t.Fatalf("idempotent close changed terminal status: %v", err)
					}
				})
			}
		}
	}
}

func TestBotHomeFilesMissingAndUnsafe(t *testing.T) {
	b, h, root := localHomeFilesBackend(t)
	if err := os.WriteFile(filepath.Join(root, "data/existing.md"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "data/escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"SOUL.md", "missing/SOUL.md", "escape/missing", "escape"} {
		t.Run(path, func(t *testing.T) {
			want := store.ErrBotNotFound
			if strings.HasPrefix(path, "escape") {
				want = store.ErrBotForbidden
			}
			if _, err := b.StatBotHomeFile(t.Context(), h, path); !errors.Is(err, want) {
				t.Fatalf("stat got %v want %v", err, want)
			}
			if _, err := b.OpenBotHomeFile(t.Context(), h, path, store.BotFileRange{}); !errors.Is(err, want) {
				t.Fatalf("open got %v want %v", err, want)
			}
		})
	}
	if entry, err := b.StatBotHomeFile(t.Context(), h, "existing.md"); err != nil || entry.SizeBytes != 2 {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StatBotHomeFile(t.Context(), h, "SOUL.md"); err == nil || errors.Is(err, store.ErrBotNotFound) || !strings.Contains(err.Error(), "open retained file roots") {
		t.Fatalf("missing physical root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
		t.Fatal("read provisioned root")
	}
	if len(homeReservations(t, b, h)) != 0 {
		t.Fatal("helper reservation leaked")
	}
}

func TestBotHomeFilesErrorResponseJoin(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		want       error
	}{
		{"missing", "{\"error\":{\"code\":\"not_found\"}}\n", store.ErrBotNotFound},
		{"malformed", "{bad}\n", nil}, {"truncated", "{\"path\":", nil},
		{"success-terminal", "{\"path\":\"SOUL.md\"}\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, h := homeBackend(t)
			workspaceFake(t, b)
			terminal := errors.New("terminal sentinel")
			b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					if _, err := bufio.NewReader(o.Stdin).ReadString('\n'); err != nil {
						return err
					}
					_, _ = io.WriteString(o.Stdout, tc.wire)
					if tc.want != nil { // Typed errors must close stdin normally, not cancel before exit.
						_, _ = io.Copy(io.Discard, o.Stdin)
						if ctx.Err() != nil {
							return errors.New("premature cancellation")
						}
					}
					_, _ = io.WriteString(o.Stderr, "helper diagnostic")
					return terminal
				}), nil
			}
			_, err := b.StatBotHomeFile(t.Context(), h, "SOUL.md")
			if !errors.Is(err, terminal) || !strings.Contains(err.Error(), "helper diagnostic") {
				t.Fatalf("terminal/stderr discarded: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("classification discarded: %v", err)
			}
		})
	}
}

func TestBotHomeFilesRejectMalformedRequestBeforeAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    map[string]any
	}{
		{"missing-operation", map[string]any{}},
		{"unknown-operation", map[string]any{"op": "remove", "path": "safe"}},
		{"missing-path", map[string]any{"op": "stat"}},
		{"non-string-path", map[string]any{"op": "open", "path": 1}},
		{"absolute", map[string]any{"op": "stat", "path": "/etc/passwd"}},
		{"traversal", map[string]any{"op": "open", "path": "../.owner"}},
		{"alias", map[string]any{"op": "list", "path": "a//b"}},
		{"root-file", map[string]any{"op": "stat", "path": "."}},
		{"bad-cursor", map[string]any{"op": "list", "path": ".", "after": "../foreign"}},
		{"non-string-cursor", map[string]any{"op": "list", "path": ".", "after": 1}},
		{"nil-cursor", map[string]any{"op": "list", "path": ".", "after": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, h := homeBackend(t)
			client := workspaceFake(t, b)
			client.ClearActions()
			b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
				return nil, errors.New("unexpected exec dispatch")
			}
			// Malformed input must be rejected before attachment admission,
			// provisioning or even runtime availability.
			p, err := b.startHomeFiles(t.Context(), h, tc.q)
			if p != nil {
				_ = p.Close()
			}
			if !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("got %v, want invalid request", err)
			}
			if len(client.Actions()) != 0 {
				t.Fatalf("malformed request reached Kubernetes: %v", client.Actions())
			}
		})
	}
}

func TestBotHomeFilesRootIdentity(t *testing.T) {
	for _, mode := range []string{"wrong-owner", "owner-symlink", "owner-hardlink", "data-symlink", "private-symlink"} {
		t.Run(mode, func(t *testing.T) {
			b, h, root := localHomeFilesBackend(t)
			outside := t.TempDir()
			owner := filepath.Join(root, ".owner")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.WriteFile(filepath.Join(outside, "file.txt"), []byte("outside"), 0600))
			switch mode {
			case "wrong-owner":
				must(os.WriteFile(owner, []byte("foreign\n"), 0600))
			case "owner-symlink":
				must(os.WriteFile(filepath.Join(outside, "owner"), []byte(h.Ref+"\n"), 0600))
				must(os.Remove(owner))
				must(os.Symlink(filepath.Join(outside, "owner"), owner))
			case "owner-hardlink":
				must(os.Link(owner, filepath.Join(outside, "owner")))
			case "data-symlink":
				must(os.Remove(filepath.Join(root, "data")))
				must(os.Symlink(outside, filepath.Join(root, "data")))
			case "private-symlink":
				must(os.Symlink(outside, filepath.Join(root, "workspace-private")))
			}
			var err error
			approved := false
			if mode == "private-symlink" {
				_, err = b.PutBotContent(t.Context(), h, store.BotContentArtifact, 1, strings.NewReader("x"), 1, func(f func() error) error {
					approved = true
					return f()
				})
			} else {
				_, err = b.StatBotHomeFile(t.Context(), h, "file.txt")
			}
			if err == nil || approved {
				t.Fatalf("unsafe physical root accepted: approved=%v err=%v", approved, err)
			}
			entries, err := os.ReadDir(outside)
			must(err)
			want := 1
			if mode == "owner-symlink" || mode == "owner-hardlink" {
				want++
			}
			if len(entries) != want || len(homeReservations(t, b, h)) != 0 {
				t.Fatal("outside root mutated or attachment leaked")
			}
		})
	}
}

func TestBotHomeFilesMetadataLineParity(t *testing.T) {
	p := &homeFileProcess{reader: bufio.NewReader(strings.NewReader("{\"error\":{\"code\":\"not_found\"}}\n"))}
	var entry json.RawMessage
	if err := p.line(&entry); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatal(err)
	}
}
