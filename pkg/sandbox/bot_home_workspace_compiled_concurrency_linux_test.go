//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Every host handoff must release the legacy lock and fence concurrent changes,
// including the scratch handoff and the final (already populated) restore fence.
func TestCompiledWorkspaceHostExchangeConcurrency(t *testing.T) {
	for _, mode := range []string{"write", "publish-first", "publish-final", "knowledge-publish", "knowledge-retry", "knowledge-verify", "script", "scratch", "restore-first", "restore-final"} {
		for _, mutate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mutate=%t", mode, mutate), func(t *testing.T) {
				root := knowledgeCompiledRoot(t)
				binding := "scope"
				if strings.HasPrefix(mode, "restore") {
					binding = strings.Repeat("a", 64)
				}
				call := func(q map[string]any, suffix string, out io.Writer) error {
					q["binding"] = binding
					raw, _ := json.Marshal(q)
					return RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\n"+suffix), out)
				}
				pub := "11111111-1111-4111-8111-111111111111"
				script := BotHomeWorkspaceFile{Path: "scripts/run.py", Data: []byte("print('ok')"), Executable: true}
				publication := BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: pub, Files: []BotHomeWorkspaceFile{script}}
				if err := call(map[string]any{"op": "publish", "request": publication}, "commit\ncommit\n", io.Discard); err != nil {
					t.Fatal(err)
				}
				if err := call(map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("old")}, "commit\n", io.Discard); err != nil {
					t.Fatal(err)
				}
				q := map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("outer")}
				suffix := "commit\ncommit\n"
				target := 1
				switch mode {
				case "publish-first", "publish-final":
					publication.TaskPath = "tasks/other"
					publication.PublicationID = "other"
					q = map[string]any{"op": "publish", "request": publication}
					if mode == "publish-final" {
						target = 2
					}
				case "knowledge-publish", "knowledge-retry", "knowledge-verify":
					record := knowledgeCompiledRecord("knowledge")
					if mode != "knowledge-publish" {
						if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, record, "commit\n", io.Discard); err != nil {
							t.Fatal(err)
						}
					}
					op := workspaceKnowledgePublish
					if mode == "knowledge-verify" {
						op = workspaceKnowledgeVerify
					}
					q = map[string]any{"op": op, "record": record}
				case "script", "scratch":
					q = map[string]any{"op": "script", "path": "tasks/task", "publication": pub, "script": script.Path, "digest": script.Digest()}
					suffix = "{\"exit_code\":0,\"files\":[]}\ncommit\n"
				case "restore-first", "restore-final":
					var out bytes.Buffer
					if err := call(map[string]any{"op": "snapshot", "path": "workspace", "limit": 16 << 20}, "", &out); err != nil {
						t.Fatal(err)
					}
					var snapshot BotHomeWorkspaceSnapshot
					if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
						t.Fatal(err)
					}
					root = knowledgeCompiledRoot(t)
					q = map[string]any{"op": "restore", "snapshot": snapshot}
					if mode == "restore-final" {
						target = 2
					}
				}
				entered := false
				ready := 0
				out := workspaceExchangeWriter{hook: func(b []byte) {
					isScratch := bytes.HasPrefix(b, []byte("WORKSPACE_SCRIPT "))
					isReady := string(b) == "WORKSPACE_READY\n"
					if isReady {
						ready++
					}
					if mode == "scratch" && !isScratch || mode != "scratch" && (!isReady || ready != target) {
						return
					}
					entered = true
					// Nonblocking acquisition fails immediately on the original deadlock,
					// avoiding an orphan goroutine or hanging the unit test process.
					lock, err := os.OpenFile(filepath.Join(root.Name(), ".workspace-lock"), os.O_RDWR, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer lock.Close()
					if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
						t.Fatalf("host callback still blocked by workspace lock: %v", err)
					}
					if mutate {
						// A legacy .workspace-lock writer must remain interoperable. Its change
						// must survive while the optimistic outer operation is rejected.
						if err = os.WriteFile(filepath.Join(root.Name(), "concurrent"), []byte("winner"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(mode, "restore") {
						var got bytes.Buffer
						if err = call(map[string]any{"op": "read", "path": "tasks/task/state/value", "limit": 1024}, "", &got); err != nil {
							t.Fatal(err)
						}
						if got.String() != "\"b2xk\"\n" {
							t.Fatalf("read staged data: %s", got.String())
						}
					}
				}}
				err := call(q, suffix, &out)
				if !entered {
					t.Fatalf("host callback not exercised: %v", err)
				}
				if mutate {
					if err == nil || !strings.Contains(err.Error(), "workspace changed during host authorization") {
						t.Fatalf("concurrent mutation accepted: %v", err)
					}
					got, e := os.ReadFile(filepath.Join(root.Name(), "concurrent"))
					if e != nil || string(got) != "winner" {
						t.Fatalf("concurrent write lost: %s %v", got, e)
					}
					if !strings.HasPrefix(mode, "restore") {
						got, e = os.ReadFile(filepath.Join(root.Name(), "tasks/task/state/value"))
						if e != nil || string(got) != "old" {
							t.Fatalf("failed operation selected state: %s %v", got, e)
						}
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type workspaceExchangeWriter struct{ hook func([]byte) }

func (w *workspaceExchangeWriter) Write(b []byte) (int, error) { w.hook(b); return len(b), nil }
