package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRetainedBotHomeCommandShell(t *testing.T) {
	dir := t.TempDir()
	// Emulate only the external mount/chroot utilities; execute both generated
	// shell layers for real so argument shifting and cwd quoting are exercised.
	for name, body := range map[string]string{
		"mountpoint": "#!/bin/sh\nexit \"${MOUNT_FAILURE:-0}\"\n",
		"chroot":     "#!/bin/sh\nshift\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	work := filepath.Join(dir, "with 'quote")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	command := RetainedBotHomeCommand([]string{"/usr/local/bin/astonish-shell", "/bin/sh", "-c", `printf '%s' "$1" > marker`, "test", "literal ' $value"}, work)
	run := func() ([]byte, error) { return exec.Command(command[0], command[1:]...).CombinedOutput() }
	if out, err := run(); err != nil {
		t.Fatalf("shell: %v %s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(work, "marker"))
	if err != nil || string(data) != "literal ' $value" {
		t.Fatalf("relative write %q %v", data, err)
	}
	if err := os.Remove(filepath.Join(work, "marker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOUNT_FAILURE", "1")
	if out, err := run(); err == nil {
		t.Fatalf("missing mount executed: %s", out)
	}
	if _, err := os.Stat(filepath.Join(work, "marker")); !os.IsNotExist(err) {
		t.Fatalf("missing mount wrote marker: %v", err)
	}
	if !strings.Contains(command[2], "mountpoint -q /sandbox/rootfs/root") {
		t.Fatal("home guard missing")
	}
}

func TestRetainedBotHomeCommand(t *testing.T) {
	for _, tc := range []struct {
		name, dir, want string
		command         []string
	}{
		{"default", "", "/root", []string{"sh", "-c", "pwd"}},
		{"explicit", "/tmp/with 'quote", "/tmp/with 'quote", []string{"pwd"}},
		{"wrapped MCP browser", "", "/root", []string{"/usr/local/bin/astonish-shell", "sh", "-c", "pwd"}},
		{"interactive", "", "/root", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RetainedBotHomeCommand(tc.command, tc.dir)
			if len(got) < 6 || got[0] != "/bin/sh" || got[4] != tc.want {
				t.Fatalf("wrong overlay cwd: %q", got)
			}
			want := tc.command
			if len(want) > 0 && want[0] == "/usr/local/bin/astonish-shell" {
				want = want[1:]
			}
			if len(want) == 0 {
				want = []string{"/bin/bash", "-l"}
			}
			if !reflect.DeepEqual(got[5:], want) {
				t.Fatalf("command changed: %q", got)
			}
		})
	}
}
