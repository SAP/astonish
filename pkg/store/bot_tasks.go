package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	BotTaskSchemaVersion           = 1
	BotTaskMaxAssets               = 64
	BotTaskMaxAssetBytes     int64 = 1 << 20
	BotTaskMaxBundleBytes    int64 = 16 << 20
	BotTaskMaxStateBytes     int64 = 128 << 20
	BotTaskMaxWorkspaceBytes int64 = 512 << 20
)

type BotTaskStatus string

const (
	BotTaskDraft    BotTaskStatus = "draft"
	BotTaskReady    BotTaskStatus = "ready"
	BotTaskArchived BotTaskStatus = "archived"
)

type BotTaskPublicationState string

const (
	BotTaskPublicationPrepared   BotTaskPublicationState = "prepared"
	BotTaskPublicationFilesReady BotTaskPublicationState = "files_ready"
	BotTaskPublicationCommitted  BotTaskPublicationState = "committed"
	BotTaskPublicationHeld       BotTaskPublicationState = "held"
	BotTaskPublicationAborted    BotTaskPublicationState = "aborted"
)

type BotTaskResearchMode string

const (
	BotTaskResearchNone            BotTaskResearchMode = "none"
	BotTaskResearchOpenWeb         BotTaskResearchMode = "open_web"
	BotTaskResearchProvidedSources BotTaskResearchMode = "provided_sources"
)

type BotTaskAssetKind string

const (
	BotTaskAssetDefinition BotTaskAssetKind = "definition"
	BotTaskAssetText       BotTaskAssetKind = "text"
	BotTaskAssetScript     BotTaskAssetKind = "script"
	BotTaskAssetData       BotTaskAssetKind = "data"
)

type BotTaskRef struct {
	TaskID   uuid.UUID `json:"task_id"`
	Revision int64     `json:"revision"`
	Digest   string    `json:"digest"`
}

func (r *BotTaskRef) Clone() *BotTaskRef {
	if r == nil {
		return nil
	}
	out := *r
	return &out
}
func (r BotTaskRef) Validate() error {
	if r.TaskID == uuid.Nil || r.Revision < 1 || !skillDigestValid(r.Digest) {
		return ErrBotInvalid
	}
	return nil
}

type BotTaskAsset struct {
	Path       string           `json:"path"`
	Kind       BotTaskAssetKind `json:"kind"`
	Digest     string           `json:"digest"`
	SizeBytes  int64            `json:"size_bytes"`
	Executable bool             `json:"executable,omitempty"`
}

func ValidateBotTaskAssetPath(p string) error {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
		return ErrBotInvalid
	}
	return nil
}
func (a BotTaskAsset) Validate() error {
	if ValidateBotTaskAssetPath(a.Path) != nil || !skillDigestValid(a.Digest) || a.SizeBytes < 0 || a.SizeBytes > BotTaskMaxAssetBytes {
		return ErrBotInvalid
	}
	switch a.Kind {
	case BotTaskAssetDefinition, BotTaskAssetText, BotTaskAssetScript, BotTaskAssetData:
	default:
		return ErrBotInvalid
	}
	if a.Executable && a.Kind != BotTaskAssetScript {
		return ErrBotInvalid
	}
	return nil
}

type BotTaskGoalRef struct {
	BotID    BotID     `json:"bot_id"`
	GoalID   uuid.UUID `json:"goal_id"`
	Revision int64     `json:"revision"`
}
type BotTaskSkillRef struct {
	BotID    BotID     `json:"bot_id"`
	SkillID  uuid.UUID `json:"skill_id"`
	Revision int64     `json:"revision"`
	Digest   string    `json:"digest"`
}

// Manifest describes a committed file bundle, not a second editable definition.
// Assignment and requirements are parsed from authoritative TASK.md by the loader.
// Ready certifies executable shape, never factual truth, permission or recurrence.
// RawMessage parameters preserve integer bytes above 2^53 in clone and archival JSON.
type BotTaskManifest struct {
	SchemaVersion     int                        `json:"schema_version"`
	BotID             BotID                      `json:"bot_id"`
	TaskID            uuid.UUID                  `json:"task_id"`
	Title             string                     `json:"title"`
	Assignment        string                     `json:"assignment,omitempty"`
	OwnerRequirements []string                   `json:"owner_requirements,omitempty"`
	Assumptions       []string                   `json:"assumptions,omitempty"`
	Parameters        map[string]json.RawMessage `json:"parameters,omitempty"`
	ResearchMode      BotTaskResearchMode        `json:"research_mode,omitempty"`
	Goal              *BotTaskGoalRef            `json:"goal,omitempty"`
	Skills            []BotTaskSkillRef          `json:"skills,omitempty"`
	OutputRules       []string                   `json:"output_rules,omitempty"`
	Assets            []BotTaskAsset             `json:"assets,omitempty"`
}

func (m *BotTaskManifest) Clone() *BotTaskManifest {
	if m == nil {
		return nil
	}
	out := *m
	out.OwnerRequirements = append([]string(nil), m.OwnerRequirements...)
	out.Assumptions = append([]string(nil), m.Assumptions...)
	out.OutputRules = append([]string(nil), m.OutputRules...)
	out.Assets = append([]BotTaskAsset(nil), m.Assets...)
	out.Skills = append([]BotTaskSkillRef(nil), m.Skills...)
	if m.Goal != nil {
		g := *m.Goal
		out.Goal = &g
	}
	if m.Parameters != nil {
		out.Parameters = make(map[string]json.RawMessage, len(m.Parameters))
		for k, v := range m.Parameters {
			out.Parameters[k] = append(json.RawMessage(nil), v...)
		}
	}
	return &out
}
func DecodeBotTaskManifest(raw []byte) (*BotTaskManifest, error) {
	if int64(len(raw)) > BotTaskMaxBundleBytes {
		return nil, ErrBotInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m BotTaskManifest
	if dec.Decode(&m) != nil {
		return nil, ErrBotInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrBotInvalid
	}
	if m.SchemaVersion != BotTaskSchemaVersion {
		return nil, ErrBotUnsupported
	}
	if err := m.Validate(BotTaskDraft); err != nil {
		return nil, err
	}
	return &m, nil
}
func (m BotTaskManifest) Validate(status BotTaskStatus) error {
	if m.SchemaVersion != BotTaskSchemaVersion {
		return ErrBotUnsupported
	}
	if m.BotID == uuid.Nil || m.TaskID == uuid.Nil || !skillText(m.Title) {
		return ErrBotInvalid
	}
	switch status {
	case BotTaskDraft, BotTaskReady, BotTaskArchived:
	default:
		return ErrBotInvalid
	}
	switch m.ResearchMode {
	case "", BotTaskResearchNone, BotTaskResearchOpenWeb, BotTaskResearchProvidedSources:
	default:
		return ErrBotInvalid
	}
	if len(m.Assets) > BotTaskMaxAssets || len(m.Skills) > BotTaskMaxAssets {
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	var size int64
	definition := false
	for _, a := range m.Assets {
		if a.Validate() != nil || seen[a.Path] {
			return ErrBotInvalid
		}
		for prior := range seen {
			if strings.HasPrefix(prior, a.Path+"/") || strings.HasPrefix(a.Path, prior+"/") {
				return ErrBotInvalid
			}
		}
		seen[a.Path] = true
		size += a.SizeBytes
		if a.Kind == BotTaskAssetDefinition {
			if a.Path != "TASK.md" || a.SizeBytes == 0 {
				return ErrBotInvalid
			}
			definition = true
		}
	}
	if size > BotTaskMaxBundleBytes {
		return ErrBotInvalid
	}
	if status == BotTaskReady && (!skillText(m.Assignment) || !definition) {
		return ErrBotInvalid
	}
	if m.Goal != nil {
		if m.Goal.BotID != m.BotID {
			return ErrBotForbidden
		}
		if m.Goal.GoalID == uuid.Nil || m.Goal.Revision < 1 {
			return ErrBotInvalid
		}
	}
	skills := map[uuid.UUID]bool{}
	for _, s := range m.Skills {
		if s.BotID != m.BotID {
			return ErrBotForbidden
		}
		if s.SkillID == uuid.Nil || s.Revision < 1 || !skillDigestValid(s.Digest) || skills[s.SkillID] {
			return ErrBotInvalid
		}
		skills[s.SkillID] = true
	}
	for k, v := range m.Parameters {
		if !skillText(k) || !json.Valid(v) {
			return ErrBotInvalid
		}
	}
	for _, list := range [][]string{m.OwnerRequirements, m.Assumptions, m.OutputRules} {
		if len(list) > BotTaskMaxAssets {
			return ErrBotInvalid
		}
		for _, s := range list {
			if !skillText(s) {
				return ErrBotInvalid
			}
		}
	}
	raw, err := json.Marshal(m)
	if err != nil || int64(len(raw))+size > BotTaskMaxBundleBytes {
		return ErrBotInvalid
	}
	return nil
}

func ValidateBotTaskStatusTransition(from, to BotTaskStatus) error {
	if from == BotTaskDraft && (to == BotTaskReady || to == BotTaskArchived) || from == BotTaskReady && (to == BotTaskDraft || to == BotTaskArchived) || from == BotTaskArchived && to == BotTaskDraft {
		return nil
	}
	return ErrBotInvalid
}

// Shape only: FilesReady additionally requires repository-resolved trusted complete
// byte readback; Committed requires original authority, lease fence and head CAS.
// Held/Aborted are terminal; recovery creates a new authorized publication.
func ValidateBotTaskPublicationTransition(from, to BotTaskPublicationState) error {
	if from == BotTaskPublicationPrepared && (to == BotTaskPublicationFilesReady || to == BotTaskPublicationHeld || to == BotTaskPublicationAborted) || from == BotTaskPublicationFilesReady && (to == BotTaskPublicationCommitted || to == BotTaskPublicationHeld || to == BotTaskPublicationAborted) {
		return nil
	}
	return ErrBotInvalid
}

// Task stores identity/head only. Manifest/bundle snapshots live in immutable
// revisions for recovery and run pinning; current instructions come from Home.
type BotTask struct {
	ID        uuid.UUID     `json:"id"`
	BotID     BotID         `json:"bot_id"`
	Key       string        `json:"key"`
	Title     string        `json:"title"`
	Status    BotTaskStatus `json:"status"`
	Revision  int64         `json:"revision"`
	Head      *BotTaskRef   `json:"head,omitempty"`
	HomePath  string        `json:"home_path"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}
type BotTaskRevision struct {
	SchemaVersion int               `json:"schema_version"`
	ID            uuid.UUID         `json:"id"`
	BotID         BotID             `json:"bot_id"`
	Ref           BotTaskRef        `json:"ref"`
	Parent        *BotTaskRef       `json:"parent,omitempty"`
	Manifest      BotTaskManifest   `json:"manifest"`
	Bundle        map[string][]byte `json:"bundle"`
	CreatedAt     time.Time         `json:"created_at"`
}

func (r *BotTaskRevision) Clone() *BotTaskRevision {
	if r == nil {
		return nil
	}
	out := *r
	out.Parent = r.Parent.Clone()
	out.Manifest = *r.Manifest.Clone()
	if r.Bundle != nil {
		out.Bundle = make(map[string][]byte, len(r.Bundle))
		for k, v := range r.Bundle {
			out.Bundle[k] = append([]byte(nil), v...)
		}
	}
	return &out
}

// ContentDigest binds revision identity, parent, parsed manifest and exact asset
// bytes; exclude only the digest itself and server-created archival timestamp.
func (r BotTaskRevision) ContentDigest() string {
	r.Ref.Digest = ""
	r.CreatedAt = time.Time{}
	return botWorkDigest(r)
}
func (r BotTaskRevision) Validate(status BotTaskStatus) error {
	if r.SchemaVersion != BotTaskSchemaVersion {
		return ErrBotUnsupported
	}
	if r.ID == uuid.Nil || r.BotID == uuid.Nil || r.Ref.Validate() != nil || r.Manifest.BotID != r.BotID || r.Manifest.TaskID != r.Ref.TaskID {
		return ErrBotInvalid
	}
	if err := r.Manifest.Validate(status); err != nil {
		return err
	}
	// Candidate numbers are monotonic allocations, including held/aborted
	// candidates. Parent identifies the selected committed head and may skip them.
	if r.Parent != nil && (r.Parent.Validate() != nil || r.Parent.TaskID != r.Ref.TaskID || r.Parent.Revision >= r.Ref.Revision) {
		return ErrBotInvalid
	}
	if len(r.Bundle) != len(r.Manifest.Assets) {
		return ErrBotInvalid
	}
	for _, a := range r.Manifest.Assets {
		data, ok := r.Bundle[a.Path]
		if !ok || int64(len(data)) != a.SizeBytes {
			return ErrBotInvalid
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.Digest {
			return ErrBotInvalid
		}
	}
	if r.Ref.Digest != r.ContentDigest() {
		return ErrBotInvalid
	}
	return nil
}

type BotTaskPublication struct {
	ID                     uuid.UUID               `json:"id"`
	BotID                  BotID                   `json:"bot_id"`
	TaskID                 uuid.UUID               `json:"task_id"`
	State                  BotTaskPublicationState `json:"state"`
	ExpectedHead           *BotTaskRef             `json:"expected_head,omitempty"`
	Candidate              BotTaskRevision         `json:"candidate"`
	AuthorityConstraintRef string                  `json:"authority_constraint_ref"`
	// Resolved by installed trusted workspace capability, never a caller boolean.
	WorkspaceReceiptRef string `json:"workspace_receipt_ref,omitempty"`
}
type BotTaskMutation struct {
	BotID                  BotID
	Lease                  BotLease
	ActorID                uuid.UUID
	ExpectedRevision       int64
	IdempotencyKey         string
	RequestDigest          string
	AuthorityConstraintRef string
}
type BotTaskPrepareRequest struct {
	Mutation     BotTaskMutation
	Task         BotTask
	Candidate    BotTaskRevision
	ExpectedHead *BotTaskRef
}
type BotTaskCommitRequest struct {
	Mutation            BotTaskMutation
	PublicationID       uuid.UUID
	WorkspaceReceiptRef string
}
type BotTaskLifecycleRequest struct {
	Mutation BotTaskMutation
	TaskID   uuid.UUID
	Status   BotTaskStatus
	Reason   string
}
type BotTaskPage struct {
	Limit           int
	After           string
	IncludeArchived bool
}
type BotTaskList struct {
	Tasks []BotTask
	Next  string
}

// Optional extensions: legacy BotStore implementations need not implement Tasks.
// Every write rechecks canonical scope/authority; IDs/files never grant access.
type BotTaskStore interface {
	ListTasks(context.Context, BotID, BotTaskPage) (*BotTaskList, error)
	GetTask(context.Context, BotID, uuid.UUID) (*BotTask, error)
	GetTaskRevision(context.Context, BotID, BotTaskRef) (*BotTaskRevision, error)
	PrepareTaskPublication(context.Context, BotTaskPrepareRequest) (*BotTaskPublication, error)
	CommitTaskPublication(context.Context, BotTaskCommitRequest) (*BotTask, error)
	ChangeTaskLifecycle(context.Context, BotTaskLifecycleRequest) (*BotTask, error)
}
type BotTaskRecoveryStore interface {
	ListPendingTaskPublications(context.Context, BotID, BotTaskPage) ([]BotTaskPublication, error)
	HoldTaskPublications(context.Context, BotID, string) error
	AbortTaskPublication(context.Context, BotTaskMutation, uuid.UUID, string) error
}
