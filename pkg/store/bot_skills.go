package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Bot skills are Bot-owned operating knowledge. Neither chat memory, scheduler
// payloads nor general skill catalogues implement this contract. Validation here
// checks shape/integrity, NOT authority or the truth of model-authored content.
const (
	BotSkillSchemaVersion    = 1
	BotSkillMaxSkills        = 100
	BotSkillMaxRevisionBytes = 64 << 10
	BotSkillMaxSources       = 64
	BotSkillMaxExpectations  = 64
	BotSkillMaxMethodSteps   = 128
	BotSkillMaxEvidenceRefs  = 128
	BotSkillMaxPageSize      = 100
	BotSkillMaxRunSkills     = 4
	BotSkillMaxRunBytes      = 256 << 10
	BotSkillMaxRevisions     = 1000
	BotSkillMaxTotalBytes    = 64 << 20
)

type BotSkillStatus string

const (
	BotSkillDraft    BotSkillStatus = "draft"
	BotSkillActive   BotSkillStatus = "active"
	BotSkillArchived BotSkillStatus = "archived"
)

type BotSkillEvidenceLevel string

const (
	BotSkillImported       BotSkillEvidenceLevel = "imported"
	BotSkillProposed       BotSkillEvidenceLevel = "proposed"
	BotSkillObserved       BotSkillEvidenceLevel = "observed"
	BotSkillOwnerSpecified BotSkillEvidenceLevel = "owner_specified"
)

type BotSkillStage string

const (
	BotSkillUnoriented        BotSkillStage = "unoriented"
	BotSkillOriented          BotSkillStage = "oriented"
	BotSkillLearningStaged    BotSkillStage = "learning_staged"
	BotSkillLearningCommitted BotSkillStage = "learning_committed"
	BotSkillLearningDeferred  BotSkillStage = "learning_deferred"
)

type BotSkillReceiptKind string

const (
	BotSkillSelected  BotSkillReceiptKind = "selected"
	BotSkillCreated   BotSkillReceiptKind = "created"
	BotSkillUpdated   BotSkillReceiptKind = "updated"
	BotSkillUnchanged BotSkillReceiptKind = "unchanged"
	BotSkillConflict  BotSkillReceiptKind = "conflict"
	BotSkillDeferred  BotSkillReceiptKind = "deferred"
	BotSkillRejected  BotSkillReceiptKind = "rejected"
)

type BotSkillOrientationKind string

const (
	BotSkillNoSkillNeeded           BotSkillOrientationKind = "no_skill_needed"
	BotSkillUseExisting             BotSkillOrientationKind = "use_existing"
	BotSkillCreateDraft             BotSkillOrientationKind = "create_draft"
	BotSkillUpdateOwnerExpectations BotSkillOrientationKind = "update_owner_expectations"
	BotSkillNeedClarification       BotSkillOrientationKind = "need_clarification"
)

type BotSkill struct {
	ID           uuid.UUID                  `json:"id"`
	BotID        BotID                      `json:"bot_id"`
	Key          string                     `json:"key"`
	Title        string                     `json:"title"`
	GoalID       *uuid.UUID                 `json:"goal_id,omitempty"`
	Status       BotSkillStatus             `json:"status"`
	Revision     int64                      `json:"revision"`
	HeadRevision int64                      `json:"head_revision"`
	HeadDigest   string                     `json:"head_digest"`
	Legacy       *BotSkillLegacyAssociation `json:"legacy,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	UpdatedAt    time.Time                  `json:"updated_at"`
}

// BotSkillProvenanceSource distinguishes canonical admission from legacy event/goal sources.
// The empty value retains the original JSON representation and revision digests.
type BotSkillProvenanceSource string

const BotSkillSourceRunAdmission BotSkillProvenanceSource = "run_admission"

// Provenance is resolved against canonical records by the scoped repository.
// Its presence in JSON alone never attests an observation or owner instruction.
type BotSkillProvenance struct {
	Source       BotSkillProvenanceSource   `json:"source,omitempty"`
	Legacy       *BotSkillLegacyAssociation `json:"legacy,omitempty"`
	Level        BotSkillEvidenceLevel      `json:"level"`
	BotID        BotID                      `json:"bot_id"`
	RunID        RunID                      `json:"run_id,omitempty"`
	SessionID    uuid.UUID                  `json:"session_id,omitempty"`
	EventID      string                     `json:"event_id,omitempty"`
	AttachmentID string                     `json:"attachment_id,omitempty"`
	GoalID       *uuid.UUID                 `json:"goal_id,omitempty"`
	GoalRevision int64                      `json:"goal_revision,omitempty"`
	SourceDigest string                     `json:"source_digest,omitempty"`
	Quote        string                     `json:"quote,omitempty"`
	EvidenceRefs []string                   `json:"evidence_refs,omitempty"`
	RecordedAt   time.Time                  `json:"recorded_at"`
}
type BotSkillExpectation struct {
	ID         string             `json:"id"`
	Text       string             `json:"text"`
	Provenance BotSkillProvenance `json:"provenance"`
}
type BotSkillSourceObservation struct {
	Status     BotSourceAccessStatus `json:"status"`
	HTTPStatus int                   `json:"http_status,omitempty"`
	CallID     string                `json:"call_id"`
	Truncated  bool                  `json:"truncated,omitempty"`
}
type BotSkillSource struct {
	Observation    *BotSkillSourceObservation `json:"observation,omitempty"`
	URL            string                     `json:"url"`
	Purpose        string                     `json:"purpose"`
	AccessMethod   string                     `json:"access_method"`
	Fallback       string                     `json:"fallback,omitempty"`
	LastObservedAt *time.Time                 `json:"last_observed_at,omitempty"`
	Limitation     string                     `json:"limitation,omitempty"`
	Provenance     BotSkillProvenance         `json:"provenance"`
}
type BotSkillRevision struct {
	SchemaVersion   int                   `json:"schema_version"`
	ID              uuid.UUID             `json:"id"`
	BotID           BotID                 `json:"bot_id"`
	SkillID         uuid.UUID             `json:"skill_id"`
	Revision        int64                 `json:"revision"`
	ParentRevision  int64                 `json:"parent_revision"`
	ParentDigest    string                `json:"parent_digest,omitempty"`
	Digest          string                `json:"digest"`
	Responsibility  string                `json:"responsibility"`
	Expectations    []BotSkillExpectation `json:"expectations"`
	Method          []string              `json:"method"`
	Sources         []BotSkillSource      `json:"sources,omitempty"`
	OutputFormat    string                `json:"output_format,omitempty"`
	EvaluationRules []string              `json:"evaluation_rules,omitempty"`
	Provenance      []BotSkillProvenance  `json:"provenance"`
	ChangeReason    string                `json:"change_reason"`
	RevertsRevision *int64                `json:"reverts_revision,omitempty"`
}

// AuthorityConstraintRef identifies an existing immutable authority envelope;
// neither that reference nor the skill snapshot grants new capabilities.
type BotSkillInvocation struct {
	Intent                 string           `json:"intent"`
	SkillID                uuid.UUID        `json:"skill_id"`
	Revision               int64            `json:"revision"`
	Digest                 string           `json:"digest"`
	Snapshot               BotSkillRevision `json:"snapshot"`
	AuthorityConstraintRef string           `json:"authority_constraint_ref,omitempty"`
}

// Learning proposes a full replacement snapshot so no implicit merge can erase
// expectations. Commit compares its parent, owner sources and actual run evidence.
type BotSkillLearning struct {
	SkillID         uuid.UUID        `json:"skill_id"`
	ParentRevision  int64            `json:"parent_revision"`
	ParentDigest    string           `json:"parent_digest"`
	SourceRunID     RunID            `json:"source_run_id"`
	CandidateDigest string           `json:"candidate_digest"`
	EvidenceDigest  string           `json:"evidence_digest"`
	Proposed        BotSkillRevision `json:"proposed"`
}

type BotSkillReceipt struct {
	SchemaVersion     int                 `json:"schema_version"`
	BotID             BotID               `json:"bot_id"`
	RunID             RunID               `json:"run_id"`
	SkillID           uuid.UUID           `json:"skill_id"`
	Kind              BotSkillReceiptKind `json:"kind"`
	UsedRevision      int64               `json:"used_revision"`
	CommittedRevision int64               `json:"committed_revision,omitempty"`
	Digest            string              `json:"digest,omitempty"`
	Reason            string              `json:"reason,omitempty"`
}
type BotSkillProposal struct {
	CallID          string   `json:"call_id"`
	Method          []string `json:"method,omitempty"`
	OutputFormat    string   `json:"output_format,omitempty"`
	EvaluationRules []string `json:"evaluation_rules,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
}

// HasEvidence requires support explicitly supplied by this method proposal.
// Unrelated access facts and provenance inherited from an older revision are not
// evidence of the proposed procedure. Semantic usefulness remains agent judgment.
func (p *BotSkillProposal) HasEvidence(evidence []BotWorkEvidenceReference) bool {
	if p == nil || len(p.EvidenceRefs) == 0 {
		return false
	}
	for _, ref := range p.EvidenceRefs {
		found := false
		for _, e := range evidence {
			if e.Ref == ref {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type BotSkillRunState struct {
	// SelectedAt is server-recorded. Evidence captured before this boundary cannot
	// retrospectively establish use, even if it is bound to the journal later.
	SelectedAt        *time.Time        `json:"selected_at,omitempty"`
	SelectionRevision int64             `json:"selection_revision,omitempty"`
	CreatedDuringWork bool              `json:"created_during_work,omitempty"`
	Proposal          *BotSkillProposal `json:"proposal,omitempty"`
	Stage             BotSkillStage     `json:"stage"`
	NoSkillNeeded     bool              `json:"no_skill_needed,omitempty"`
	Learning          *BotSkillLearning `json:"learning,omitempty"`
	Receipt           *BotSkillReceipt  `json:"receipt,omitempty"`
}
type BotSkillLegacyAssociation struct {
	TriggerID          uuid.UUID `json:"trigger_id"`
	Version            int64     `json:"version"`
	InstructionsDigest string    `json:"instructions_digest"`
	GrantDigest        string    `json:"grant_digest,omitempty"`
}
type BotSkillPage struct {
	Limit           int    `json:"limit"`
	After           string `json:"after,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
}
type BotSkillList struct {
	Skills []BotSkill `json:"skills"`
	Next   string     `json:"next,omitempty"`
}
type BotSkillRevisionList struct {
	Revisions []BotSkillRevision `json:"revisions"`
	Next      string             `json:"next,omitempty"`
}
type BotSkillRunList struct {
	Runs []BotRun `json:"runs"`
	Next string   `json:"next,omitempty"`
}

// These requests are trusted service contracts, not HTTP authorization DTOs.
// Repositories recheck scope, canonical source, lease, CAS and idempotency hash.
type BotSkillMutation struct {
	BotID            BotID
	Lease            BotLease
	ActorID          uuid.UUID
	ExpectedRevision int64
	IdempotencyKey   string
	RequestDigest    string
	Source           BotSkillProvenance
}
type BotSkillOrientation struct {
	Mutation               BotSkillMutation
	Kind                   BotSkillOrientationKind
	SkillID                *uuid.UUID
	Key                    string
	Title                  string
	GoalID                 *uuid.UUID
	Intent                 string
	Draft                  *BotSkillRevision
	AuthorityConstraintRef string
}
type BotSkillOrientationResult struct {
	Invocation *BotSkillInvocation
	Receipt    *BotSkillReceipt
	Work       *BotWorkState
}
type BotSkillLearningRequest struct {
	Proposal *BotSkillProposal
	// Failure records a bounded, non-sensitive pre-stage outcome under the same lease.
	Failure              BotSkillReceiptKind
	Mutation             BotSkillMutation
	ExpectedWorkRevision int64
	Learning             BotSkillLearning
}
type BotSkillLifecycleRequest struct {
	Mutation BotSkillMutation
	SkillID  uuid.UUID
	Status   BotSkillStatus
	Reason   string
}
type BotSkillLegacyConversion struct {
	Mutation            BotSkillMutation
	ExpectedBotRevision int64
	TriggerID           uuid.UUID
	Version             int64
	InstructionsDigest  string
}

type BotSkillStore interface {
	ListSkills(context.Context, BotID, BotSkillPage) (*BotSkillList, error)
	GetSkill(context.Context, BotID, uuid.UUID) (*BotSkill, error)
	GetSkillRevision(context.Context, BotID, uuid.UUID, int64) (*BotSkillRevision, error)
	ListSkillRevisions(context.Context, BotID, uuid.UUID, BotSkillPage) (*BotSkillRevisionList, error)
	ListSkillRuns(context.Context, BotID, uuid.UUID, BotSkillPage) (*BotSkillRunList, error)
	OrientSkill(context.Context, BotSkillOrientation) (*BotSkillOrientationResult, error)
	StageSkillLearning(context.Context, BotSkillLearningRequest) (*BotWorkState, error)
	ChangeSkillLifecycle(context.Context, BotSkillLifecycleRequest) (*BotSkill, error)
	ConvertLegacySkill(context.Context, BotSkillLegacyConversion) (*BotSkill, error)
}

func skillDigestValid(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}
func skillText(s string) bool { return strings.TrimSpace(s) != "" }
func (p BotSkillProvenance) Validate(botID BotID) error {
	if botID == uuid.Nil || p.BotID != botID {
		return ErrBotForbidden
	}
	if p.Source != "" {
		if p.Source != BotSkillSourceRunAdmission {
			return ErrBotUnsupported
		}
		if p.Level != BotSkillOwnerSpecified || p.RunID == uuid.Nil || p.SessionID == uuid.Nil || p.EventID != "" || p.AttachmentID != "" || p.GoalID != nil || p.GoalRevision != 0 || p.Legacy != nil || len(p.EvidenceRefs) != 0 || !skillDigestValid(p.SourceDigest) || !skillText(p.Quote) || p.RecordedAt.IsZero() {
			return ErrBotInvalid
		}
		return nil
	}
	switch p.Level {
	case BotSkillImported:
		if p.Legacy == nil || p.Legacy.TriggerID == uuid.Nil || p.Legacy.Version < 1 || !skillDigestValid(p.Legacy.InstructionsDigest) || p.SourceDigest != p.Legacy.InstructionsDigest || p.RecordedAt.IsZero() || len(p.EvidenceRefs) != 0 {
			return ErrBotInvalid
		}
	case BotSkillProposed:
		if len(p.EvidenceRefs) != 0 {
			return ErrBotInvalid
		}
	case BotSkillObserved, BotSkillOwnerSpecified:
		if !skillDigestValid(p.SourceDigest) || p.RecordedAt.IsZero() {
			return ErrBotInvalid
		}
		event := p.RunID != uuid.Nil && p.SessionID != uuid.Nil && skillText(p.EventID)
		goal := p.GoalID != nil && *p.GoalID != uuid.Nil && p.GoalRevision > 0
		if !event && !(p.Level == BotSkillOwnerSpecified && goal) {
			return ErrBotInvalid
		}
		if p.Level == BotSkillOwnerSpecified && !skillText(p.Quote) {
			return ErrBotInvalid
		}
		if p.Level == BotSkillObserved && len(p.EvidenceRefs) == 0 {
			return ErrBotInvalid
		}
	default:
		return ErrBotInvalid
	}
	if len(p.EvidenceRefs) > BotSkillMaxEvidenceRefs {
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	for _, ref := range p.EvidenceRefs {
		if !skillText(ref) || seen[ref] {
			return ErrBotInvalid
		}
		seen[ref] = true
	}
	if p.GoalID != nil && (*p.GoalID == uuid.Nil || p.GoalRevision < 1) {
		return ErrBotInvalid
	}
	return nil
}
func (r BotSkillRevision) ContentDigest() string { r.Digest = ""; return botWorkDigest(r) }

// SameKnowledge ignores only the revision envelope, never evidence, source
// timestamps or owner provenance. ContentDigest remains the canonical wire hash.
func (r BotSkillRevision) SameKnowledge(other BotSkillRevision) bool {
	normalize := func(v BotSkillRevision) BotSkillRevision {
		v.ID = uuid.Nil
		v.Revision, v.ParentRevision = 0, 0
		v.ParentDigest, v.Digest, v.ChangeReason = "", "", ""
		return v
	}
	return botWorkDigest(normalize(r)) == botWorkDigest(normalize(other))
}
func (r BotSkillRevision) Clone() *BotSkillRevision {
	raw, _ := json.Marshal(r)
	var out BotSkillRevision
	_ = json.Unmarshal(raw, &out)
	return &out
}
func (r BotSkillRevision) Validate() error {
	if r.SchemaVersion != BotSkillSchemaVersion {
		return ErrBotUnsupported
	}
	if r.ID == uuid.Nil || r.BotID == uuid.Nil || r.SkillID == uuid.Nil || r.Revision < 1 || r.Revision > BotSkillMaxRevisions || !skillText(r.Responsibility) || !skillText(r.ChangeReason) || r.Digest != r.ContentDigest() {
		return ErrBotInvalid
	}
	if r.ParentRevision != r.Revision-1 || (r.Revision == 1 && r.ParentDigest != "") || (r.Revision > 1 && !skillDigestValid(r.ParentDigest)) {
		return ErrBotInvalid
	}
	if r.RevertsRevision != nil && (*r.RevertsRevision < 1 || *r.RevertsRevision >= r.Revision) {
		return ErrBotInvalid
	}
	if len(r.Expectations) == 0 || len(r.Expectations) > BotSkillMaxExpectations || len(r.Method) == 0 || len(r.Method) > BotSkillMaxMethodSteps || len(r.Sources) > BotSkillMaxSources || len(r.EvaluationRules) > BotSkillMaxMethodSteps || len(r.Provenance) == 0 || len(r.Provenance) > BotSkillMaxEvidenceRefs {
		return ErrBotInvalid
	}
	encoded, _ := json.Marshal(r)
	if len(encoded) > BotSkillMaxRevisionBytes {
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	evidenceCount := 0
	for _, e := range r.Expectations {
		if !skillText(e.ID) || seen[e.ID] || !skillText(e.Text) || (e.Provenance.Level != BotSkillOwnerSpecified && e.Provenance.Level != BotSkillImported) {
			return ErrBotInvalid
		}
		if e.Provenance.Level == BotSkillOwnerSpecified && e.Text != e.Provenance.Quote {
			return ErrBotInvalid
		}
		if err := e.Provenance.Validate(r.BotID); err != nil {
			return err
		}
		seen[e.ID] = true
		evidenceCount += len(e.Provenance.EvidenceRefs)
	}
	for _, steps := range [][]string{r.Method, r.EvaluationRules} {
		for _, step := range steps {
			if !skillText(step) {
				return ErrBotInvalid
			}
		}
	}
	for _, s := range r.Sources {
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || !skillText(s.Purpose) || !skillText(s.AccessMethod) {
			return ErrBotInvalid
		}
		if err := s.Provenance.Validate(r.BotID); err != nil {
			return err
		}
		if s.LastObservedAt != nil && (s.LastObservedAt.IsZero() || s.Provenance.Level != BotSkillObserved) {
			return ErrBotInvalid
		}
		evidenceCount += len(s.Provenance.EvidenceRefs)
	}
	for _, p := range r.Provenance {
		if err := p.Validate(r.BotID); err != nil {
			return err
		}
		evidenceCount += len(p.EvidenceRefs)
	}
	if evidenceCount > BotSkillMaxEvidenceRefs {
		return ErrBotInvalid
	}
	return nil
}
func (i BotSkillInvocation) Clone() *BotSkillInvocation {
	out := i
	out.Snapshot = *i.Snapshot.Clone()
	return &out
}
func (i BotSkillInvocation) Validate(botID BotID) error {
	if i.Snapshot.BotID != botID {
		return ErrBotForbidden
	}
	if !skillText(i.Intent) || len(i.Intent) > 4096 || i.SkillID != i.Snapshot.SkillID || i.Revision != i.Snapshot.Revision || i.Digest != i.Snapshot.Digest || len(i.AuthorityConstraintRef) > 1024 {
		return ErrBotInvalid
	}
	return i.Snapshot.Validate()
}
func (p BotSkillPage) Validate() error {
	if p.Limit < 0 || p.Limit > BotSkillMaxPageSize || len(p.After) > 200 {
		return ErrBotInvalid
	}
	return nil
}
func ValidateBotSkillQuota(skills int, usedBytes, additionalBytes int64) error {
	if skills < 0 || skills >= BotSkillMaxSkills || usedBytes < 0 || additionalBytes < 0 || additionalBytes > BotSkillMaxTotalBytes || usedBytes > BotSkillMaxTotalBytes-additionalBytes {
		return fmt.Errorf("%w: Bot skill quota", ErrBotInvalid)
	}
	return nil
}
func ValidateBotSkillLifecycle(from, to BotSkillStatus, explicit, verified bool) error {
	if !from.valid() || !to.valid() {
		return ErrBotInvalid
	}
	if from == to {
		return nil
	}
	if from == BotSkillDraft && to == BotSkillActive && verified {
		return nil
	}
	if to == BotSkillArchived && explicit {
		return nil
	}
	if from == BotSkillArchived && explicit && (to == BotSkillDraft || to == BotSkillActive && verified) {
		return nil
	}
	return ErrBotConflict
}
func (s BotSkillStatus) valid() bool {
	return s == BotSkillDraft || s == BotSkillActive || s == BotSkillArchived
}
func ValidateBotSkillStage(from, to BotSkillStage) error {
	if !from.valid() || !to.valid() {
		return ErrBotInvalid
	}
	if from == to {
		return nil
	} // Suspension retains progress; does not imply replay.
	if from == BotSkillUnoriented && to == BotSkillOriented || from == BotSkillOriented && (to == BotSkillLearningStaged || to == BotSkillLearningDeferred) || from == BotSkillLearningStaged && (to == BotSkillLearningCommitted || to == BotSkillLearningDeferred) {
		return nil
	}
	return ErrBotConflict
}
func (s BotSkillStage) valid() bool {
	return s == BotSkillUnoriented || s == BotSkillOriented || s == BotSkillLearningStaged || s == BotSkillLearningCommitted || s == BotSkillLearningDeferred
}

// ValidateSkillSelection is additive: absent fields preserve legacy JSON and
// behavior. Store adapters still resolve ownership and authorization separately.
func ValidateSkillSelection(id *uuid.UUID, intent string) error {
	if id == nil {
		if intent != "" {
			return ErrBotInvalid
		}
		return nil
	}
	if *id == uuid.Nil || !skillText(intent) || len(intent) > 4096 {
		return ErrBotInvalid
	}
	return nil
}

func (s BotSkillRunState) Validate(contract BotWorkContract) error {
	if !s.Stage.valid() {
		return ErrBotInvalid
	}
	if s.NoSkillNeeded {
		if s.Stage != BotSkillOriented || contract.Skill != nil || s.Learning != nil || s.Receipt != nil {
			return ErrBotInvalid
		}
		return nil
	}
	if s.Stage == BotSkillUnoriented {
		if contract.Skill != nil || s.Learning != nil || s.Receipt != nil {
			return ErrBotInvalid
		}
		return nil
	}
	if contract.Skill == nil {
		return ErrBotInvalid
	}
	if err := contract.Skill.Validate(contract.BotID); err != nil {
		return err
	}
	if s.Stage == BotSkillOriented && s.Learning != nil {
		return ErrBotInvalid
	}
	if s.Stage == BotSkillLearningStaged || s.Stage == BotSkillLearningCommitted {
		if s.Learning == nil {
			return ErrBotInvalid
		}
	}
	if l := s.Learning; l != nil {
		if l.SkillID != contract.Skill.SkillID || l.SourceRunID != contract.RunID || l.ParentRevision != contract.Skill.Revision || l.ParentDigest != contract.Skill.Digest || !skillDigestValid(l.CandidateDigest) || !skillDigestValid(l.EvidenceDigest) || l.Proposed.BotID != contract.BotID || l.Proposed.SkillID != l.SkillID || l.Proposed.ParentRevision != l.ParentRevision || l.Proposed.ParentDigest != l.ParentDigest {
			return ErrBotInvalid
		}
		if err := l.Proposed.Validate(); err != nil {
			return err
		}
	}
	if s.Stage == BotSkillLearningCommitted || s.Stage == BotSkillLearningDeferred {
		if s.Receipt == nil {
			return ErrBotInvalid
		}
	}
	if r := s.Receipt; r != nil {
		if r.SchemaVersion != BotSkillSchemaVersion || r.BotID != contract.BotID || r.RunID != contract.RunID || r.SkillID != contract.Skill.SkillID || r.UsedRevision != contract.Skill.Revision {
			return ErrBotInvalid
		}
		switch s.Stage {
		case BotSkillOriented:
			if r.Kind != BotSkillSelected && r.Kind != BotSkillCreated && r.Kind != BotSkillUpdated {
				return ErrBotInvalid
			}
		case BotSkillLearningCommitted:
			if r.Kind != BotSkillUpdated && r.Kind != BotSkillUnchanged {
				return ErrBotInvalid
			}
			if r.Kind == BotSkillUpdated && (r.CommittedRevision != s.Learning.Proposed.Revision || r.Digest != s.Learning.Proposed.Digest) {
				return ErrBotInvalid
			}
			if r.Kind == BotSkillUnchanged && (r.CommittedRevision != contract.Skill.Revision || r.Digest != contract.Skill.Digest) {
				return ErrBotInvalid
			}
		case BotSkillLearningDeferred:
			if r.Kind != BotSkillDeferred && r.Kind != BotSkillConflict && r.Kind != BotSkillRejected {
				return ErrBotInvalid
			}
			if r.CommittedRevision != 0 || !skillText(r.Reason) {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
	}
	return nil
}
