//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func compiledArchiveTest(t *testing.T, home, private string, q map[string]any, body []byte, approve bool) ([]byte, error) {
	t.Helper()
	h, err := os.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p, err := os.Open(private)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var in, out bytes.Buffer
	if err = json.NewEncoder(&in).Encode(q); err != nil {
		t.Fatal(err)
	}
	in.Write(body)
	if approve {
		in.WriteString("commit\n")
	}
	err = RunBotHomeArchive(h, p, &in, &out, false)
	return out.Bytes(), err
}
func compiledArchiveDecode(t *testing.T, raw []byte) (*BotHomeArchiveManifest, []byte) {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(raw))
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m BotHomeArchiveManifest
	if err = json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	if err = m.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != m.SizeBytes() {
		t.Fatal("stream size")
	}
	return &m, body
}
func TestCompiledArchiveRoundTrip(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("retained\x00body\xff\n"), 100000)
	if err := os.Mkdir(filepath.Join(home, "reports"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "reports", "out.bin"), body, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "object"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	q := map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}
	raw, err := compiledArchiveTest(t, home, private, q, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	m, stream := compiledArchiveDecode(t, raw)
	target, priv := t.TempDir(), t.TempDir()
	ack, err := compiledArchiveTest(t, target, priv, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, stream, true)
	if err != nil || string(ack) != "{\"ready\":true}\n{\"commit\":true}\n{\"restored\":true}\n" {
		t.Fatalf("restore %s %v", ack, err)
	}
	got, err := os.ReadFile(filepath.Join(target, "reports", "out.bin"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("restored bytes", err)
	}
	raw, err = compiledArchiveTest(t, target, priv, q, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	again, againStream := compiledArchiveDecode(t, raw)
	if !reflect.DeepEqual(m, again) || !bytes.Equal(stream, againStream) {
		t.Fatal("reexport changed manifest/bytes")
	}
	if _, err = compiledArchiveTest(t, target, priv, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, stream, true); err == nil {
		t.Fatal("nonempty target reused")
	}
}
func TestCompiledArchiveLegacyCompatibility(t *testing.T) {
	// Deliberately opt in: the normal compiled suite has no Python dependency.
	if os.Getenv("ASTONISH_TEST_ARCHIVE_LEGACY") != "1" {
		t.Skip("legacy Python fixture opt-in")
	}
	home, private := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "dir"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "dir", "file"), []byte("legacy\x00\xff"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "object"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	q := map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}
	oldRaw, err := archiveProgramTest(t, home, private, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	newRaw, err := compiledArchiveTest(t, home, private, q, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	oldM, oldBody := compiledArchiveDecode(t, oldRaw)
	newM, newBody := compiledArchiveDecode(t, newRaw)
	if !reflect.DeepEqual(oldM, newM) || !bytes.Equal(oldBody, newBody) {
		t.Fatal("legacy format differs")
	}
	for _, legacyRestore := range []bool{true, false} {
		target, priv := t.TempDir(), t.TempDir()
		restore := map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": newM}
		if legacyRestore {
			_, err = archiveProgramTest(t, target, priv, restore, newBody)
		} else {
			_, err = compiledArchiveTest(t, target, priv, restore, oldBody, true)
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := compiledArchiveTest(t, target, priv, q, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		m, b := compiledArchiveDecode(t, raw)
		if !reflect.DeepEqual(m, oldM) || !bytes.Equal(b, oldBody) {
			t.Fatal("cross restore changed bytes")
		}
	}
}
func TestCompiledArchiveRejectsUnsafeAndQuota(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "socket", "runtime", "control", "incomplete", "quota", "private-quota"} {
		t.Run(kind, func(t *testing.T) {
			home, private := t.TempDir(), t.TempDir()
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(t.TempDir(), filepath.Join(home, "bad"))
			case "hardlink":
				err = os.WriteFile(filepath.Join(home, "one"), []byte("one"), 0600)
				if err == nil {
					err = os.Link(filepath.Join(home, "one"), filepath.Join(home, "two"))
				}
			case "fifo":
				err = unix.Mkfifo(filepath.Join(home, "bad"), 0600)
			case "socket":
				fd, e := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer unix.Close(fd)
				err = unix.Bind(fd, &unix.SockaddrUnix{Name: filepath.Join(home, "bad")})
			case "runtime":
				err = os.Mkdir(filepath.Join(home, ".ssh"), 0700)
			case "control":
				err = os.WriteFile(filepath.Join(home, ".archive-state"), []byte("restoring\nverified\n"), 0600)
			case "incomplete":
				err = os.WriteFile(filepath.Join(private, ".archive-state"), []byte("restoring\n"), 0600)
			case "quota", "private-quota":
				root, limit := home, MaxBotHomeArchiveBytes
				if kind == "private-quota" {
					root = private
					limit = 536870912
				}
				f, e := os.Create(filepath.Join(root, "huge"))
				if e != nil {
					t.Fatal(e)
				}
				err = f.Truncate(limit + 1)
				f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := compiledArchiveTest(t, home, private, map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}, nil, true)
			if err == nil || len(raw) != 0 {
				t.Fatalf("unsafe inventory escaped: %s %v", raw, err)
			}
		})
	}
}
func TestCompiledArchiveRestoreFailuresAreUnselectable(t *testing.T) {
	digest := sha256.Sum256([]byte("body"))
	m := &BotHomeArchiveManifest{Version: 1, Binding: strings.Repeat("a", 64), Entries: []BotHomeArchiveEntry{{Root: BotHomeArchiveHome, Path: "file", Size: 4, Mode: 0600, SHA256: hex.EncodeToString(digest[:])}}}
	for _, kind := range []string{"short", "digest", "denied", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			home, private := t.TempDir(), t.TempDir()
			body := []byte("body")
			approved := true
			switch kind {
			case "short":
				body = []byte("bo")
				approved = false
			case "digest":
				body = []byte("Body")
			case "denied":
				approved = false
			case "trailing":
				body = []byte("bodygarbage")
			}
			raw, err := compiledArchiveTest(t, home, private, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, body, approved)
			if err == nil || bytes.Contains(raw, []byte("restored")) {
				t.Fatalf("failed restore acknowledged %s %v", raw, err)
			}
			if _, err = compiledArchiveTest(t, home, private, map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}, nil, true); err == nil {
				t.Fatal("partial target exported")
			}
			if _, err = compiledArchiveTest(t, home, private, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, []byte("body"), true); err == nil {
				t.Fatal("partial target retried")
			}
		})
	}
}

type archiveFailWriter struct {
	limit int
	fail  error
}

func (w *archiveFailWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit {
		n := w.limit
		w.limit = 0
		return n, w.fail
	}
	w.limit -= len(b)
	return len(b), nil
}
func TestCompiledArchiveTerminalErrors(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "file"), []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	q := map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}
	raw, err := compiledArchiveTest(t, home, private, q, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.IndexByte(raw, '\n') + 1
	h, _ := os.Open(home)
	defer h.Close()
	p, _ := os.Open(private)
	defer p.Close()
	fail := errors.New("broken output")
	for _, limit := range []int{0, line + 2} {
		var in bytes.Buffer
		json.NewEncoder(&in).Encode(q)
		in.WriteString("commit\n")
		err = RunBotHomeArchive(h, p, &in, &archiveFailWriter{limit: limit, fail: fail}, false)
		if !errors.Is(err, fail) {
			t.Fatalf("output failure hidden: %v", err)
		}
	}
	// Manifest remains useful only as provisional metadata when the host denies.
	raw, err = compiledArchiveTest(t, home, private, q, nil, false)
	if err == nil || len(raw) != line {
		t.Fatalf("denial leaked body %d %v", len(raw), err)
	}
}

type archiveMutateWriter struct {
	bytes.Buffer
	mutate func()
}

func (w *archiveMutateWriter) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b)
	if w.mutate != nil {
		f := w.mutate
		w.mutate = nil
		f()
	}
	return n, err
}
func TestCompiledArchiveChangedAfterManifest(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	path := filepath.Join(home, "file")
	if err := os.WriteFile(path, []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	h, _ := os.Open(home)
	defer h.Close()
	p, _ := os.Open(private)
	defer p.Close()
	var in bytes.Buffer
	json.NewEncoder(&in).Encode(map[string]any{"op": "export", "binding": strings.Repeat("a", 64)})
	in.WriteString("commit\n")
	out := &archiveMutateWriter{mutate: func() {
		if err := os.WriteFile(path, []byte("Body"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	if err := RunBotHomeArchive(h, p, &in, out, false); err == nil {
		t.Fatal("changed export accepted")
	}
	line := bytes.IndexByte(out.Bytes(), '\n') + 1
	if string(out.Bytes()[line:]) != "Body" {
		t.Fatal("terminal failure polluted binary stream")
	}
}
func TestCompiledArchiveHeldAdmissionRequiresRegularLock(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			home, private := t.TempDir(), t.TempDir()
			lock := filepath.Join(private, ".workspace-lock")
			var err error
			if kind == "symlink" {
				err = os.Symlink("elsewhere", lock)
			}
			if kind == "fifo" {
				err = unix.Mkfifo(lock, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			h, _ := os.Open(home)
			defer h.Close()
			p, _ := os.Open(private)
			defer p.Close()
			var in, out bytes.Buffer
			json.NewEncoder(&in).Encode(map[string]any{"op": "export", "binding": strings.Repeat("a", 64)})
			in.WriteString("commit\n")
			if err = RunBotHomeArchive(h, p, &in, &out, true); err == nil || out.Len() != 0 {
				t.Fatal("invalid admission lock accepted", err)
			}
		})
	}
}
func TestCompiledArchiveInvalidManifestDoesNotMutate(t *testing.T) {
	for _, q := range []map[string]any{
		{"op": "restore", "binding": strings.Repeat("b", 64)},
		{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": &BotHomeArchiveManifest{Version: 1, Binding: strings.Repeat("a", 64), Entries: []BotHomeArchiveEntry{{Root: BotHomeArchiveHome, Path: "../bad", Directory: true}}}},
		{"op": "export", "binding": "bad"},
	} {
		home, private := t.TempDir(), t.TempDir()
		if _, err := compiledArchiveTest(t, home, private, q, nil, true); err == nil {
			t.Fatal("invalid request accepted")
		}
		entries, err := os.ReadDir(private)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid request mutated target", err)
		}
	}
}
