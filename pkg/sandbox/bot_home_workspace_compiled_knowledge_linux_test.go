//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func knowledgeCompiledRecord(id string) map[string]any {
	return map[string]any{"id": id, "input": map[string]any{"source": map[string]any{"sequence": 1, "bot_id": "bot"}}, "decision_digest": "digest", "decision": map[string]any{"proposals": []map[string]any{
		{"kind": "knowledge", "path": "knowledge/é.md", "expected_digest": "", "content": "# café <&> 😀\u2028\n", "evidence_ids": nil},
		{"kind": "preference", "path": "preferences/style.md", "expected_digest": "", "content": "# style\n", "evidence_ids": []string{}},
	}}}
}
func knowledgeCompiledCall(root *os.File, op string, record any, commit string, output io.Writer) error {
	raw, err := json.Marshal(map[string]any{"op": op, "binding": "scope", "record": record})
	if err != nil {
		return err
	}
	return RunBotHomeWorkspace(root, bytes.NewReader(append(append(raw, '\n'), []byte(commit)...)), output)
}
func knowledgeCompiledRoot(t *testing.T) *os.File {
	t.Helper()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}
func knowledgeCompiledSnapshot(t *testing.T, root *os.File) string {
	t.Helper()
	var out bytes.Buffer
	if err := knowledgeCompiledCall(root, workspaceKnowledgeSnapshot, nil, "", &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestCompiledKnowledgeRoundTripAndFences(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	if got := knowledgeCompiledSnapshot(t, root); got != "[]\n" {
		t.Fatal(got)
	}
	names, _ := os.ReadDir(root.Name())
	if len(names) != 0 {
		t.Fatal("read created roots or lock")
	}
	r := knowledgeCompiledRecord("first")
	if err := knowledgeCompiledCall(root, workspaceKnowledgeVerify, r, "commit\n", io.Discard); err == nil {
		t.Fatal("missing receipt verified")
	}
	for _, commit := range []string{"deny\n", ""} {
		if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, commit, io.Discard); err == nil {
			t.Fatal("denied commit")
		}
		names, _ = os.ReadDir(root.Name())
		if len(names) != 1 || names[0].Name() != ".workspace-lock" {
			t.Fatal("stage leak", names)
		}
	}
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", workspaceFailedWriter{}); err == nil {
		t.Fatal("broken stream committed")
	}
	var first bytes.Buffer
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", &first); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{workspaceKnowledgePublish, workspaceKnowledgeVerify} {
		var retry bytes.Buffer
		if err := knowledgeCompiledCall(root, op, r, "commit\n", &retry); err != nil || first.String() != retry.String() {
			t.Fatalf("retry %s: %v %s", op, err, retry.String())
		}
	}
	before := knowledgeCompiledSnapshot(t, root)
	props := r["decision"].(map[string]any)["proposals"].([]map[string]any)
	props[0]["content"] = "edited"
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err == nil || !strings.Contains(err.Error(), "edited knowledge retry") {
		t.Fatal(err)
	}
	r["id"] = "next"
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err == nil || !strings.Contains(err.Error(), "knowledge CAS") {
		t.Fatal(err)
	}
	for _, p := range props {
		b, err := os.ReadFile(filepath.Join(root.Name(), ".knowledge", p["path"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		p["expected_digest"] = workspaceHash(b)
	}
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "deny\n", io.Discard); err == nil {
		t.Fatal("denied replacement")
	}
	if after := knowledgeCompiledSnapshot(t, root); after != before {
		t.Fatal("partial denied batch")
	}
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err != nil {
		t.Fatal(err)
	}
	if knowledgeCompiledSnapshot(t, root) == before {
		t.Fatal("replacement not selected")
	}
	if _, err := os.Stat(filepath.Join(root.Name(), ".knowledge/.publications/first")); err != nil {
		t.Fatal("old receipt lost", err)
	}
	if err := os.WriteFile(filepath.Join(root.Name(), ".knowledge/knowledge/é.md"), []byte("tamper"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := knowledgeCompiledCall(root, workspaceKnowledgeVerify, r, "commit\n", io.Discard); err == nil {
		t.Fatal("tampered bytes verified")
	}
}

func TestCompiledKnowledgePythonCompatibility(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("explicit Python interoperability fixture")
	}
	root := knowledgeCompiledRoot(t)
	program := "import os,sys,json,base64,hashlib,stat,fcntl,uuid\nq=json.loads(sys.stdin.readline());root=os.open(sys.argv[1],os.O_RDONLY|os.O_DIRECTORY)\n" + strings.Replace(BotHomeWorkspaceProgram, "if op=='publish':", BotKnowledgeWorkspaceProgram+"\nif op in ('knowledge-snapshot','knowledge-publish','knowledge-verify'): result=knowledgeoperation()\nelif op=='publish':", 1)
	python := func(op string, r any) []byte {
		t.Helper()
		q, _ := json.Marshal(map[string]any{"op": op, "record": r, "binding": "scope"})
		cmd := exec.Command("python3", "-I", "-c", program, root.Name())
		cmd.Stdin = bytes.NewReader(append(append(q, '\n'), []byte("commit\n")...))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("legacy %s: %v %s", op, err, out)
		}
		return bytes.ReplaceAll(out, []byte("WORKSPACE_READY\n"), nil)
	}
	r := knowledgeCompiledRecord("compiled")
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err != nil {
		t.Fatal(err)
	}
	python(workspaceKnowledgeVerify, r)
	var a, b any
	if err := json.Unmarshal(python(workspaceKnowledgeSnapshot, nil), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(knowledgeCompiledSnapshot(t, root)), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("snapshot bytes changed")
	}
	r["id"] = "legacy"
	for _, p := range r["decision"].(map[string]any)["proposals"].([]map[string]any) {
		p["expected_digest"] = workspaceHash([]byte(p["content"].(string)))
		p["content"] = "# legacy bytes\n"
	}
	python(workspaceKnowledgePublish, r)
	if err := knowledgeCompiledCall(root, workspaceKnowledgeVerify, r, "commit\n", io.Discard); err != nil {
		t.Fatal("compiled rejected legacy receipt", err)
	}
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err != nil {
		t.Fatal("compiled rejected legacy retry", err)
	}
}

func TestCompiledKnowledgeUnsafeTreeAndQuotas(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "utf8", "path", "quota", "count"} {
		t.Run(kind, func(t *testing.T) {
			root := knowledgeCompiledRoot(t)
			if err := os.WriteFile(filepath.Join(root.Name(), ".workspace-lock"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root.Name(), ".knowledge/knowledge")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "bad.md")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("/etc/passwd", dest)
			case "hardlink":
				err = os.Link(filepath.Join(root.Name(), ".workspace-lock"), dest)
			case "fifo":
				err = unix.Mkfifo(dest, 0600)
			case "directory":
				err = os.Mkdir(dest, 0700)
			case "utf8":
				err = os.WriteFile(dest, []byte{255}, 0600)
			case "path":
				err = os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("x"), 0600)
			case "quota":
				err = os.WriteFile(dest, []byte(strings.Repeat("x", (1<<20)+1)), 0600)
			case "count":
				for n := 0; n < 65; n++ {
					err = os.WriteFile(filepath.Join(dir, strings.Repeat("a", n+1)+".md"), nil, 0600)
					if err != nil {
						break
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = knowledgeCompiledCall(root, workspaceKnowledgeSnapshot, nil, "", io.Discard); err == nil {
				t.Fatal("unsafe tree accepted")
			}
		})
	}
	for _, name := range []string{"knowledge/.hidden.md", "knowledge/../bad.md", "knowledge/a.md/child.md", "other/a.md"} {
		t.Run(name, func(t *testing.T) {
			root := knowledgeCompiledRoot(t)
			r := knowledgeCompiledRecord("bad")
			props := r["decision"].(map[string]any)["proposals"].([]map[string]any)
			props[0]["path"] = name
			if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err == nil {
				t.Fatal("bad path accepted")
			}
		})
	}
	root := knowledgeCompiledRoot(t)
	r := knowledgeCompiledRecord("huge")
	r["decision"].(map[string]any)["proposals"].([]map[string]any)[0]["content"] = strings.Repeat("x", 1<<20)
	if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err == nil {
		t.Fatal("batch quota accepted")
	}
}

type knowledgeFenceWriter struct {
	bytes.Buffer
	hook func()
}

func (w *knowledgeFenceWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *knowledgeFenceWriter) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("WORKSPACE_READY")) {
		w.hook()
	}
	return w.Buffer.Write(b)
}
func TestCompiledKnowledgeRechecksAfterCommitBarrier(t *testing.T) {
	for _, kind := range []string{"stage", "destination", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			root := knowledgeCompiledRoot(t)
			r := knowledgeCompiledRecord("first")
			if err := knowledgeCompiledCall(root, workspaceKnowledgePublish, r, "commit\n", io.Discard); err != nil {
				t.Fatal(err)
			}
			if kind != "receipt" {
				r["id"] = "next"
				for _, p := range r["decision"].(map[string]any)["proposals"].([]map[string]any) {
					p["expected_digest"] = workspaceHash([]byte(p["content"].(string)))
					p["content"] = "changed"
				}
			}
			w := &knowledgeFenceWriter{hook: func() {
				target := ".knowledge/knowledge/é.md"
				if kind == "stage" {
					names, err := os.ReadDir(root.Name())
					if err != nil {
						t.Fatal(err)
					}
					for _, n := range names {
						if strings.HasPrefix(n.Name(), ".knowledge-stage-") {
							target = n.Name() + "/knowledge/é.md"
						}
					}
				}
				if kind == "receipt" {
					target = ".knowledge/.publications/first"
				}
				if err := os.WriteFile(filepath.Join(root.Name(), target), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			op := workspaceKnowledgePublish
			if kind == "receipt" {
				op = workspaceKnowledgeVerify
			}
			if err := knowledgeCompiledCall(root, op, r, "commit\n", w); err == nil {
				t.Fatal("fence substitution accepted")
			}
		})
	}
}
