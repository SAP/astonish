package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestCompiledWorkspaceLegacySnapshotValidation(t *testing.T) {
	for _, path := range []string{"tasks/task/state/value", "tasks/task/state/é<&😀\u2028"} {
		t.Run(path, func(t *testing.T) {
			f := BotHomeWorkspaceFile{Path: path, Data: []byte("exact\x00bytes")}
			e := BotHomeWorkspaceEntry{Path: path, SizeBytes: int64(len(f.Data)), Digest: f.Digest()}
			// Python sort_keys=True, separators=(',',':'), ensure_ascii=True bytes.
			escaped := strings.NewReplacer("é", `\u00e9`, "😀", `\ud83d\ude00`, "\u2028", `\u2028`).Replace(path)
			raw := fmt.Sprintf(`[{"digest":%q,"executable":false,"path":%q,"size_bytes":%d}]`, e.Digest, escaped, e.SizeBytes)
			raw = strings.ReplaceAll(raw, `\\u`, `\u`)
			sum := sha256.Sum256([]byte(raw))
			s := BotHomeWorkspaceSnapshot{Files: []BotHomeWorkspaceFile{f}, Receipt: BotHomeWorkspaceReceipt{TaskPath: "workspace", BindingKey: strings.Repeat("a", 64), Entries: []BotHomeWorkspaceEntry{e}, Digest: hex.EncodeToString(sum[:])}}
			before := s.Receipt.Digest
			if err := ValidateLegacyWorkspaceSnapshot(&s); err != nil {
				t.Fatal(err)
			}
			if s.Receipt.Digest != before {
				t.Fatal("validator mutated input")
			}
			s.Files[0].Data = []byte("changed")
			if ValidateLegacyWorkspaceSnapshot(&s) == nil {
				t.Fatal("corrupt bytes accepted")
			}
		})
	}
	if ValidateLegacyWorkspaceSnapshot(nil) == nil {
		t.Fatal("nil accepted")
	}
}

func TestCompiledWorkspaceOperationSelection(t *testing.T) {
	for _, op := range []string{"publish", "resolve", "read", "write", "snapshot", "list", "knowledge-snapshot", "knowledge-publish", "knowledge-verify", "restore", "script"} {
		if !IsCompiledWorkspaceOperation(op) {
			t.Fatalf("compiled operation missing: %s", op)
		}
	}
	for _, op := range []any{"archive", nil, 123} {
		if IsCompiledWorkspaceOperation(op) {
			t.Fatalf("unmigrated operation routed: %v", op)
		}
	}
}
