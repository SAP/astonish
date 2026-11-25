package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const workspaceScript = "script"

// ValidateLegacyWorkspaceSnapshot accepts both the historical Go validator's
// digest and Python's ensure_ascii=True physical-inventory digest. File bytes,
// inventory and quota checks remain identical for either representation.
func ValidateLegacyWorkspaceSnapshot(s *BotHomeWorkspaceSnapshot) error {
	if err := ValidateBotHomeWorkspaceSnapshot(s); err == nil || s == nil || len(s.Files) > 4096 || len(s.Files) != len(s.Receipt.Entries) {
		return err
	}
	rows := make([]map[string]any, 0, len(s.Receipt.Entries))
	for _, e := range s.Receipt.Entries {
		rows = append(rows, map[string]any{"path": e.Path, "size_bytes": e.SizeBytes, "digest": e.Digest, "executable": e.Executable})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rows); err != nil {
		return err
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	var ascii strings.Builder
	for _, r := range string(raw) {
		if r < 128 {
			ascii.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&ascii, `\u%04x`, r)
		} else {
			r -= 0x10000
			fmt.Fprintf(&ascii, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	sum := sha256.Sum256([]byte(ascii.String()))
	if hex.EncodeToString(sum[:]) != s.Receipt.Digest {
		return ValidateBotHomeWorkspaceSnapshot(s)
	}
	normalized := *s
	raw, _ = json.Marshal(rows)
	sum = sha256.Sum256(raw)
	normalized.Receipt.Digest = hex.EncodeToString(sum[:])
	return ValidateBotHomeWorkspaceSnapshot(&normalized)
}

const (
	workspaceKnowledgeSnapshot = "knowledge-snapshot"
	workspaceKnowledgePublish  = "knowledge-publish"
	workspaceKnowledgeVerify   = "knowledge-verify"
)

// IsCompiledWorkspaceOperation selects fixed compiled storage dispatch. Script
// storage is compiled too; only its separate untrusted scratch workload remains
// interpreted. An execution error never falls back to legacy Python storage.
func IsCompiledWorkspaceOperation(value any) bool {
	op, ok := value.(string)
	if !ok {
		return false
	}
	switch op {
	case "publish", "resolve", "read", "write", "snapshot", "list", workspaceKnowledgeSnapshot, workspaceKnowledgePublish, workspaceKnowledgeVerify, "restore", workspaceScript:
		return true
	default:
		return false
	}
}
