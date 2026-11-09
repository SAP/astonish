package docker

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotHomeOfflineDeletionGuards(t *testing.T) {
	b := testBotHome()
	for _, tc := range []struct {
		name, inspect, attached string
		ok                      bool
	}{
		{"owned", homeVolumeJSON(b, "local", nil, b.Scope.Key()), "", true},
		{"attached", homeVolumeJSON(b, "local", nil, b.Scope.Key()), "run-container", false},
		{"foreign", homeVolumeJSON(b, "local", nil, "other"), "", false},
		{"bind", homeVolumeJSON(b, "local", map[string]string{"device": "/", "o": "bind"}, b.Scope.Key()), "", false},
		{"missing", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("DOCKER_TEST_LOG", log)
			body := `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1 $2" = 'volume inspect' ]; then
`
			if tc.inspect == "" {
				body += `echo 'No such volume' >&2; exit 1`
			} else {
				body += "printf '%s' '" + tc.inspect + "'"
			}
			body += "\nelif [ \"$1\" = ps ]; then printf '%s' '" + tc.attached + "'; fi\n"
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: writeDockerTestScript(t, log, body)}}
			if err := db.DeleteBotHome(t.Context(), b); (err == nil) != tc.ok {
				t.Fatalf("delete: %v", err)
			}
			data, _ := os.ReadFile(log)
			removed := strings.Contains(string(data), "volume rm ")
			if removed != (tc.ok && tc.name != "missing") || strings.Contains(string(data), "rm -f") {
				t.Fatalf("unsafe commands: %s", data)
			}
		})
	}
}

func TestBotHomeOfflineInvalidReadDoesNotLaunch(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"; exit 1`)}}
	b := testBotHome()
	for _, p := range []string{"../escape", "/etc/passwd", "a/../b"} {
		if _, err := db.ReadBotHomeArtifact(t.Context(), b, p, 12); err == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	for _, n := range []int64{-1, 0, 1 << 30} {
		if _, err := db.ReadBotHomeArtifact(t.Context(), b, "file", n); err == nil {
			t.Fatal("unsafe bound accepted")
		}
	}
	b.Ref = "other"
	if _, err := db.ReadBotHomeArtifact(t.Context(), b, "file", 12); err == nil {
		t.Fatal("foreign reference accepted")
	}
	if err := db.DeleteBotHome(t.Context(), b); err == nil {
		t.Fatal("foreign reference deleted")
	}
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Fatalf("docker called before validation: %s", data)
	}
}

func TestBotHomeOfflineReaderLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, output, exit     string
		badMount, badOwner, ok bool
	}{
		{name: "success", output: "abc", exit: "0", ok: true},
		{name: "oversized-cli", output: "abcdef", exit: "0"},
		{name: "reader-error", exit: "41"},
		{name: "mount-mismatch", exit: "0", badMount: true},
		{name: "cleanup-ownership", output: "abc", exit: "0", badOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := testBotHome()
			dir := t.TempDir()
			log := filepath.Join(dir, "log")
			t.Setenv("DOCKER_TEST_LOG", log)
			t.Setenv("READER_NAME", filepath.Join(dir, "name"))
			mount := `[{"Type":"volume","Name":"` + b.Ref + `","Destination":"` + botHomeMount + `","RW":false}]`
			if tc.badMount {
				mount = `[]`
			}
			owner := b.Scope.Key()
			if tc.badOwner {
				owner = "foreign"
			}
			script := `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
case "$1" in
volume) printf '%s' '` + homeVolumeJSON(b, "local", nil, b.Scope.Key()) + `';;
container) for arg in "$@"; do case "$arg" in --name=*) printf '%s' "${arg#--name=}" > "$READER_NAME";; esac; done;;
start) printf '%s' '` + tc.output + `';;
inspect) case "$3" in
'{{json .Config.Labels}}') printf '{"astonish.bot-home.reader":"%s","astonish.bot-home.scope":"` + owner + `"}' "$(cat "$READER_NAME")";;
'{{json .Mounts}}') printf '%s' '` + mount + `';;
'{{.State.ExitCode}}') printf '%s' '` + tc.exit + `';;
esac;;
esac`
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: writeDockerTestScript(t, log, script), SandboxImage: "test-image"}}
			data, err := db.ReadBotHomeArtifact(t.Context(), b, "artifact", 3)
			if (err == nil) != tc.ok || (tc.ok && string(data) != "abc") || (!tc.ok && len(data) != 0) {
				t.Fatalf("read %q %v", data, err)
			}
			calls, _ := os.ReadFile(log)
			text := string(calls)
			for _, flag := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "readonly,volume-nocopy", "--pull=never"} {
				if !strings.Contains(text, flag) {
					t.Fatalf("missing %s", flag)
				}
			}
			if strings.Contains(text, "rm -fv ") == tc.badOwner {
				t.Fatalf("wrong cleanup: %s", text)
			}
			if tc.badMount && strings.Contains(text, "start --attach") {
				t.Fatal("unvalidated reader started")
			}
		})
	}
}

func TestBotHomeArtifactBufferBound(t *testing.T) {
	b := &artifactBuffer{remaining: 3}
	if _, ok := any(b).(io.ReaderFrom); ok {
		t.Fatal("ReaderFrom bypasses write bound")
	}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("d")); err == nil {
		t.Fatal("unbounded output")
	}
	if b.String() != "abc" {
		t.Fatal(b.String())
	}
}
