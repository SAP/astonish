package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

// Archive callbacks cannot acquire lifecycle locks while their caller holds SQL.
// A stopped session is still an attachment: stop alone is not export preparation.
// The low-level contract must remain fail-closed until a trusted coordinator has
// completed exact-session detach outside the callback. Never infer permission
// from a familiar name, labels, stopped status, or an unrelated detached volume.
func TestBotHomeArchiveRequiresPreDetachedCompute(t *testing.T) {
	for _, attachedRoot := range []string{"home", "private"} {
		for _, attachment := range []string{"astonish-session-retained", "foreign-container", "astonish-home-admission-forged"} {
			t.Run(attachedRoot+"/"+attachment, func(t *testing.T) {
				h := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "test-user", BotID: "test-bot"}, Ref: "retained-home"}
				volume := h.Ref
				if attachedRoot == "private" {
					volume = workspaceVolume(h)
				}
				dir := t.TempDir()
				log, attached := filepath.Join(dir, "log"), filepath.Join(dir, "attached")
				if err := os.WriteFile(attached, []byte("attached"), 0600); err != nil {
					t.Fatal(err)
				}
				script := writeDockerTestScript(t, log, fmt.Sprintf(`printf '%%s\n' "$*" >> %q
case "$1" in
  ps) if [ -e %q ] && [ "$4" = '--filter=volume=%s' ]; then printf '%%s\n' '%s'; fi ;;
  inspect) printf '%%s\n' '{"State":{"Status":"exited"}}' ;;
  *) echo unexpected-runtime-mutation >&2; exit 23 ;;
esac`, log, attached, volume, attachment))
				db := &DockerBackend{cfg: Config{ContainerRuntimePath: script, SandboxImage: "fixture:local"}}
				if err := db.archiveDetached(t.Context(), h, ""); err == nil || !strings.Contains(err.Error(), "requires detached") {
					t.Fatalf("attached compute accepted: %v", err)
				}
				if _, err := os.Stat(attached); err != nil {
					t.Fatalf("archive detached compute itself: %v", err)
				}
				// Simulate an already-completed, external lifecycle preparation. This
				// test is deliberately not evidence that such a coordinator exists.
				if err := os.Remove(attached); err != nil {
					t.Fatal(err)
				}
				if err := db.archiveDetached(t.Context(), h, ""); err != nil {
					t.Fatalf("pre-detached storage rejected: %v", err)
				}
				for _, verb := range []string{"start ", "stop ", "rm ", "run ", "create "} {
					assertDockerTestLogExcludes(t, log, verb)
				}
			})
		}
	}
}
