//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
	"os"
	"strings"
	"testing"
)

func TestCompiledWorkspacePythonReceiptCompatibility(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("explicit Python interoperability fixture")
	}
	rootName := t.TempDir()
	root, err := os.Open(rootName)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	req := map[string]any{"op": "publish", "binding": "scope", "request": BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("bytes")}, {Path: "é<&.txt", Data: []byte("unicode")}}}}
	raw, _ := json.Marshal(req)
	var out bytes.Buffer
	if err = RunBotHomeWorkspace(root, bytes.NewReader(append(append(raw, '\n'), []byte("commit\ncommit\n")...)), &out); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(map[string]any{"op": "resolve", "binding": "scope", "path": "pub"})
	program := "import os,sys,json,base64,hashlib,stat,fcntl,uuid\nq=json.loads(sys.stdin.readline());root=os.open(sys.argv[1],os.O_RDONLY|os.O_DIRECTORY)\n" + BotHomeWorkspaceProgram
	cmd := exec.Command("python3", "-I", "-c", program, rootName)
	cmd.Stdin = bytes.NewReader(append(q, '\n'))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python rejected compiled receipt: %s %v", data, err)
	}
	// Reverse direction on the same durable format, preserving current head CAS.
	var receipt BotHomeWorkspaceReceipt
	if err = json.Unmarshal(bytes.ReplaceAll(out.Bytes(), []byte("WORKSPACE_READY\n"), nil), &receipt); err != nil {
		t.Fatal(err)
	}
	request := req["request"].(BotHomeWorkspacePublishRequest)
	request.PublicationID = "python"
	request.ExpectedDigest = receipt.Digest
	req["request"] = request
	raw, _ = json.Marshal(req)
	cmd = exec.Command("python3", "-I", "-c", program, rootName)
	cmd.Stdin = bytes.NewReader(append(append(raw, '\n'), []byte("commit\ncommit\n")...))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python publish: %s %v", data, err)
	}
	q, _ = json.Marshal(map[string]any{"op": "snapshot", "binding": "scope", "path": "workspace", "limit": 1 << 20})
	cmd = exec.Command("python3", "-I", "-c", program, rootName)
	cmd.Stdin = bytes.NewReader(append(q, '\n'))
	pythonSnapshot, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var pythonWhole, goWhole BotHomeWorkspaceSnapshot
	if err = json.Unmarshal(pythonSnapshot, &pythonWhole); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = RunBotHomeWorkspace(root, bytes.NewReader(append(q, '\n')), &out); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out.Bytes(), &goWhole); err != nil {
		t.Fatal(err)
	}
	if pythonWhole.Receipt.Digest != goWhole.Receipt.Digest {
		t.Fatal("whole-workspace legacy ASCII digest changed")
	}
	q, _ = json.Marshal(map[string]any{"op": "resolve", "binding": "scope", "path": "python"})
	if err = RunBotHomeWorkspace(root, bytes.NewReader(append(q, '\n')), io.Discard); err != nil {
		t.Fatalf("compiled rejected Python receipt: %v", err)
	}
}

func TestCompiledWorkspaceStageCancellationAndRead(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var out bytes.Buffer
	// EOF at the host commit fence must unwind the temporary file, not publish.
	q, _ := json.Marshal(map[string]any{"op": "write", "binding": "scope", "path": "tasks/task/state/result", "data": []byte("pending")})
	err = RunBotHomeWorkspace(root, bytes.NewReader(append(q, '\n')), &out)
	if err == nil || !strings.Contains(out.String(), "WORKSPACE_READY") {
		t.Fatalf("cancellation did not reach fence: %s %v", out.String(), err)
	}
	entries, err := os.ReadDir(filepath.Join(root.Name(), "tasks/task/state"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancel leaked stage: %v %v", entries, err)
	}
	name := "tasks/task/state/.tmp-01234567-89ab-4def-8123-456789abcdef"
	if err := os.WriteFile(filepath.Join(root.Name(), name), []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	q, _ = json.Marshal(map[string]any{"op": "read", "binding": "scope", "path": name, "limit": 1024})
	out.Reset()
	if err := RunBotHomeWorkspace(root, bytes.NewReader(append(q, '\n')), &out); !errors.Is(err, errHomeForbidden) {
		t.Fatalf("workspace exposed residue: %s %v", out.String(), err)
	}
}

func TestCompiledWorkspaceReadDoesNotCreateAndSpecialFiles(t *testing.T) {
	rootName := t.TempDir()
	root, err := os.Open(rootName)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	raw := []byte("{\"op\":\"read\",\"binding\":\"scope\",\"path\":\"tasks/task/state/x\",\"limit\":10}\n")
	if err = RunBotHomeWorkspace(root, bytes.NewReader(raw), io.Discard); err == nil {
		t.Fatal("missing read accepted")
	}
	entries, _ := os.ReadDir(rootName)
	if len(entries) != 0 {
		t.Fatal("read created storage")
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			f, e := os.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			if e = os.WriteFile(filepath.Join(dir, ".workspace-lock"), nil, 0600); e != nil {
				t.Fatal(e)
			}
			name := filepath.Join(dir, "bad")
			switch kind {
			case "symlink":
				e = os.Symlink(".workspace-lock", name)
			case "hardlink":
				e = os.Link(filepath.Join(dir, ".workspace-lock"), name)
			case "fifo":
				e = unix.Mkfifo(name, 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			q := []byte("{\"op\":\"snapshot\",\"binding\":\"scope\",\"path\":\"workspace\",\"limit\":100}\n")
			if err := RunBotHomeWorkspace(f, bytes.NewReader(q), io.Discard); err == nil {
				t.Fatal("special accepted")
			}
		})
	}
	if err := RunBotHomeWorkspace(root, strings.NewReader(strings.Repeat("x", (24<<20)+2)), io.Discard); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

type workspaceFailedWriter struct{}

func (workspaceFailedWriter) Write([]byte) (int, error) { return 0, errors.New("closed stream") }

func TestCompiledWorkspaceLegacyRestore(t *testing.T) {
	source, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	call := func(root *os.File, q any, commits string, dst any) error {
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		var out bytes.Buffer
		if err = RunBotHomeWorkspace(root, bytes.NewReader(append(append(raw, '\n'), []byte(commits)...)), &out); err != nil {
			return err
		}
		if dst != nil {
			return json.Unmarshal([]byte(strings.ReplaceAll(out.String(), "WORKSPACE_READY\n", "")), dst)
		}
		return nil
	}
	binding := strings.Repeat("a", 64)
	publish := BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("original\x00bytes")}, {Path: "é<&.txt", Data: []byte("unicode")}}}
	if err := call(source, map[string]any{"op": "publish", "binding": binding, "request": publish}, "commit\ncommit\n", nil); err != nil {
		t.Fatal(err)
	}
	var snapshot BotHomeWorkspaceSnapshot
	if err := call(source, map[string]any{"op": "snapshot", "binding": binding, "path": "workspace", "limit": 1 << 20}, "", &snapshot); err != nil {
		t.Fatal(err)
	}
	clone := func() *BotHomeWorkspaceSnapshot {
		raw, _ := json.Marshal(snapshot)
		var s BotHomeWorkspaceSnapshot
		_ = json.Unmarshal(raw, &s)
		return &s
	}
	resign := func(s *BotHomeWorkspaceSnapshot) {
		s.Receipt.Entries = nil
		for _, f := range s.Files {
			s.Receipt.Entries = append(s.Receipt.Entries, BotHomeWorkspaceEntry{Path: f.Path, SizeBytes: int64(len(f.Data)), Digest: f.Digest()})
		}
		sort.Slice(s.Receipt.Entries, func(i, j int) bool { return s.Receipt.Entries[i].Path < s.Receipt.Entries[j].Path })
		s.Receipt.Digest = workspaceCanonicalDigest(s.Receipt.Entries, true)
	}
	t.Run("roundtrip", func(t *testing.T) {
		root, e := os.Open(t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		defer root.Close()
		q := map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "snapshot": clone()}
		if e = call(root, q, "commit\ncommit\n", nil); e != nil {
			t.Fatal(e)
		}
		var data []byte
		if e = call(root, map[string]any{"op": "read", "binding": strings.Repeat("b", 64), "path": "tasks/task/TASK.md", "limit": 100}, "", &data); e != nil || !bytes.Equal(data, publish.Files[0].Data) {
			t.Fatalf("read %q %v", data, e)
		}
		for _, f := range snapshot.Files {
			got, e := os.ReadFile(filepath.Join(root.Name(), f.Path))
			if e != nil {
				t.Fatal(e)
			}
			want := f.Data
			if strings.HasPrefix(f.Path, ".receipts/") || strings.HasSuffix(f.Path, "/.head") {
				want, e = workspaceRemapReceipt(want, strings.Repeat("b", 64))
				if e != nil {
					t.Fatal(e)
				}
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("bytes changed: %s", f.Path)
			}
		}
		if e = call(root, q, "commit\ncommit\n", nil); e == nil {
			t.Fatal("nonempty target adopted")
		}
	})
	for _, name := range []string{"bad-envelope", "bad-revision", "foreign-receipt", "missing-revision", "head-path", "ancestor", "marker", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			root, e := os.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer root.Close()
			s := clone()
			switch name {
			case "bad-envelope":
				s.Receipt.Digest = strings.Repeat("0", 64)
			case "bad-revision":
				for i := range s.Files {
					if strings.Contains(s.Files[i].Path, "/.revisions/") {
						s.Files[i].Data = []byte("edited")
						break
					}
				}
				resign(s)
			case "foreign-receipt":
				for i := range s.Files {
					if strings.HasPrefix(s.Files[i].Path, ".receipts/") {
						s.Files[i].Data = bytes.ReplaceAll(s.Files[i].Data, []byte(binding), []byte(strings.Repeat("f", 64)))
					}
				}
				resign(s)
			case "missing-revision":
				s.Files = s.Files[:len(s.Files)-1]
				resign(s)
			case "head-path":
				for i := range s.Files {
					if strings.HasSuffix(s.Files[i].Path, "/.head") {
						s.Files[i].Path = "tasks/other/.head"
					}
				}
				resign(s)
			case "ancestor":
				s.Files = append(s.Files, BotHomeWorkspaceFile{Path: "tasks", Data: []byte("collision")})
				resign(s)
			case "marker":
				s.Files = append(s.Files, BotHomeWorkspaceFile{Path: workspaceRestoreMarker, Data: []byte("x")})
				resign(s)
			case "duplicate":
				s.Files = append(s.Files, s.Files[0])
				resign(s)
			}
			if e = call(root, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "snapshot": s}, "commit\ncommit\n", nil); e == nil {
				t.Fatal("corrupt restore accepted")
			}
			entries, e := os.ReadDir(root.Name())
			if e != nil {
				t.Fatal(e)
			}
			for _, f := range entries {
				if f.Name() != ".workspace-lock" {
					t.Fatalf("invalid restore wrote %s", f.Name())
				}
			}
		})
	}
	for _, commits := range []string{"deny\n", "commit\ndeny\n"} {
		t.Run(commits, func(t *testing.T) {
			root, e := os.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer root.Close()
			if e = call(root, map[string]any{"op": "restore", "binding": binding, "snapshot": clone()}, commits, nil); e == nil {
				t.Fatal("denied restore accepted")
			}
			if commits == "commit\ndeny\n" {
				if _, e = os.Stat(filepath.Join(root.Name(), workspaceRestoreMarker)); e != nil {
					t.Fatal("failure marker missing", e)
				}
				for _, q := range []map[string]any{{"op": "resolve", "binding": binding, "path": "pub"}, {"op": "snapshot", "binding": binding, "path": "workspace", "limit": 1 << 20}, {"op": "read", "binding": binding, "path": "tasks/task/TASK.md", "limit": 100}} {
					if e = call(root, q, "", nil); e == nil {
						t.Fatal("partial restore selectable")
					}
				}
			}
		})
	}
}

func TestCompiledWorkspaceReceiptRemapExactBytes(t *testing.T) {
	raw := []byte("{ \"binding_key\" : \"old\", \"unknown\": [1, 2] }\n")
	got, err := workspaceRemapReceipt(raw, "new")
	if err != nil || !bytes.Equal(got, bytes.Replace(raw, []byte("old"), []byte("new"), 1)) {
		t.Fatalf("remap %s %v", got, err)
	}
	if _, err = workspaceRemapReceipt([]byte(`{"binding_key":"a","binding_key":"b"}`), "new"); err == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestCompiledWorkspaceRoundTrip(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	call := func(q any, commits string, out any) error {
		raw, _ := json.Marshal(q)
		var result bytes.Buffer
		err := RunBotHomeWorkspace(root, bytes.NewReader(append(append(raw, '\n'), []byte(commits)...)), &result)
		if err != nil {
			return err
		}
		body := strings.ReplaceAll(result.String(), "WORKSPACE_READY\n", "")
		if out != nil {
			return json.Unmarshal([]byte(body), out)
		}
		return nil
	}
	req := BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("hello")}, {Path: "scripts/a.sh", Data: []byte("echo ok"), Executable: true}}}
	q := map[string]any{"op": "publish", "binding": "binding", "request": req}
	var receipt BotHomeWorkspaceReceipt
	if err := call(q, "commit\ncommit\n", &receipt); err != nil {
		t.Fatal(err)
	}
	var resolved BotHomeWorkspaceReceipt
	if err := call(map[string]any{"op": "resolve", "binding": "binding", "path": "pub"}, "", &resolved); err != nil || resolved.Digest != receipt.Digest {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	var data []byte
	if err := call(map[string]any{"op": "read", "binding": "binding", "path": "tasks/task/TASK.md", "limit": 100}, "", &data); err != nil || string(data) != "hello" {
		t.Fatalf("read: %q %v", data, err)
	}
	if err := call(q, "", &resolved); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	req.Files[0].Data = []byte("edited")
	q["request"] = req
	if err := call(q, "commit\ncommit\n", nil); err == nil {
		t.Fatal("edited retry accepted")
	}
	var snapshot BotHomeWorkspaceSnapshot
	if err := call(map[string]any{"op": "snapshot", "binding": "binding", "path": "workspace", "limit": 1 << 20}, "", &snapshot); err != nil || len(snapshot.Files) != 4 {
		t.Fatalf("snapshot: %d %v", len(snapshot.Files), err)
	}
	q = map[string]any{"op": "write", "binding": "binding", "path": "tasks/task/state/x", "data": []byte("state")}
	raw, _ := json.Marshal(q)
	if err := RunBotHomeWorkspace(root, bytes.NewReader(append(append(raw, '\n'), []byte("commit\n")...)), workspaceFailedWriter{}); err == nil {
		t.Fatal("failed ready stream accepted")
	}
	if _, err := os.Stat(filepath.Join(root.Name(), "tasks/task/state/x")); !os.IsNotExist(err) {
		t.Fatal("broken ready stream committed", err)
	}
	if err := call(q, "deny\n", nil); err == nil {
		t.Fatal("denied commit accepted")
	}
	if err := call(q, "commit\n", nil); err != nil {
		t.Fatal(err)
	}
	if err := call(map[string]any{"op": "read", "binding": "binding", "path": "tasks/task/state/x", "limit": 4}, "", &data); err == nil {
		t.Fatal("oversized read accepted")
	}
}
