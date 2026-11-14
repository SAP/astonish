package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	MaxBotHomeWorkspaceAssets            = 64
	MaxBotHomeWorkspaceAssetBytes  int64 = 1 << 20
	MaxBotHomeWorkspaceBundleBytes int64 = 16 << 20
	MaxBotHomeWorkspaceStateBytes  int64 = 128 << 20
	MaxBotHomeWorkspaceBytes       int64 = 512 << 20
)

// BotHomeWorkspaceCommitBarrier is server wiring, never decoded from tool input.
// Implementations must perform the actual final mutation inside commit while
// the repository holds its live lease/cancellation fence. Nil means a direct
// trusted backend caller; launcher writes always supply this barrier.
type BotHomeWorkspaceCommitBarrier func(commit func() error) error

// Only regular files may cross this interface. Implementations reject symlink,
// hardlink, device, FIFO and directory substitutions through every ancestor using
// race-safe confined operations. A validated relative path alone is insufficient.
type BotHomeWorkspaceFile struct {
	Path         string                        `json:"path"`
	Data         []byte                        `json:"data"`
	Executable   bool                          `json:"executable,omitempty"`
	BeforeCommit BotHomeWorkspaceCommitBarrier `json:"-"`
}
type BotHomeWorkspaceEntry struct {
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	Digest     string `json:"digest"`
	Executable bool   `json:"executable,omitempty"`
}
type BotHomeWorkspacePublishRequest struct {
	PublicationID string
	// TaskPath is a normalized home-relative directory, e.g. tasks/<task-id>.
	TaskPath       string
	ExpectedDigest string
	Files          []BotHomeWorkspaceFile
	BeforeCommit   BotHomeWorkspaceCommitBarrier `json:"-"`
}

// Receipt is backend-issued provenance, not arbitrary caller-provided readback.
// The installed capability must resolve ReceiptRef against exact binding,
// publication identity and complete actual byte readback before registry commit.
// Digest is byte integrity only; it attests neither truth nor authorization.
type BotHomeWorkspaceReceipt struct {
	ReceiptRef    string                  `json:"receipt_ref"`
	PublicationID string                  `json:"publication_id"`
	BindingKey    string                  `json:"binding_key"`
	TaskPath      string                  `json:"task_path"`
	Digest        string                  `json:"digest"`
	Entries       []BotHomeWorkspaceEntry `json:"entries"`
}
type BotHomeWorkspaceSnapshot struct {
	Files   []BotHomeWorkspaceFile
	Receipt BotHomeWorkspaceReceipt
}

// Optional; generic Backend, BotHomeBackend and execution signatures stay intact.
// Callers authorize server-derived binding and fence before each operation.
// Publish atomically replaces an immutable task bundle, never mutable state;
// snapshot/read must read complete retained bytes or fail (no truncated success).
// Write is for bounded mutable state/support/output, NOT editing committed TASK.md
// or executable bundle assets behind their revision. Quotas include existing bytes.
// List uses bounded deterministic pagination. No host filesystem fallback.
type BotHomeWorkspaceBackend interface {
	PublishBotHomeWorkspace(context.Context, *BotHomeBinding, BotHomeWorkspacePublishRequest) (*BotHomeWorkspaceReceipt, error)
	ResolveBotHomeWorkspaceReceipt(context.Context, *BotHomeBinding, string) (*BotHomeWorkspaceReceipt, error)
	ReadBotHomeWorkspace(context.Context, *BotHomeBinding, string, int64) ([]byte, error)
	WriteBotHomeWorkspace(context.Context, *BotHomeBinding, BotHomeWorkspaceFile) error
	ListBotHomeWorkspace(context.Context, *BotHomeBinding, string, string, int) ([]BotHomeWorkspaceEntry, string, error)
	SnapshotBotHomeWorkspace(context.Context, *BotHomeBinding, string, int64) (*BotHomeWorkspaceSnapshot, error)
}

func (f BotHomeWorkspaceFile) Digest() string {
	sum := sha256.Sum256(f.Data)
	return hex.EncodeToString(sum[:])
}
func (q BotHomeWorkspacePublishRequest) Validate() error {
	if q.PublicationID == "" || strings.TrimSpace(q.PublicationID) != q.PublicationID || strings.ContainsAny(q.PublicationID, "\x00\r\n") || ValidateBotHomeArtifactPath(q.TaskPath) != nil || len(q.Files) == 0 || len(q.Files) > MaxBotHomeWorkspaceAssets {
		return errors.New("sandbox: invalid workspace publication")
	}
	if q.ExpectedDigest != "" {
		raw, err := hex.DecodeString(q.ExpectedDigest)
		if err != nil || len(raw) != 32 || q.ExpectedDigest != strings.ToLower(q.ExpectedDigest) {
			return errors.New("sandbox: invalid expected workspace digest")
		}
	}
	seen := map[string]bool{}
	var size int64
	for _, f := range q.Files {
		if ValidateBotHomeArtifactPath(f.Path) != nil || seen[f.Path] || int64(len(f.Data)) > MaxBotHomeWorkspaceAssetBytes {
			return errors.New("sandbox: invalid workspace file")
		}
		for prior := range seen {
			if strings.HasPrefix(prior, f.Path+"/") || strings.HasPrefix(f.Path, prior+"/") {
				return errors.New("sandbox: conflicting workspace file ancestors")
			}
		}
		seen[f.Path] = true
		size += int64(len(f.Data))
	}
	if size > MaxBotHomeWorkspaceBundleBytes {
		return errors.New("sandbox: workspace bundle quota exceeded")
	}
	return nil
}
