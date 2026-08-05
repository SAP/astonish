package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CheckpointStore records pre-images ("snapshots") of files a code-mode agent
// is about to modify, so a later /rollback can restore the working directory to
// the state it had before a chosen turn.
//
// The model is snapshot-on-write, scoped per (sessionID, turnIndex):
//   - Before a file is written or edited during turn N, the current on-disk
//     content of that file is captured once (the first capture in a turn wins —
//     subsequent writes in the same turn do not overwrite the pre-image).
//   - Rolling back to turn T restores every snapshot with turnIndex >= T,
//     newest-turn-first, so overlapping edits resolve to the oldest pre-image.
//
// A snapshot whose Existed flag is false means the file did not exist before the
// turn; restoring it deletes the file. This lets rollback undo file creation.
//
// Storage layout (under a code-mode-specific base directory):
//
//	<base>/checkpoints/<sessionID>/turn-<NNNN>.json
//
// Each turn file holds the snapshots captured during that turn. The store is
// deliberately self-contained (no git dependency) so rollback works in any
// working directory.
type CheckpointStore struct {
	baseDir string

	mu sync.Mutex
	// captured tracks, per session, the set of absolute paths already snapshotted
	// in the current (highest) turn so repeated writes in one turn snapshot once.
	captured map[string]map[string]bool
}

// FileSnapshot is a single captured pre-image of a file for one turn.
type FileSnapshot struct {
	// Path is the absolute path of the file that was (about to be) modified.
	Path string `json:"path"`
	// Existed is false when the file did not exist before the turn; restoring
	// such a snapshot deletes the file (undoing its creation).
	Existed bool `json:"existed"`
	// Content is the file's content before the turn (empty when Existed is false).
	Content string `json:"content,omitempty"`
	// Mode is the file's permission bits before the turn (0 when Existed is false).
	Mode uint32 `json:"mode,omitempty"`
	// CapturedAt is when the snapshot was taken.
	CapturedAt time.Time `json:"captured_at"`
}

// turnCheckpoint is the on-disk payload for one turn.
type turnCheckpoint struct {
	SessionID string         `json:"session_id"`
	TurnIndex int            `json:"turn_index"`
	Snapshots []FileSnapshot `json:"snapshots"`
}

// NewCheckpointStore creates a checkpoint store rooted under baseDir. The
// "checkpoints" subdirectory is created lazily on first capture.
func NewCheckpointStore(baseDir string) (*CheckpointStore, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("checkpoint store base directory is required")
	}
	return &CheckpointStore{
		baseDir:  baseDir,
		captured: make(map[string]map[string]bool),
	}, nil
}

// checkpointDir returns the directory holding a session's turn checkpoints.
func (c *CheckpointStore) checkpointDir(sessionID string) string {
	return filepath.Join(c.baseDir, "checkpoints", sanitizeID(sessionID))
}

func (c *CheckpointStore) turnPath(sessionID string, turnIndex int) string {
	return filepath.Join(c.checkpointDir(sessionID), fmt.Sprintf("turn-%04d.json", turnIndex))
}

// BeginTurn marks the start of a new turn for a session. It resets the
// per-turn "already captured" set so the first write to a path in the new turn
// snapshots afresh. Safe to call before every user turn.
func (c *CheckpointStore) BeginTurn(sessionID string, turnIndex int) {
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captured[sessionID] = make(map[string]bool)
	_ = turnIndex
}

// Capture snapshots the current on-disk content of path for the given turn,
// unless it was already captured in this turn. A missing file is recorded as a
// non-existing snapshot so rollback can delete it. Capture failures other than
// "already captured" are returned but are non-fatal to the caller (the turn may
// proceed; rollback simply cannot restore that file).
func (c *CheckpointStore) Capture(sessionID string, turnIndex int, path string) error {
	if c == nil || sessionID == "" {
		return nil
	}
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || abs == "" {
		return nil
	}

	c.mu.Lock()
	set := c.captured[sessionID]
	if set == nil {
		set = make(map[string]bool)
		c.captured[sessionID] = set
	}
	if set[abs] {
		c.mu.Unlock()
		return nil
	}
	set[abs] = true
	c.mu.Unlock()

	snap := FileSnapshot{Path: abs, CapturedAt: time.Now()}
	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && info.Mode().IsRegular():
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			return fmt.Errorf("failed to snapshot %s: %w", abs, readErr)
		}
		snap.Existed = true
		snap.Content = string(data)
		snap.Mode = uint32(info.Mode().Perm())
	case os.IsNotExist(statErr):
		snap.Existed = false
	default:
		// Not a regular file (dir, symlink, etc.) or unreadable — record as
		// non-existing so rollback leaves it alone rather than clobbering it.
		snap.Existed = false
	}

	return c.appendSnapshot(sessionID, turnIndex, snap)
}

// appendSnapshot merges a snapshot into the turn's on-disk checkpoint file.
func (c *CheckpointStore) appendSnapshot(sessionID string, turnIndex int, snap FileSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dir := c.checkpointDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create checkpoint dir: %w", err)
	}
	path := c.turnPath(sessionID, turnIndex)

	tc := turnCheckpoint{SessionID: sessionID, TurnIndex: turnIndex}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &tc)
	}
	// De-dup defensively (Capture already guards, but disk state may differ).
	for _, existing := range tc.Snapshots {
		if existing.Path == snap.Path {
			return nil
		}
	}
	tc.Snapshots = append(tc.Snapshots, snap)

	out, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize checkpoint: %w", err)
	}
	return atomicWrite(path, append(out, '\n'), 0o644)
}

// TurnsWithChanges returns the sorted set of turn indices that have at least
// one captured snapshot for the session.
func (c *CheckpointStore) TurnsWithChanges(sessionID string) []int {
	if c == nil || sessionID == "" {
		return nil
	}
	entries, err := os.ReadDir(c.checkpointDir(sessionID))
	if err != nil {
		return nil
	}
	var turns []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(e.Name(), "turn-%04d.json", &idx); err == nil {
			turns = append(turns, idx)
		}
	}
	sort.Ints(turns)
	return turns
}

// FileCountFrom returns how many distinct files would be restored if the
// session were rolled back to targetTurn (i.e. all snapshots with
// turnIndex >= targetTurn).
func (c *CheckpointStore) FileCountFrom(sessionID string, targetTurn int) int {
	if c == nil || sessionID == "" {
		return 0
	}
	seen := make(map[string]bool)
	for _, turn := range c.TurnsWithChanges(sessionID) {
		if turn < targetTurn {
			continue
		}
		tc, err := c.readTurn(sessionID, turn)
		if err != nil {
			continue
		}
		for _, s := range tc.Snapshots {
			seen[s.Path] = true
		}
	}
	return len(seen)
}

func (c *CheckpointStore) readTurn(sessionID string, turnIndex int) (turnCheckpoint, error) {
	var tc turnCheckpoint
	data, err := os.ReadFile(c.turnPath(sessionID, turnIndex))
	if err != nil {
		return tc, err
	}
	if err := json.Unmarshal(data, &tc); err != nil {
		return tc, fmt.Errorf("failed to parse checkpoint: %w", err)
	}
	return tc, nil
}

// RollbackResult reports what a RestoreTo call changed.
type RollbackResult struct {
	Restored []string // paths whose prior content was written back
	Deleted  []string // paths removed (they did not exist before the target turn)
}

// RestoreTo restores every file snapshotted at or after targetTurn to its
// pre-turn state, then discards those turn checkpoints. Restoration walks turns
// newest-first so that when a file was modified across several turns, the
// oldest (earliest) pre-image at or after the target turn wins — leaving the
// file exactly as it was just before targetTurn began.
func (c *CheckpointStore) RestoreTo(sessionID string, targetTurn int) (RollbackResult, error) {
	var res RollbackResult
	if c == nil || sessionID == "" {
		return res, nil
	}

	turns := c.TurnsWithChanges(sessionID)
	// Newest-first so earlier snapshots overwrite later ones for the same path.
	sort.Sort(sort.Reverse(sort.IntSlice(turns)))

	applied := make(map[string]FileSnapshot)
	var affectedTurns []int
	for _, turn := range turns {
		if turn < targetTurn {
			continue
		}
		affectedTurns = append(affectedTurns, turn)
		tc, err := c.readTurn(sessionID, turn)
		if err != nil {
			continue
		}
		for _, s := range tc.Snapshots {
			// Newest-first iteration means an earlier turn's snapshot (seen
			// later here) correctly overwrites the entry.
			applied[s.Path] = s
		}
	}

	restoredSet := make(map[string]bool)
	deletedSet := make(map[string]bool)
	for path, snap := range applied {
		if snap.Existed {
			mode := os.FileMode(snap.Mode)
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return res, fmt.Errorf("failed to prepare dir for %s: %w", path, err)
			}
			if err := atomicWrite(path, []byte(snap.Content), mode); err != nil {
				return res, fmt.Errorf("failed to restore %s: %w", path, err)
			}
			restoredSet[path] = true
		} else {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return res, fmt.Errorf("failed to remove %s: %w", path, err)
			}
			deletedSet[path] = true
		}
	}

	// Discard the checkpoints we just consumed so a subsequent rollback does
	// not re-apply them.
	for _, turn := range affectedTurns {
		_ = os.Remove(c.turnPath(sessionID, turn))
	}
	// Reset the per-turn capture guard for this session.
	c.mu.Lock()
	delete(c.captured, sessionID)
	c.mu.Unlock()

	res.Restored = sortedKeys(restoredSet)
	res.Deleted = sortedKeys(deletedSet)
	return res, nil
}

// DeleteSession removes all checkpoints for a session (used when a session is
// deleted or reset).
func (c *CheckpointStore) DeleteSession(sessionID string) error {
	if c == nil || sessionID == "" {
		return nil
	}
	c.mu.Lock()
	delete(c.captured, sessionID)
	c.mu.Unlock()
	if err := os.RemoveAll(c.checkpointDir(sessionID)); err != nil {
		return fmt.Errorf("failed to delete checkpoints: %w", err)
	}
	return nil
}

// sanitizeID strips path separators from a session ID so it is safe as a
// directory name.
func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, string(os.PathSeparator), "_")
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "..", "_")
	if id == "" {
		return "unknown"
	}
	return id
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
