package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestRestoreWorkspaceLegacyDigestAdmission(t *testing.T) {
	f := sandbox.BotHomeWorkspaceFile{Path: "tasks/task/state/é<&", Data: []byte("exact bytes")}
	e := sandbox.BotHomeWorkspaceEntry{Path: f.Path, SizeBytes: int64(len(f.Data)), Digest: f.Digest()}
	raw := fmt.Sprintf(`[{"digest":%q,"executable":false,"path":"tasks/task/state/\u00e9<&","size_bytes":%d}]`, e.Digest, e.SizeBytes)
	sum := sha256.Sum256([]byte(raw))
	s := &sandbox.BotHomeWorkspaceSnapshot{Files: []sandbox.BotHomeWorkspaceFile{f}, Receipt: sandbox.BotHomeWorkspaceReceipt{BindingKey: strings.Repeat("a", 64), TaskPath: "workspace", Entries: []sandbox.BotHomeWorkspaceEntry{e}, Digest: hex.EncodeToString(sum[:])}}
	db := &DockerBackend{}
	// No compute is started: a valid legacy snapshot reaches binding admission.
	if err := db.RestoreBotHomeWorkspace(context.Background(), nil, s); !errors.Is(err, sandbox.ErrBotHomeUnsupported) {
		t.Fatalf("legacy digest rejected before binding admission: %v", err)
	}
	s.Files[0].Data = []byte("changed")
	if err := db.RestoreBotHomeWorkspace(context.Background(), nil, s); err == nil || errors.Is(err, sandbox.ErrBotHomeUnsupported) {
		t.Fatalf("corrupt archive bypassed validation: %v", err)
	}
}
