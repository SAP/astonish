package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestBotTaskWorkspaceArchiveValidation(t *testing.T) {
	f := BotHomeWorkspaceFile{Path: "tasks/task/state/checkpoint", Data: []byte("nonempty")}
	e := BotHomeWorkspaceEntry{Path: f.Path, SizeBytes: int64(len(f.Data)), Digest: f.Digest()}
	canonical, _ := json.Marshal([]map[string]any{{"path": e.Path, "size_bytes": e.SizeBytes, "digest": e.Digest, "executable": false}})
	hash := sha256.Sum256(canonical)
	valid := BotHomeWorkspaceSnapshot{Files: []BotHomeWorkspaceFile{f}, Receipt: BotHomeWorkspaceReceipt{TaskPath: "workspace", BindingKey: strings.Repeat("a", 64), Entries: []BotHomeWorkspaceEntry{e}, Digest: hex.EncodeToString(hash[:])}}
	if err := ValidateBotHomeWorkspaceSnapshot(&valid); err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"digest", "bytes", "path", "duplicate", "manifest", "executable", "binding"} {
		t.Run(test, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var bad BotHomeWorkspaceSnapshot
			_ = json.Unmarshal(raw, &bad)
			switch test {
			case "digest":
				bad.Receipt.Digest = strings.Repeat("0", 64)
			case "bytes":
				bad.Files[0].Data = []byte("changed")
			case "path":
				bad.Files[0].Path = "../outside"
			case "duplicate":
				bad.Files = append(bad.Files, bad.Files[0])
			case "manifest":
				bad.Receipt.Entries[0].SizeBytes++
			case "executable":
				bad.Files[0].Executable = true
			case "binding":
				bad.Receipt.BindingKey = "bad"
			}
			if ValidateBotHomeWorkspaceSnapshot(&bad) == nil {
				t.Fatal("malformed snapshot accepted")
			}
		})
	}
}
