package docker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Uses only owned scratch containers, with no user or durable mounts.
func TestBotHomeWorkspaceScratchPinnedInput(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_WORKSPACE") != "1" {
		t.Skip("explicit physical Docker acceptance required")
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	h := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "workspace-scratch-test", TeamID: "owned", UserID: uuid.NewString(), BotID: uuid.NewString()}}
	code := `import os
assert os.getuid()!=0, 'workload still shares trusted supervisor uid'
with open('/proc/self/status') as status:
 assert 'CapEff:\t0000000000000000' in status.read(), 'workload capabilities retained'
try:
 os.kill(os.getppid(), 9)
 raise RuntimeError('supervisor signal permitted')
except PermissionError: pass
try:
 open('/tmp/scratch-input.py','w').write('forged')
 raise RuntimeError('pinned script writable')
except PermissionError: pass
open('result','w').write('protected')
print('protected')
`
	input, _ := json.Marshal(map[string]any{"script": sandbox.BotHomeWorkspaceFile{Path: "scripts/check.py", Data: []byte(code), Executable: true}, "files": []sandbox.BotHomeWorkspaceFile{}})
	raw, err := db.runWorkspaceScratch(context.Background(), h, input)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "protected" {
		t.Fatalf("unprotected scratch: %s", raw)
	}
}
