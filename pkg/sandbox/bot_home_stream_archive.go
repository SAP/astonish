package sandbox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// BotHomeArchiveBarrier must synchronously execute operation exactly once while
// holding the platform's durable, exact-binding writer exclusion. It must
// authorize the binding, require paused/quiescent workers, and keep exclusion
// through helper cleanup. A status check alone is NOT a barrier. Docker supplies
// attachment exclusion internally using the protected workspace's kernel flock;
// callers must finish required session stop/detach work before invoking operation,
// never acquire session lifecycle locks inside operation or streaming callbacks.
// Docker export may preserve an exactly bound stopped session under that flock;
// it does not stop or detach compute itself. Restore requires a detached target.
// The operation context must be cancelled when the platform writer fence is lost.
// Backend capability is not authorization. Missing barriers fail closed.
type BotHomeArchiveBarrier func(context.Context, *BotHomeBinding, func(context.Context) error) error

// BotHomeStreamArchiveBackend is optional; Kubernetes/OpenShell deliberately do
// not implement it. Bodies are concatenated in manifest order, without base64 or
// framing. Persist the returned manifest alongside the stream and authenticate
// it in the platform archive. Success is the only permission to retain/select
// these bytes. A failed export must be discarded. A failed restore leaves an
// unselectable disposable target, which must be purged, never retried/adopted.
// Restore never selects a binding or remaps SQL/receipt authority. The caller
// verifies reference closure and remaps scoped refs only after successful restore.
type BotHomeStreamArchiveBackend interface {
	ExportBotHomeArchive(context.Context, *BotHomeBinding, io.Writer, BotHomeArchiveBarrier) (*BotHomeArchiveManifest, error)
	RestoreBotHomeArchive(context.Context, *BotHomeBinding, *BotHomeArchiveManifest, io.Reader, BotHomeArchiveBarrier) error
}

type BotHomeArchiveRoot string

const (
	BotHomeArchiveHome             BotHomeArchiveRoot = "home"
	BotHomeArchivePrivate          BotHomeArchiveRoot = "private"
	MaxBotHomeArchiveManifestBytes                    = 8 << 20
	MaxBotHomeArchiveEntries                          = 16384
	// Transport bound only, not a new per-user storage quota. Private storage
	// retains its existing 512 MiB / 4096 file / 8192 entry workspace quota.
	MaxBotHomeArchiveBytes int64 = 8 << 30
)

type BotHomeArchiveEntry struct {
	Root      BotHomeArchiveRoot `json:"root"`
	Path      string             `json:"path"`
	Directory bool               `json:"directory,omitempty"`
	Size      int64              `json:"size"`
	SHA256    string             `json:"sha256,omitempty"`
	Mode      uint32             `json:"mode"`
}
type BotHomeArchiveManifest struct {
	Version int                   `json:"version"`
	Binding string                `json:"binding"`
	Entries []BotHomeArchiveEntry `json:"entries"`
}

func archiveDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// BotHomeArchivePath rejects runtime/credential mounts rather than silently
// archiving masked mountpoints. Export fails, never produces a partial inventory.
func BotHomeArchivePath(root BotHomeArchiveRoot, path string) error {
	if ValidateBotHomeArtifactPath(path) != nil {
		return errors.New("archive path")
	}
	if root != BotHomeArchiveHome && root != BotHomeArchivePrivate {
		return errors.New("archive root")
	}
	for _, p := range strings.Split(path, "/") {
		if p == ".workspace-lock" || p == ".archive-state" {
			return errors.New("archive control path")
		}
	}
	if root == BotHomeArchiveHome {
		first := strings.Split(path, "/")[0]
		switch first {
		case ".ssh", ".aws", ".kube", ".docker", ".astonish", "proc", "sys", "dev", "run":
			return errors.New("archive runtime path")
		}
	}
	return nil
}
func (m *BotHomeArchiveManifest) Validate() error {
	invalid := errors.New("invalid retained Home archive manifest")
	if m == nil || m.Version != 1 || !archiveDigest(m.Binding) || len(m.Entries) > MaxBotHomeArchiveEntries {
		return invalid
	}
	last := ""
	var total, privateBytes int64
	privateEntries, privateFiles := 0, 0
	seen := map[string]bool{}
	for _, e := range m.Entries {
		key := string(e.Root) + "/" + e.Path
		if BotHomeArchivePath(e.Root, e.Path) != nil || key <= last || e.Size < 0 || e.Size > MaxBotHomeArchiveBytes || e.Mode & ^uint32(0777) != 0 {
			return invalid
		}
		if e.Directory {
			if e.Size != 0 || e.SHA256 != "" {
				return invalid
			}
		} else if !archiveDigest(e.SHA256) {
			return invalid
		}
		a := strings.Split(e.Path, "/")
		if len(a) > 1 && !seen[string(e.Root)+"/"+strings.Join(a[:len(a)-1], "/")] {
			return invalid
		}
		seen[key] = e.Directory
		last = key
		total += e.Size
		if e.Root == BotHomeArchivePrivate {
			privateEntries++
			privateBytes += e.Size
			if !e.Directory {
				privateFiles++
			}
		}
	}
	if total > MaxBotHomeArchiveBytes || privateBytes > 536870912 || privateEntries > 8192 || privateFiles > 4096 {
		return invalid
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxBotHomeArchiveManifestBytes {
		return invalid
	}
	return nil
}
func (m *BotHomeArchiveManifest) SizeBytes() int64 {
	var n int64
	for _, e := range m.Entries {
		n += e.Size
	}
	return n
}
