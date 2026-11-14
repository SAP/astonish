package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

const BotKnowledgeExtractionSchemaVersion = 1
const BotKnowledgeExtractionMaxInputBytes = 256 * 1024
const BotKnowledgeExtractionMaxOutputBytes = 64 * 1024

type BotKnowledgeExtractionState string

const (
	BotKnowledgeExtractionPending    BotKnowledgeExtractionState = "pending"
	BotKnowledgeExtractionExtracting BotKnowledgeExtractionState = "extracting"
	BotKnowledgeExtractionPrepared   BotKnowledgeExtractionState = "prepared"
	BotKnowledgeExtractionFilesReady BotKnowledgeExtractionState = "files_ready"
	BotKnowledgeExtractionCommitted  BotKnowledgeExtractionState = "committed"
	BotKnowledgeExtractionNoChange   BotKnowledgeExtractionState = "no_change"
	BotKnowledgeExtractionHeld       BotKnowledgeExtractionState = "held"
	BotKnowledgeExtractionAborted    BotKnowledgeExtractionState = "aborted"
)

// Source is immutable issuer-written provenance. The scoped repository verifies
// the committed run/outcome and sequence; a caller-supplied identity is not authority.
// Unique identity is the ENTIRE tuple, not a transient lease or prompt digest.
type BotKnowledgeExtractionSource struct {
	BotID         BotID  `json:"bot_id"`
	RunID         RunID  `json:"run_id"`
	OutcomeDigest string `json:"outcome_digest"`
	Sequence      int64  `json:"sequence"`
	Version       int64  `json:"version"`
}

func (s BotKnowledgeExtractionSource) Validate() error {
	if s.BotID == uuid.Nil || s.RunID == uuid.Nil || s.Sequence < 1 || s.Version < 1 || !knowledgeDigest(s.OutcomeDigest) {
		return ErrBotInvalid
	}
	return nil
}

type BotKnowledgeToolEvidence struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
	Failure   string `json:"failure,omitempty"`
}
type BotKnowledgeFileSummary struct {
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Summary string `json:"summary"`
}
type BotKnowledgeOutcome struct {
	Status  string                     `json:"status"`
	Answer  string                     `json:"answer"`
	Failure string                     `json:"failure,omitempty"`
	Review  string                     `json:"review,omitempty"`
	Tools   []BotKnowledgeToolEvidence `json:"tools"`
}

// Complete means no source history, actual calls/results or reviews were silently
// truncated. Oversize sources must be held, not fed a misleading partial snapshot.
// Upstream must redact credentials before creating this durable source snapshot.
type BotKnowledgeExtractionInput struct {
	SchemaVersion int                          `json:"schema_version"`
	Source        BotKnowledgeExtractionSource `json:"source"`
	Complete      bool                         `json:"complete"`
	OwnerMessage  string                       `json:"owner_message"`
	Conversation  string                       `json:"conversation"`
	Outcome       BotKnowledgeOutcome          `json:"outcome"`
	PriorFiles    []BotKnowledgeFileSummary    `json:"prior_files"`
}

func BotKnowledgeDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func knowledgeDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func (i BotKnowledgeExtractionInput) Validate() error {
	b, err := json.Marshal(i)
	if err != nil || len(b) > BotKnowledgeExtractionMaxInputBytes || i.SchemaVersion != BotKnowledgeExtractionSchemaVersion || !i.Complete || i.Source.Validate() != nil || strings.TrimSpace(i.OwnerMessage) == "" || strings.TrimSpace(i.Outcome.Status) == "" || BotKnowledgeDigest(i.Outcome) != i.Source.OutcomeDigest {
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	for _, tool := range i.Outcome.Tools {
		if tool.ID == "" || tool.Name == "" || seen[tool.ID] {
			return ErrBotInvalid
		}
		seen[tool.ID] = true
	}
	seen = map[string]bool{}
	for _, file := range i.PriorFiles {
		if !knowledgeSummaryPath(file.Path) || !knowledgeDigest(file.Digest) || seen[file.Path] {
			return ErrBotInvalid
		}
		seen[file.Path] = true
	}
	return nil
}
func (i BotKnowledgeExtractionInput) Clone() BotKnowledgeExtractionInput {
	i.Outcome.Tools = append([]BotKnowledgeToolEvidence(nil), i.Outcome.Tools...)
	i.PriorFiles = append([]BotKnowledgeFileSummary(nil), i.PriorFiles...)
	return i
}

type BotKnowledgeProposalKind string

const (
	BotKnowledgeProposalKnowledge  BotKnowledgeProposalKind = "knowledge"
	BotKnowledgeProposalPreference BotKnowledgeProposalKind = "preference"
	BotKnowledgeProposalProcedure  BotKnowledgeProposalKind = "procedure"
	BotKnowledgeProposalFailure    BotKnowledgeProposalKind = "failure"
)

type BotKnowledgeExtractionDecisionKind string

const (
	BotKnowledgeDecisionChanges  BotKnowledgeExtractionDecisionKind = "changes"
	BotKnowledgeDecisionNoChange BotKnowledgeExtractionDecisionKind = "no_change"
)

// Proposals are inert Markdown, never executable assets, grants, Goals, or Tasks.
// ExpectedDigest is the prior file CAS token; empty means the file must not exist.
// EvidenceIDs refer to actual source tool calls, not invented certification.
type BotKnowledgeProposal struct {
	Kind           BotKnowledgeProposalKind `json:"kind"`
	Path           string                   `json:"path"`
	ExpectedDigest string                   `json:"expected_digest"`
	Content        string                   `json:"content"`
	EvidenceIDs    []string                 `json:"evidence_ids"`
}
type BotKnowledgeExtractionDecision struct {
	SchemaVersion int                                `json:"schema_version"`
	Kind          BotKnowledgeExtractionDecisionKind `json:"kind"`
	Reason        string                             `json:"reason"`
	Proposals     []BotKnowledgeProposal             `json:"proposals"`
}

func knowledgeSummaryPath(p string) bool {
	if len(p) == 0 || len(p) > 240 || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return true
}
func knowledgePath(p string) bool {
	return knowledgeSummaryPath(p) && strings.HasSuffix(p, ".md") && (strings.HasPrefix(p, "knowledge/") || strings.HasPrefix(p, "preferences/") || strings.HasPrefix(p, "procedures/") || strings.HasPrefix(p, "failures/"))
}
func (d BotKnowledgeExtractionDecision) Validate() error {
	b, err := json.Marshal(d)
	if err != nil || len(b) > BotKnowledgeExtractionMaxOutputBytes || d.SchemaVersion != BotKnowledgeExtractionSchemaVersion || strings.TrimSpace(d.Reason) == "" || len(d.Proposals) > 32 {
		return ErrBotInvalid
	}
	switch d.Kind {
	case BotKnowledgeDecisionNoChange:
		if len(d.Proposals) != 0 {
			return ErrBotInvalid
		}
	case BotKnowledgeDecisionChanges:
		if len(d.Proposals) == 0 {
			return ErrBotInvalid
		}
	default:
		return ErrBotInvalid
	}
	seen := map[string]bool{}
	for _, p := range d.Proposals {
		var prefix string
		switch p.Kind {
		case BotKnowledgeProposalKnowledge:
			prefix = "knowledge/"
		case BotKnowledgeProposalPreference:
			prefix = "preferences/"
		case BotKnowledgeProposalProcedure:
			prefix = "procedures/"
		case BotKnowledgeProposalFailure:
			prefix = "failures/"
		default:
			return ErrBotInvalid
		}
		if !knowledgePath(p.Path) || !strings.HasPrefix(p.Path, prefix) || seen[p.Path] || strings.TrimSpace(p.Content) == "" || p.ExpectedDigest != "" && !knowledgeDigest(p.ExpectedDigest) {
			return ErrBotInvalid
		}
		seen[p.Path] = true
		ids := map[string]bool{}
		for _, id := range p.EvidenceIDs {
			if id == "" || ids[id] {
				return ErrBotInvalid
			}
			ids[id] = true
		}
	}
	return nil
}
func (d BotKnowledgeExtractionDecision) Clone() BotKnowledgeExtractionDecision {
	d.Proposals = append([]BotKnowledgeProposal(nil), d.Proposals...)
	for n := range d.Proposals {
		d.Proposals[n].EvidenceIDs = append([]string(nil), d.Proposals[n].EvidenceIDs...)
	}
	return d
}

// This is a NEW extraction lease, never the now-terminal source run lease.
// Each mutation rechecks owner scope, revocation, cancellation and current fence.
type BotKnowledgeExtractionLease struct {
	BotID        BotID     `json:"bot_id"`
	ExtractionID uuid.UUID `json:"extraction_id"`
	WorkerID     string    `json:"worker_id"`
	Fence        int64     `json:"fence"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (l BotKnowledgeExtractionLease) Validate(now time.Time) error {
	if l.BotID == uuid.Nil || l.ExtractionID == uuid.Nil || strings.TrimSpace(l.WorkerID) == "" || l.Fence < 1 || !now.Before(l.ExpiresAt) {
		return ErrBotInvalid
	}
	return nil
}

type BotKnowledgeExtraction struct {
	ID             uuid.UUID                       `json:"id"`
	Input          BotKnowledgeExtractionInput     `json:"input"`
	State          BotKnowledgeExtractionState     `json:"state"`
	Revision       int64                           `json:"revision"`
	AttemptID      uuid.UUID                       `json:"attempt_id"`
	Decision       *BotKnowledgeExtractionDecision `json:"decision,omitempty"`
	DecisionDigest string                          `json:"decision_digest,omitempty"`
	HoldReason     string                          `json:"hold_reason,omitempty"`
}

func (r BotKnowledgeExtraction) Clone() BotKnowledgeExtraction {
	r.Input = r.Input.Clone()
	if r.Decision != nil {
		d := r.Decision.Clone()
		r.Decision = &d
	}
	return r
}

// Held can ONLY retry deterministic prepared persistence with a persisted decision;
// an uncertain paid attempt without a decision can only be explicitly aborted.
func ValidateBotKnowledgeExtractionTransition(from, to BotKnowledgeExtractionState, hasDecision bool) error {
	valid := false
	switch from {
	case BotKnowledgeExtractionPending:
		valid = to == BotKnowledgeExtractionExtracting || to == BotKnowledgeExtractionHeld || to == BotKnowledgeExtractionAborted
	case BotKnowledgeExtractionExtracting:
		valid = to == BotKnowledgeExtractionPrepared || to == BotKnowledgeExtractionNoChange || to == BotKnowledgeExtractionHeld
	case BotKnowledgeExtractionPrepared:
		valid = to == BotKnowledgeExtractionFilesReady || to == BotKnowledgeExtractionHeld || to == BotKnowledgeExtractionAborted
	case BotKnowledgeExtractionFilesReady:
		valid = to == BotKnowledgeExtractionCommitted || to == BotKnowledgeExtractionHeld || to == BotKnowledgeExtractionAborted
	case BotKnowledgeExtractionHeld:
		valid = to == BotKnowledgeExtractionAborted || hasDecision && to == BotKnowledgeExtractionPrepared
	}
	if !valid {
		return ErrBotConflict
	}
	return nil
}

// Readback proves actual resulting bytes of EVERY proposal at the exact fenced
// publication. Digest equality alone is not permission to publish.
type BotKnowledgeFileReadback struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type BotKnowledgeExtractionTransition struct {
	Lease            BotKnowledgeExtractionLease
	ExpectedRevision int64
	From             BotKnowledgeExtractionState
	To               BotKnowledgeExtractionState
	Decision         *BotKnowledgeExtractionDecision
	DecisionDigest   string
	Readback         []BotKnowledgeFileReadback
	Reason           string
}

// Validate checks mutation shape/integrity, not repository scope or current
// database authority. Implementations additionally compare the live lease/fence,
// recheck revocation/cancellation, and CAS every expected file digest.
func (q BotKnowledgeExtractionTransition) Validate(current BotKnowledgeExtraction, now time.Time) error {
	if q.Lease.Validate(now) != nil || q.Lease.ExtractionID != current.ID || q.Lease.BotID != current.Input.Source.BotID || q.ExpectedRevision != current.Revision || q.From != current.State {
		return ErrBotConflict
	}
	if err := ValidateBotKnowledgeExtractionTransition(q.From, q.To, current.Decision != nil); err != nil {
		return err
	}
	if q.To == BotKnowledgeExtractionHeld && strings.TrimSpace(q.Reason) == "" {
		return ErrBotInvalid
	}
	decision := current.Decision
	digest := current.DecisionDigest
	if q.Decision != nil {
		if q.From != BotKnowledgeExtractionExtracting || (q.To != BotKnowledgeExtractionPrepared && q.To != BotKnowledgeExtractionNoChange) {
			return ErrBotConflict
		}
		if q.Decision.Validate() != nil || q.DecisionDigest != BotKnowledgeDigest(*q.Decision) {
			return ErrBotInvalid
		}
		if decision != nil && digest != q.DecisionDigest {
			return ErrBotConflict
		}
		decision = q.Decision
		digest = q.DecisionDigest
	}
	switch q.To {
	case BotKnowledgeExtractionPrepared, BotKnowledgeExtractionFilesReady, BotKnowledgeExtractionCommitted:
		if decision == nil || decision.Kind != BotKnowledgeDecisionChanges || digest != BotKnowledgeDigest(*decision) {
			return ErrBotInvalid
		}
	case BotKnowledgeExtractionNoChange:
		if decision == nil || decision.Kind != BotKnowledgeDecisionNoChange || digest != BotKnowledgeDigest(*decision) {
			return ErrBotInvalid
		}
	}
	if q.To == BotKnowledgeExtractionFilesReady || q.To == BotKnowledgeExtractionCommitted {
		if len(q.Readback) != len(decision.Proposals) {
			return ErrBotInvalid
		}
		readback := map[string]string{}
		for _, f := range q.Readback {
			if _, exists := readback[f.Path]; exists {
				return ErrBotInvalid
			}
			readback[f.Path] = f.Digest
		}
		for _, p := range decision.Proposals {
			sum := sha256.Sum256([]byte(p.Content))
			if readback[p.Path] != hex.EncodeToString(sum[:]) {
				return ErrBotConflict
			}
		}
	}
	return nil
}

// Optional extension: do not add this to generic BotStore or grant child Tasks.
// Enqueue verifies immutable committed source and deduplicates the full tuple,
// returning clone-isolated records. Claim obtains fresh extraction authority.
// Begin atomically persists AttemptID + Extracting BEFORE allowing any paid call;
// started=false on duplicate, including uncertain Extracting/Held. A lost Begin
// acknowledgement must NOT allow a call. There is exactly one attempt per source.
// Transition is revision+lease/fence CAS; Prepared decision is immutable. Recovery
// reclaims fresh authority and retries only stored decisions. FilesReady requires
// all expected file digest CAS checks AND exact readback of staged durable bytes;
// Committed atomically publishes the scoped head/receipt after rechecking authority.
// Source run success is NOT extraction completion. Expired Extracting is Held,
// never Pending. No-change is a durable, explicit terminal decision.
type BotKnowledgeExtractionStore interface {
	EnqueueBotKnowledgeExtraction(context.Context, BotKnowledgeExtractionInput) (*BotKnowledgeExtraction, error)
	ReadBotKnowledgeExtraction(context.Context, BotKnowledgeExtractionSource) (*BotKnowledgeExtraction, error)
	ClaimBotKnowledgeExtraction(context.Context, BotKnowledgeExtractionSource, string, time.Duration) (BotKnowledgeExtractionLease, error)
	BeginBotKnowledgeExtraction(context.Context, BotKnowledgeExtractionLease, int64) (record *BotKnowledgeExtraction, started bool, err error)
	TransitionBotKnowledgeExtraction(context.Context, BotKnowledgeExtractionTransition) (*BotKnowledgeExtraction, error)
}
