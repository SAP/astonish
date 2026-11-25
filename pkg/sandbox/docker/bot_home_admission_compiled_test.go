package docker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestBotHomeAdmissionCompiledIdentity(t *testing.T) {
	b := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "test-user", BotID: "test-bot"}}
	name := "astonish-home-admission-owned"
	for _, mode := range []string{"wait", "try"} {
		for _, corrupt := range []string{"", "image", "entrypoint", "mode", "mount", "stdin", "scope", "extra-argument"} {
			t.Run(mode+"/"+corrupt, func(t *testing.T) {
				image, entry := "owned-compiled:local", "/usr/local/bin/astonish-host"
				args := []string{"node", "--internal-home-admission", mode}
				mount, scope, stdin := workspaceVolume(b), b.Scope.Key(), true
				switch corrupt {
				case "image":
					image = BotWorkspaceHelperImage
				case "entrypoint":
					entry = "python3"
				case "mode":
					args[2] = "other"
				case "mount":
					mount = "foreign"
				case "stdin":
					stdin = false
				case "scope":
					scope = "foreign"
				case "extra-argument":
					args = append(args, "extra")
				}
				v := map[string]any{"State": map[string]any{"Status": "running"}, "Config": map[string]any{"Labels": map[string]string{botHomeAdmissionLabel: "1", botHomeScopeLabel: scope, botHomeReaderLabel: name}, "Image": image, "Entrypoint": []string{entry}, "Cmd": args, "OpenStdin": stdin, "StdinOnce": true}, "Mounts": []map[string]any{{"Type": "volume", "Name": mount, "Destination": "/private-files", "RW": true}}}
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				runtime := writeDockerTestScript(t, filepath.Join(t.TempDir(), "log"), fmt.Sprintf("printf '%%s\\n' '%s'", raw))
				db := &DockerBackend{cfg: Config{ContainerRuntimePath: runtime, SandboxImage: "owned-compiled:local"}}
				err = db.validateAdmissionHelper(t.Context(), b, name, true)
				if (err != nil) != (corrupt != "") {
					t.Fatalf("identity %q: %v", corrupt, err)
				}
			})
		}
	}
}
