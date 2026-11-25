//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCompiledWorkspaceScript(t *testing.T) {
	for _, mode := range []string{"success", "empty", "failure", "digest", "receipt", "marker", "duplicate", "traversal", "oversize", "denied", "stage-change", "state-change", "revision-change", "large-frame", "no-state", "symlink", "hardlink", "fifo", "stdout-quota", "receipt-change", "marker-after-barrier", "task-change", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			script := BotHomeWorkspaceFile{Path: "scripts/run.py", Data: []byte("print('ok')"), Executable: true}
			pub := "11111111-1111-4111-8111-111111111111"
			call := func(q any, suffix string, out *bytes.Buffer) error {
				raw, _ := json.Marshal(q)
				return RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\n"+suffix), out)
			}
			var out bytes.Buffer
			if err = call(map[string]any{"op": "publish", "binding": "scope", "request": BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: pub, Files: []BotHomeWorkspaceFile{script}}}, "commit\ncommit\n", &out); err != nil {
				t.Fatal(err)
			}
			if err = call(map[string]any{"op": "write", "binding": "scope", "path": "tasks/task/state/old", "data": []byte("old")}, "commit\n", &out); err != nil {
				t.Fatal(err)
			}
			q := map[string]any{"op": "script", "binding": "scope", "path": "tasks/task", "publication": pub, "script": script.Path, "digest": script.Digest()}
			files := []BotHomeWorkspaceFile{{Path: "new", Data: []byte("new\x00bytes")}}
			code := 0
			switch mode {
			case "no-state":
				if err = os.RemoveAll(filepath.Join(root.Name(), "tasks/task/state")); err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink", "fifo":
				path := filepath.Join(root.Name(), "tasks/task/state/bad")
				if mode == "symlink" {
					err = os.Symlink("old", path)
				} else if mode == "hardlink" {
					err = os.Link(filepath.Join(root.Name(), "tasks/task/state/old"), path)
				} else {
					err = unix.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "empty":
				files = []BotHomeWorkspaceFile{}
			case "failure":
				code = 1
			case "digest":
				q["digest"] = strings.Repeat("0", 64)
			case "receipt":
				q["publication"] = "22222222-2222-4222-8222-222222222222"
				raw, _ := os.ReadFile(filepath.Join(root.Name(), ".receipts", pub))
				if err = os.WriteFile(filepath.Join(root.Name(), ".receipts", q["publication"].(string)), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "marker":
				if err = os.WriteFile(filepath.Join(root.Name(), workspaceRestoreMarker), []byte("incomplete"), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				files = append(files, files[0])
			case "traversal":
				files[0].Path = "../escape"
			case "oversize":
				files[0].Data = bytes.Repeat([]byte("x"), (1<<20)+1)
			case "large-frame":
				files = nil
				for i := 0; i < 20; i++ {
					files = append(files, BotHomeWorkspaceFile{Path: string(rune('a' + i)), Data: bytes.Repeat([]byte("x"), 1<<20)})
				}
			}
			stdout := "ok"
			if mode == "stdout-quota" {
				stdout = strings.Repeat("x", 65537)
			}
			update, _ := json.Marshal(map[string]any{"exit_code": code, "stdout": stdout, "stderr": "", "files": files})
			commit := "commit\n"
			if mode == "denied" {
				commit = "deny\n"
			}
			raw, _ := json.Marshal(q)
			out.Reset()
			writer := &scriptFenceWriter{out: &out, hook: func() {
				var path string
				switch mode {
				case "receipt-change":
					path = filepath.Join(root.Name(), ".receipts", pub)
				case "marker-after-barrier":
					path = filepath.Join(root.Name(), workspaceRestoreMarker)
				case "task-change":
					task := filepath.Join(root.Name(), "tasks/task")
					if e := os.Rename(task, task+"-moved"); e != nil {
						t.Fatal(e)
					}
					if e := os.MkdirAll(task+"/state", 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(task+"/state/old", []byte("old"), 0600); e != nil {
						t.Fatal(e)
					}
				case "stage-change":
					entries, e := os.ReadDir(filepath.Join(root.Name(), "tasks/task"))
					if e != nil {
						t.Fatal(e)
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), "state-stage-") {
							path = filepath.Join(root.Name(), "tasks/task", entry.Name(), "new")
						}
					}
				case "state-change":
					path = filepath.Join(root.Name(), "tasks/task/state/old")
				case "revision-change":
					path = filepath.Join(root.Name(), "tasks/task/.revisions", pub, script.Path)
				}
				if path != "" {
					if e := os.WriteFile(path, []byte("tampered"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}}
			suffix := string(update) + "\n" + commit
			if mode == "truncated" {
				suffix = string(update)
			}
			err = RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\n"+suffix), writer)
			success := mode == "success" || mode == "empty" || mode == "large-frame" || mode == "no-state"
			if success {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(out.String(), "WORKSPACE_SCRIPT ") || !strings.Contains(out.String(), "WORKSPACE_READY\n") {
					t.Fatalf("handshake: %s", out.String())
				}
				if _, err = os.Stat(filepath.Join(root.Name(), "tasks/task/state/old")); !os.IsNotExist(err) {
					t.Fatalf("old state remains: %v", err)
				}
				for _, f := range files {
					got, e := os.ReadFile(filepath.Join(root.Name(), "tasks/task/state", f.Path))
					if e != nil || !bytes.Equal(got, f.Data) {
						t.Fatalf("state %s: %v", f.Path, e)
					}
				}
			} else if err == nil {
				t.Fatal("unsafe script accepted")
			} else if mode != "state-change" {
				got, e := os.ReadFile(filepath.Join(root.Name(), "tasks/task/state/old"))
				if e != nil || string(got) != "old" {
					t.Fatalf("state changed on failure: %q %v", got, e)
				}
			}
			if mode == "marker" && strings.Contains(out.String(), "WORKSPACE_SCRIPT") {
				t.Fatal("incomplete restore executed")
			}
		})
	}
}

type scriptFenceWriter struct {
	out  *bytes.Buffer
	hook func()
}

func (w *scriptFenceWriter) Write(b []byte) (int, error) {
	if bytes.Equal(b, []byte("WORKSPACE_READY\n")) {
		w.hook()
	}
	return w.out.Write(b)
}

func TestCompiledWorkspaceScriptRouting(t *testing.T) {
	if !IsCompiledWorkspaceOperation("script") {
		t.Fatal("trusted script storage still uses Python")
	}
}
