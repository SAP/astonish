//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type workspaceFenceWriter struct {
	bytes.Buffer
	onReady func(int)
	ready   int
}

func (w *workspaceFenceWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *workspaceFenceWriter) Write(b []byte) (int, error) {
	if string(b) == "WORKSPACE_READY\n" {
		w.ready++
		w.onReady(w.ready)
	}
	return w.Buffer.Write(b)
}

func TestCompiledWorkspaceRejectsFenceSubstitution(t *testing.T) {
	for _, mode := range []string{"write-stage", "revision-stage", "revision-after-selection", "revision-parent"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			q := map[string]any{"op": "publish", "binding": "scope", "request": BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("original")}}}}
			if mode == "write-stage" {
				q = map[string]any{"op": "write", "binding": "scope", "path": "tasks/task/state/x", "data": []byte("original")}
			}
			output := &workspaceFenceWriter{onReady: func(n int) {
				if mode == "revision-after-selection" && n != 2 || mode != "revision-after-selection" && n != 1 {
					return
				}
				var target string
				switch mode {
				case "write-stage":
					names, e := filepath.Glob(filepath.Join(dir, "tasks/task/state/.tmp-*"))
					if e != nil || len(names) != 1 {
						t.Fatalf("stage: %v %v", names, e)
					}
					target = names[0]
				case "revision-stage":
					names, e := filepath.Glob(filepath.Join(dir, "tasks/task/.revisions/.revision-stage-*/TASK.md"))
					if e != nil || len(names) != 1 {
						t.Fatalf("stage: %v %v", names, e)
					}
					target = names[0]
				case "revision-after-selection":
					target = filepath.Join(dir, "tasks/task/.revisions/pub/TASK.md")
				case "revision-parent":
					base := filepath.Join(dir, "tasks/task/.revisions")
					if e := os.Rename(base, base+"-detached"); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(base, 0700); e != nil {
						t.Fatal(e)
					}
					return
				}
				if e := os.WriteFile(target, []byte("tampered"), 0600); e != nil {
					t.Fatal(e)
				}
			}}
			raw, _ := json.Marshal(q)
			err = RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\ncommit\ncommit\n"), output)
			if err == nil {
				t.Fatal("substituted staged bytes accepted")
			}
			selected := "tasks/task/.head"
			if mode == "write-stage" {
				selected = "tasks/task/state/x"
			}
			if _, e := os.Stat(filepath.Join(dir, selected)); !os.IsNotExist(e) {
				t.Fatalf("substitution selected: %v", e)
			}
			if mode == "revision-parent" {
				if _, e := os.Stat(filepath.Join(dir, "tasks/task/.revisions-detached/pub")); !os.IsNotExist(e) {
					t.Fatalf("detached revision selected: %v", e)
				}
			}
		})
	}
}
