package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func archiveProgramTest(t *testing.T, home, private string, q map[string]any, body []byte) ([]byte, error) {
	t.Helper()
	program := fmt.Sprintf("import os\nhome=os.open(%q,os.O_RDONLY|os.O_DIRECTORY)\nprivate=os.open(%q,os.O_RDONLY|os.O_DIRECTORY)\n", home, private) + BotHomeArchiveProgram
	raw, _ := json.Marshal(q)
	input := append(raw, '\n')
	input = append(input, body...)
	input = append(input, []byte("commit\n")...)
	cmd := exec.Command("python3", "-I", "-c", program)
	cmd.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}
func TestBotHomeArchiveProgram(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("large-retained-"), 2<<20)
	if err := os.WriteFile(filepath.Join(home, "report.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "object"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	q := map[string]any{"op": "export", "binding": strings.Repeat("a", 64)}
	raw, err := archiveProgramTest(t, home, private, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(raw))
	line, _ := r.ReadBytes('\n')
	var m BotHomeArchiveManifest
	if err = json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	if err = m.Validate(); err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	if int64(len(data)) != m.SizeBytes() {
		t.Fatal("wrong stream size")
	}
	for _, corrupt := range []bool{false, true} {
		target, priv := t.TempDir(), t.TempDir()
		input := append([]byte(nil), data...)
		if corrupt {
			input[0] ^= 1
		}
		_, err = archiveProgramTest(t, target, priv, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, input)
		if corrupt {
			if err == nil {
				t.Fatal("accepted corrupt stream")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(target, "report.bin"))
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("restore differs", err)
		}
		if _, err = archiveProgramTest(t, target, priv, q, nil); err != nil {
			t.Fatal("reexport restored", err)
		}
		if _, err = archiveProgramTest(t, target, priv, map[string]any{"op": "restore", "binding": strings.Repeat("b", 64), "manifest": m}, data); err == nil {
			t.Fatal("nonempty target accepted")
		}
	}
	for _, kind := range []string{"symlink", "hardlink", "fifo", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			h, p := t.TempDir(), t.TempDir()
			var e error
			switch kind {
			case "symlink":
				e = os.Symlink(home, filepath.Join(h, "unsafe"))
			case "hardlink":
				e = os.Link(filepath.Join(home, "report.bin"), filepath.Join(h, "unsafe"))
			case "fifo":
				e = exec.Command("mkfifo", filepath.Join(h, "unsafe")).Run()
			case "runtime":
				e = os.Mkdir(filepath.Join(h, ".ssh"), 0700)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = archiveProgramTest(t, h, p, q, nil); e == nil {
				t.Fatal("unsafe export accepted")
			}
		})
	}
}
