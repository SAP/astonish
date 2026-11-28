package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// BotHomeWorkspaceArchiveBackend is deliberately separate from ordinary runtime
// writes. Recovery requires a fresh, explicitly provisioned destination.
type BotHomeWorkspaceArchiveBackend interface {
	BotHomeWorkspaceBackend
	RestoreBotHomeWorkspace(context.Context, *BotHomeBinding, *BotHomeWorkspaceSnapshot) error
}

// ValidateBotHomeWorkspaceSnapshot validates the complete bounded physical
// inventory, including private immutable revisions and receipt bookkeeping.
func ValidateBotHomeWorkspaceSnapshot(s *BotHomeWorkspaceSnapshot) error {
	if s == nil || s.Receipt.TaskPath != "workspace" || len(s.Receipt.BindingKey) != 64 || len(s.Files) > 4096 || len(s.Files) != len(s.Receipt.Entries) {
		return errors.New("invalid workspace archive snapshot")
	}
	if _, e := hex.DecodeString(s.Receipt.BindingKey); e != nil {
		return e
	}
	entries := make([]BotHomeWorkspaceEntry, 0, len(s.Files))
	seen := map[string]bool{}
	var total int64
	for _, f := range s.Files {
		if ValidateBotHomeArtifactPath(f.Path) != nil || f.Path == ".workspace-lock" || seen[f.Path] || f.Executable || int64(len(f.Data)) > MaxBotHomeWorkspaceStateBytes {
			return errors.New("invalid workspace archive file")
		}
		for p := range seen {
			if strings.HasPrefix(p, f.Path+"/") || strings.HasPrefix(f.Path, p+"/") {
				return errors.New("workspace archive ancestor collision")
			}
		}
		seen[f.Path] = true
		total += int64(len(f.Data))
		entries = append(entries, BotHomeWorkspaceEntry{Path: f.Path, SizeBytes: int64(len(f.Data)), Digest: f.Digest()})
	}
	if total > MaxBotHomeWorkspaceBundleBytes {
		return errors.New("workspace archive exceeds bounded transport quota")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) == 0 {
		entries = []BotHomeWorkspaceEntry{}
	}
	// Match the helper's canonical sort_keys representation, not Go struct order.
	canonical := make([]map[string]any, 0, len(entries))
	for i, e := range entries {
		if e != s.Receipt.Entries[i] {
			return errors.New("workspace archive manifest mismatch")
		}
		canonical = append(canonical, map[string]any{"path": e.Path, "size_bytes": e.SizeBytes, "digest": e.Digest, "executable": false})
	}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != s.Receipt.Digest {
		return errors.New("workspace archive digest mismatch")
	}
	return nil
}
