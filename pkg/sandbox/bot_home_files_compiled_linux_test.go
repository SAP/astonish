//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The output writer mutates filesystem state exactly at the ready fence; this
// models a separate process rather than adding a hook to production code.
type homeFenceWriter struct {
	bytes.Buffer
	fence func()
}

func (w *homeFenceWriter) Write(b []byte) (int, error) {
	n, e := w.Buffer.Write(b)
	if bytes.Contains(b, []byte(`"commit":true`)) && w.fence != nil {
		fn := w.fence
		w.fence = nil
		fn()
	}
	return n, e
}

func TestCompiledHomeFilesFenceAndStreamErrors(t *testing.T) {
	for _, mode := range []string{"parent", "stage", "target"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			home, e := os.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer home.Close()
			out := &homeFenceWriter{fence: func() {
				switch mode {
				case "parent":
					if e := os.Rename(filepath.Join(dir, "dir"), filepath.Join(dir, "old")); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(filepath.Join(dir, "dir"), 0700); e != nil {
						t.Fatal(e)
					}
				case "stage":
					rows, e := os.ReadDir(filepath.Join(dir, "dir"))
					if e != nil {
						t.Fatal(e)
					}
					for _, row := range rows {
						if strings.HasPrefix(row.Name(), ".stage-") {
							if e := os.WriteFile(filepath.Join(dir, "dir", row.Name()), []byte("evil"), 0600); e != nil {
								t.Fatal(e)
							}
						}
					}
				case "target":
					if e := os.WriteFile(filepath.Join(dir, "dir", "proof"), []byte("other"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}}
			e = RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"write\",\"path\":\"dir/proof\",\"size\":4,\"create_only\":true}\ndata"+"commit\n"), out)
			if e == nil {
				t.Fatalf("%s race accepted: %s", mode, out.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "dir", "proof")); mode != "target" && !os.IsNotExist(err) {
				t.Fatalf("failed fence published target: %v", err)
			}
			stageParent := filepath.Join(dir, "dir")
			if mode == "parent" {
				stageParent = filepath.Join(dir, "old")
			}
			stages, err := os.ReadDir(stageParent)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range stages {
				if homeInternalStage(stage.Name()) {
					t.Errorf("failed fence leaked stage %s", stage.Name())
				}
			}
			if mode == "target" {
				raw, e := os.ReadFile(filepath.Join(dir, "dir", "proof"))
				if e != nil || string(raw) != "other" {
					t.Fatalf("target overwritten: %q %v", raw, e)
				}
			}
		})
	}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "proof"), []byte("body"), 0600); e != nil {
		t.Fatal(e)
	}
	home, e := os.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer home.Close()
	out := &homeBodyFailure{}
	e = RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"open\",\"path\":\"proof\"}\n"), out)
	if !errors.Is(e, io.ErrClosedPipe) || bytes.Contains(out.Bytes(), []byte(`"error"`)) {
		t.Fatalf("stream error framing: %q %v", out.Bytes(), e)
	}
	for _, name := range []string{"a\tb", strings.Repeat("x", 256), strings.Repeat("d/", 64) + "x"} {
		var out bytes.Buffer
		raw, _ := json.Marshal(map[string]any{"op": "stat", "path": name})
		e = RunBotHomeFiles(home, nil, bytes.NewReader(append(raw, '\n')), &out)
		if e == nil {
			t.Fatalf("unbounded path %q", name)
		}
	}
}

type homeBodyFailure struct {
	bytes.Buffer
	writes int
}

func (w *homeBodyFailure) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}
func (w *homeBodyFailure) Write(b []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, fmt.Errorf("body interrupted: %w", io.ErrClosedPipe)
	}
	return w.Buffer.Write(b)
}

func TestCompiledHomeFilesScopedRoots(t *testing.T) {
	for _, mode := range []string{"valid", "k8s-owner", "unknown-owner-prefix", "wrong-owner", "owner-link", "owner-hardlink", "owner-fifo", "data-link", "private-link", "missing-private", "create-private"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			owner := strings.Repeat("a", 64)
			if mode == "k8s-owner" {
				owner = "k8s-home-v1:" + owner
			}
			if mode == "unknown-owner-prefix" {
				owner = "unknown:" + owner
			}
			if err := os.WriteFile(filepath.Join(dir, ".owner"), []byte(owner+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "data"), 0700); err != nil {
				t.Fatal(err)
			}
			if mode != "missing-private" && mode != "create-private" {
				if err := os.Mkdir(filepath.Join(dir, "workspace-private"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "wrong-owner":
				owner = strings.Repeat("b", 64)
			case "owner-link", "owner-hardlink", "owner-fifo":
				if err := os.Rename(filepath.Join(dir, ".owner"), filepath.Join(dir, "original")); err != nil {
					t.Fatal(err)
				}
				var err error
				if mode == "owner-link" {
					err = os.Symlink("original", filepath.Join(dir, ".owner"))
				}
				if mode == "owner-hardlink" {
					err = os.Link(filepath.Join(dir, "original"), filepath.Join(dir, ".owner"))
				}
				if mode == "owner-fifo" {
					err = unix.Mkfifo(filepath.Join(dir, ".owner"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "data-link", "private-link":
				name := "data"
				if mode == "private-link" {
					name = "workspace-private"
				}
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			home, private, err := OpenBotHomeFileRoots(root, owner, true, mode == "create-private")
			if home != nil {
				defer home.Close()
			}
			if private != nil {
				defer private.Close()
			}
			wantOK := mode == "valid" || mode == "k8s-owner" || mode == "create-private"
			if (err == nil) != wantOK {
				t.Fatalf("roots: home=%v private=%v err=%v", home, private, err)
			}
			if mode == "missing-private" {
				if _, e := os.Lstat(filepath.Join(dir, "workspace-private")); !os.IsNotExist(e) {
					t.Fatalf("read created private root: %v", e)
				}
			}
			if wantOK && (home == nil || private == nil) {
				t.Fatal("missing roots")
			}
		})
	}
}

func TestCompiledHomeFilesResidualStages(t *testing.T) {
	home, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	reserved := []string{".stage-0123456789abcdef0123456789abcdef", ".tmp-01234567-89ab-4def-8123-456789abcdef"}
	ordinary := []string{".stage-notes", ".stage-0123456789abcdef0123456789abcdef.txt", ".tmp-notes", ".hidden", ".stage-" + strings.Repeat("g", 32)}
	for _, name := range append(append([]string{}, reserved...), ordinary...) {
		if err := os.WriteFile(filepath.Join(home.Name(), name), []byte("uncommitted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"list\",\"path\":\".\"}\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range reserved {
		if bytes.Contains(out.Bytes(), []byte(`"path":"`+name+`"`)) {
			t.Errorf("staging residue listed: %s", name)
		}
		for _, op := range []string{"stat", "open", "write"} {
			var result bytes.Buffer
			q := map[string]any{"op": op, "path": name}
			if op == "write" {
				q["size"] = 0
				q["create_only"] = true
			}
			raw, _ := json.Marshal(q)
			err := RunBotHomeFiles(home, nil, bytes.NewReader(append(raw, '\n')), &result)
			if !errors.Is(err, errHomeForbidden) || !bytes.Contains(result.Bytes(), []byte(`"forbidden"`)) {
				t.Errorf("%s %s: %s %v", op, name, result.Bytes(), err)
			}
		}
		if _, err := homeParts("nested/"+name+"/file", false); !errors.Is(err, errHomeForbidden) {
			t.Errorf("reserved ancestor accepted: %s", name)
		}
	}
	for _, name := range ordinary {
		if !bytes.Contains(out.Bytes(), []byte(`"path":"`+name+`"`)) {
			t.Errorf("ordinary dotfile hidden: %s", name)
		}
		var result bytes.Buffer
		raw, _ := json.Marshal(map[string]any{"op": "open", "path": name})
		if err := RunBotHomeFiles(home, nil, bytes.NewReader(append(raw, '\n')), &result); err != nil || !bytes.HasSuffix(result.Bytes(), []byte("uncommitted")) {
			t.Errorf("ordinary dotfile inaccessible: %s %v", name, err)
		}
	}
}

// Run the actual compiled protocol in a child process: closing its input is the
// cooperative cancellation path; SIGKILL intentionally cannot execute defers.
func TestCompiledHomeFilesCancellationProcess(t *testing.T) {
	if root := os.Getenv("ASTONISH_STAGE_CHILD_ROOT"); root != "" {
		home, err := os.Open(root)
		if err != nil {
			os.Exit(3)
		}
		err = RunBotHomeFiles(home, nil, os.Stdin, os.Stdout)
		home.Close()
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	for _, abrupt := range []bool{false, true} {
		t.Run(fmt.Sprint("abrupt=", abrupt), func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCompiledHomeFilesCancellationProcess$")
			cmd.Env = append(os.Environ(), "ASTONISH_STAGE_CHILD_ROOT="+root)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = in.Close() })
			if _, err = io.WriteString(in, "{\"op\":\"write\",\"path\":\"final\",\"size\":4,\"create_only\":true}\ndata"); err != nil {
				t.Fatal(err)
			}
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				if !strings.Contains(line, `"commit":true`) {
					t.Fatalf("no commit fence: %q", line)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("child did not reach fence")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".stage-") {
				t.Fatalf("no stage: %v %v", entries, err)
			}
			if abrupt {
				err = cmd.Process.Kill()
			} else {
				err = in.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("cancelled child succeeded")
			}
			entries, err = os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if !abrupt && len(entries) != 0 {
				t.Fatalf("cooperative cancel leaked: %v", entries)
			}
			if abrupt {
				if len(entries) != 1 {
					t.Fatalf("expected killed child's residue: %v", entries)
				}
				home, err := os.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				defer home.Close()
				var listed bytes.Buffer
				if err := RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"list\",\"path\":\".\"}\n"), &listed); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(listed.String(), `"entries":[]`) {
					t.Fatalf("killed child exposed stage: %s", listed.String())
				}
				for _, op := range []string{"stat", "open"} {
					raw, _ := json.Marshal(map[string]any{"op": op, "path": entries[0].Name()})
					if err := RunBotHomeFiles(home, nil, bytes.NewReader(append(raw, '\n')), io.Discard); !errors.Is(err, errHomeForbidden) {
						t.Fatalf("killed child residue accessible via %s: %v", op, err)
					}
				}
			}
		})
	}
}

func TestCompiledHomeFilesCrashRecoveryPreservesIndependentWriter(t *testing.T) {
	root := t.TempDir()
	start := func(dir string) (*exec.Cmd, io.WriteCloser, string) {
		t.Helper()
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestCompiledHomeFilesCancellationProcess$")
		cmd.Env = append(os.Environ(), "ASTONISH_STAGE_CHILD_ROOT="+filepath.Join(root, dir))
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = in.Close() })
		if _, err = io.WriteString(in, "{\"op\":\"write\",\"path\":\"final\",\"size\":4,\"create_only\":true}\ndata"); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
		select {
		case line := <-ready:
			if !strings.Contains(line, `"commit":true`) {
				t.Fatalf("no fence: %s", line)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no child fence")
		}
		rows, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil || len(rows) != 1 || !homeInternalStage(rows[0].Name()) {
			t.Fatalf("stage: %v %v", rows, err)
		}
		return cmd, in, filepath.Join(root, dir, rows[0].Name())
	}
	crashed, _, residue := start("crashed")
	active, activeInput, activeStage := start("active")
	if err := crashed.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := crashed.Wait(); err == nil {
		t.Fatal("killed writer succeeded")
	}
	ordinary := filepath.Join(root, "crashed", ".stage-notes")
	workspaceStage := filepath.Join(root, "crashed", ".tmp-01234567-89ab-4def-8123-456789abcdef")
	for _, name := range []string{ordinary, workspaceStage} {
		if err := os.WriteFile(name, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	if err = RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"list\",\"path\":\"crashed\"}\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(residue); err != nil {
		t.Fatalf("read reclaimed residue: %v", err)
	}
	if err = RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"write\",\"path\":\"crashed/recovered\",\"size\":4,\"create_only\":true}\nnextcommit\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(residue); !os.IsNotExist(err) {
		t.Fatalf("crash residue not reclaimed: %v", err)
	}
	for _, name := range []string{ordinary, workspaceStage, activeStage} {
		b, e := os.ReadFile(name)
		if e != nil || (name == activeStage && string(b) != "data") || (name != activeStage && string(b) != "keep") {
			t.Fatalf("unowned/active bytes changed: %s %q %v", name, b, e)
		}
	}
	if _, err = io.WriteString(activeInput, "commit\n"); err != nil {
		t.Fatal(err)
	}
	if err = active.Wait(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "active", "final"))
	if err != nil || string(b) != "data" {
		t.Fatalf("active commit: %q %v", b, err)
	}
	rows, err := os.ReadDir(filepath.Join(root, "crashed"))
	if err != nil || len(rows) != 3 {
		t.Fatalf("exact recovered inventory: %v %v", rows, err)
	}
}

func TestCompiledHomeFilesRecoveryRejectsSpecialStages(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, ".stage-0123456789abcdef0123456789abcdef")
			outside := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, name)
			case "hardlink":
				err = os.Link(outside, name)
			case "fifo":
				err = unix.Mkfifo(name, 0600)
			case "directory":
				err = os.Mkdir(name, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			home, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer home.Close()
			if err = RunBotHomeFiles(home, nil, strings.NewReader("{\"op\":\"write\",\"path\":\"final\",\"size\":0,\"create_only\":true}\ncommit\n"), io.Discard); err == nil {
				t.Fatal("special stage accepted")
			}
			if _, err = os.Lstat(name); err != nil {
				t.Fatalf("special stage removed: %v", err)
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "keep" {
				t.Fatalf("external bytes changed: %q %v", b, err)
			}
		})
	}
}

func TestCompiledHomeFilesPrivateRecoveryBeforeQuota(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "content-v1"), 0700); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(root, "content-v1", ".stage-0123456789abcdef0123456789abcdef")
	f, err := os.Create(residue)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(512 << 20); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	private, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	if err = RunBotHomeFiles(nil, private, strings.NewReader("{\"op\":\"put\",\"kind\":\"artifact\",\"size\":4}\ndatacommit\n"), io.Discard); err != nil {
		t.Fatalf("residue prevented quota recovery: %v", err)
	}
	if _, err = os.Lstat(residue); !os.IsNotExist(err) {
		t.Fatalf("private residue survived: %v", err)
	}
}

func TestCompiledHomeFilesProtocol(t *testing.T) {
	home, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	private, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	run := func(q map[string]any, body string) ([]byte, error) {
		t.Helper()
		raw, _ := json.Marshal(q)
		var out bytes.Buffer
		err := RunBotHomeFiles(home, private, io.MultiReader(bytes.NewReader(append(raw, '\n')), strings.NewReader(body)), &out)
		return out.Bytes(), err
	}
	binary := "a\x00\xff\nb"
	out, err := run(map[string]any{"op": "write", "path": "dir/proof.bin", "size": len(binary), "create_only": true}, binary+"commit\n")
	if err != nil || !bytes.Contains(out, []byte(`"commit":true`)) {
		t.Fatalf("write: %s %v", out, err)
	}
	out, err = run(map[string]any{"op": "open", "path": "dir/proof.bin", "offset": 1, "length": 3}, "")
	if err != nil || !bytes.Equal(out[bytes.IndexByte(out, '\n')+1:], []byte(binary[1:4])) {
		t.Fatalf("range %q %v", out, err)
	}
	out, err = run(map[string]any{"op": "put", "kind": "artifact", "revision": 1, "size": len(binary), "readback": true}, binary+"commit\n")
	if err != nil || !bytes.HasSuffix(out, []byte(binary)) {
		t.Fatalf("put: %q %v", out, err)
	}
	out, err = run(map[string]any{"op": "write", "path": "denied", "size": 1, "create_only": true}, "xabort\n")
	if err == nil {
		t.Fatal("denied commit succeeded")
	}
	if _, err := os.Stat(filepath.Join(home.Name(), "denied")); !os.IsNotExist(err) {
		t.Fatalf("denied file persisted: %v %q", err, out)
	}
	sum := sha256.Sum256([]byte(binary))
	digest := hex.EncodeToString(sum[:])
	out, err = run(map[string]any{"op": "write", "path": "dir/proof.bin", "size": 1, "expected": strings.Repeat("0", 64)}, "xcommit\n")
	if err == nil || bytes.Contains(out, []byte(`"commit":true`)) {
		t.Fatalf("CAS mismatch accepted: %s %v", out, err)
	}
	out, err = run(map[string]any{"op": "write", "path": "dir/proof.bin", "size": len(binary), "expected": digest}, binary+"commit\n")
	if err != nil {
		t.Fatalf("CAS replacement: %s %v", out, err)
	}
	out, err = run(map[string]any{"op": "snapshot", "path": "dir/proof.bin", "kind": "artifact", "revision": 2}, "commit\n")
	if err != nil || !bytes.Contains(out, []byte(`content-v1/artifact-2-`)) {
		t.Fatalf("snapshot: %s %v", out, err)
	}
	out, err = run(map[string]any{"op": "write", "path": "short", "size": 100, "create_only": true}, "x")
	if err == nil {
		t.Fatal("short input accepted")
	}
	if _, e := os.Stat(filepath.Join(home.Name(), "short")); !os.IsNotExist(e) {
		t.Fatalf("short input persisted: %v", e)
	}
	out, err = run(map[string]any{"op": "list", "path": ".", "limit": 1}, "")
	if err != nil || !bytes.Contains(out, []byte(`"entries"`)) {
		t.Fatalf("list: %s %v", out, err)
	}
	for _, name := range []string{"symlink", "hardlink", "fifo", "../escape"} {
		err = nil
		switch name {
		case "symlink":
			err = os.Symlink("dir/proof.bin", filepath.Join(home.Name(), name))
		case "hardlink":
			err = os.Link(filepath.Join(home.Name(), "dir/proof.bin"), filepath.Join(home.Name(), name))
		case "fifo":
			err = unix.Mkfifo(filepath.Join(home.Name(), name), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		out, err = run(map[string]any{"op": "stat", "path": name}, "")
		if err == nil || !bytes.Contains(out, []byte(`"forbidden"`)) {
			t.Fatalf("unsafe %s: %q %v", name, out, err)
		}
	}
}
