package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// BotScheduledOccurrence contains identifiers only. The scoped store resolves the
// enabled schedule, version and instructions transactionally. An authenticated,
// live accountable owner is required; job payloads never establish authority.
type BotScheduledOccurrence struct {
	JobID          string
	BotID          BotID
	Version        int64
	TriggerID      RunID
	OccurrenceAt   time.Time
	IdempotencyKey string
}

type BotScheduledAdmission struct {
	// Acknowledged means the exact occurrence advanced in the admission transaction.
	Acknowledged bool
	Run          BotRun
	Duplicate    bool
}

// BotScheduledRunStore is an optional internal scheduler admission capability.
// HTTP RequestRun remains manual-only. Composition must supply an authenticated
// owner context and must not derive that principal from request/job hints.
type BotScheduledRunStore interface {
	AdmitScheduledRun(context.Context, BotScheduledOccurrence) (*BotScheduledAdmission, error)
}

// BotRunInput is durable input for one already admitted run. Instructions must
// come from the saved request, never from the worker's dispatch payload.
type BotRunInput struct {
	Work    *BotWorkState
	Bot     Bot
	Version BotVersion
	Run     BotRun
	Goals   []BotGoal
	// ExternalAdmissionValidated comes only from the persisted admission receipt.
	ExternalAdmissionValidated bool
	Instructions               string
	Attachments                []ChatFile
}

// BotRuntimeStore is an optional, owner-authenticated worker read capability.
// It does not admit, claim, resume, or authorize execution by itself. Implementors
// must recheck live scope membership and require the authenticated Bot owner.
// Service-principal delegation is intentionally not implied by this interface.
type BotRuntimeStore interface {
	LoadRunInput(context.Context, BotID, RunID) (*BotRunInput, error)
}

const (
	BotWorkSchemaVersion     = 1
	BotWorkMaxRepairs        = 2
	BotWorkMaxCriteria       = 64
	BotWorkMaxContractBytes  = 32 << 10
	BotWorkMaxCandidateBytes = 1 << 20
)

type BotWorkProtocol string

const (
	BotWorkProtocolLegacyAssessment BotWorkProtocol = ""
	BotWorkProtocolAgentLedV1       BotWorkProtocol = "agent_led_v1"
)

type BotWorkPhase string

// Agent-led execution phases are not legacy review verdicts.
const (
	BotWorkWorking  BotWorkPhase = "working"
	BotWorkFinished BotWorkPhase = "finished"
)

const (
	BotWorkPreparing      BotWorkPhase = "preparing"
	BotWorkProducing      BotWorkPhase = "producing"
	BotWorkCandidateReady BotWorkPhase = "candidate_ready"
	BotWorkReviewing      BotWorkPhase = "reviewing"
	BotWorkRepairReady    BotWorkPhase = "repair_ready"
	BotWorkRepairing      BotWorkPhase = "repairing"
	BotWorkAccepted       BotWorkPhase = "accepted"
	BotWorkBlocked        BotWorkPhase = "blocked"
	BotWorkInterrupted    BotWorkPhase = "interrupted"
)

type BotCriterionVerdict string

const (
	BotCriterionSatisfied  BotCriterionVerdict = "satisfied"
	BotCriterionFailed     BotCriterionVerdict = "failed"
	BotCriterionUnverified BotCriterionVerdict = "unverified"
)

type BotAssessmentMethod string

const (
	BotAssessmentMechanical BotAssessmentMethod = "mechanical_observation"
	BotAssessmentModel      BotAssessmentMethod = "model_assessment"
)

// BotWorkContract is source material, never an authorization grant. It is pinned
// by the repository at first claim and is immutable for every continuation.
type BotWorkContract struct {
	Skill            *BotSkillInvocation `json:"skill,omitempty"`
	BotID            BotID               `json:"bot_id"`
	RunID            RunID               `json:"run_id"`
	Version          int64               `json:"version"`
	AdmissionEventID string              `json:"admission_event_id,omitempty"`
	Instructions     string              `json:"instructions"`
	Charter          string              `json:"charter"`
	Goals            []BotGoal           `json:"goals,omitempty"`
	Attachments      []BotWorkAttachment `json:"attachments,omitempty"`
}

// BotWorkAttachment captures immutable admitted source evidence, never a model
// summary. Full original bytes remain in the saved run envelope.
type BotWorkAttachment struct {
	SourceID       string `json:"source_id"`
	Filename       string `json:"filename"`
	MimeType       string `json:"mime_type"`
	IdentityDigest string `json:"identity_digest"`
	SHA256         string `json:"sha256"`
	Size           int    `json:"size"`
	Text           string `json:"text,omitempty"`
	Blocker        string `json:"blocker,omitempty"`
}

// PinBotWorkAttachments only supports complete UTF-8 textual sources. Unknown
// binary/multimodal sources and oversized text explicitly prevent assessment.
func PinBotWorkAttachments(files []ChatFile) []BotWorkAttachment {
	var out []BotWorkAttachment
	remaining := BotWorkMaxContractBytes / 2
	manifest := sha256.New()
	for i, f := range files {
		d := sha256.Sum256(f.Data)
		a := BotWorkAttachment{Filename: f.Filename, MimeType: f.MimeType, Size: len(f.Data), SHA256: hex.EncodeToString(d[:])}
		a.IdentityDigest = botWorkDigest(struct{ Filename, MimeType, SHA256 string }{f.Filename, f.MimeType, a.SHA256})
		_, _ = manifest.Write([]byte(a.IdentityDigest))
		if len(files) > 32 {
			continue
		}
		a.SourceID = fmt.Sprintf("attachment:%d:%s", i, a.SHA256)
		if len(a.Filename) > 256 {
			a.Filename = a.Filename[:256]
		}
		if len(a.MimeType) > 128 {
			a.MimeType = a.MimeType[:128]
		}
		media, params, err := mime.ParseMediaType(f.MimeType)
		textual := strings.HasPrefix(media, "text/") || media == "application/json" || media == "application/xml"
		charset := strings.ToLower(params["charset"])
		switch {
		case err != nil || !textual || (charset != "" && charset != "utf-8" && charset != "us-ascii") || !utf8.Valid(f.Data) || bytes.IndexByte(f.Data, 0) >= 0:
			a.Blocker = "Attachment content is unsupported for complete textual assessment."
		case len(f.Data) == 0:
			a.Blocker = "Attachment text is missing; requirements cannot be assessed."
		default:
			if len(f.Data) <= remaining {
				text, _ := json.Marshal(string(f.Data))
				if len(text) <= remaining {
					a.Text = string(f.Data)
					remaining -= len(text)
				}
			}
			if a.Text == "" {
				a.Blocker = "Complete attachment text exceeds the assessment limit; original bytes remain saved."
			}
		}
		out = append(out, a)
	}
	encoded, _ := json.Marshal(out)
	if len(files) > 32 || len(encoded) > BotWorkMaxContractBytes {
		digest := hex.EncodeToString(manifest.Sum(nil))
		return []BotWorkAttachment{{SourceID: "attachment:manifest:" + digest, IdentityDigest: digest, SHA256: digest, Size: len(files), Blocker: "Attachment count or descriptor size exceeds the assessment limit; full attachment identities are pinned by the manifest digest and originals remain saved."}}
	}
	return out
}

// BotWorkCriterionScope identifies what fulfillment means for this assignment,
// not whether a requirement may be ignored or an action is authorized.
type BotWorkCriterionScope string

const (
	BotWorkCriterionCurrentTurn          BotWorkCriterionScope = "current_turn"
	BotWorkCriterionFutureTaskDefinition BotWorkCriterionScope = "future_task_definition"
)

type BotWorkVerificationKind string

const (
	BotWorkVerificationModel         BotWorkVerificationKind = "model"
	BotWorkVerificationSourceURL     BotWorkVerificationKind = "source_url"
	BotWorkVerificationRetrievalTime BotWorkVerificationKind = "retrieval_time"
)

type BotWorkCriterion struct {
	VerificationKind BotWorkVerificationKind `json:"verification_kind,omitempty"`
	FulfillmentScope BotWorkCriterionScope   `json:"fulfillment_scope,omitempty"`
	ID               string                  `json:"id"`
	SourceIDs        []string                `json:"source_ids"`
	Text             string                  `json:"text"`
	Excluded         bool                    `json:"excluded,omitempty"`
	ExclusionReason  string                  `json:"exclusion_reason,omitempty"`
}

// EffectiveVerificationKind preserves model assessment for legacy records.
func (c BotWorkCriterion) EffectiveVerificationKind() BotWorkVerificationKind {
	if c.VerificationKind == "" {
		return BotWorkVerificationModel
	}
	return c.VerificationKind
}

// EffectiveFulfillmentScope keeps persisted legacy criteria current-turn by default.
func (c BotWorkCriterion) EffectiveFulfillmentScope() BotWorkCriterionScope {
	if c.FulfillmentScope == "" {
		return BotWorkCriterionCurrentTurn
	}
	return c.FulfillmentScope
}

type BotCriterionAssessment struct {
	CriterionID  string              `json:"criterion_id"`
	Verdict      BotCriterionVerdict `json:"verdict"`
	Method       BotAssessmentMethod `json:"method"`
	EvidenceRefs []string            `json:"evidence_refs"`
	Explanation  string              `json:"explanation"`
}

// Bodies are private captured bytes; filenames alone are not review evidence.
type BotWorkCandidate struct {
	RuntimeBlockers []BotWorkBlocker  `json:"runtime_blockers,omitempty"`
	Revision        int64             `json:"revision"`
	EventID         string            `json:"event_id"`
	Text            string            `json:"text"`
	Artifacts       []BotArtifact     `json:"artifacts,omitempty"`
	Bodies          map[string]string `json:"bodies,omitempty"`
	Observations    []BotEvidence     `json:"observations,omitempty"`
	Digest          string            `json:"digest"`
}

func (c BotWorkCandidate) ContentDigest() string {
	c.Digest = ""
	return botWorkDigest(c)
}

type BotWorkReview struct {
	BlockedResponse *BotBlockedResponseAssessment `json:"blocked_response,omitempty"`
	CandidateDigest string                        `json:"candidate_digest"`
	Criteria        []BotCriterionAssessment      `json:"criteria"`
}

type BotWorkState struct {
	Protocol           BotWorkProtocol        `json:"protocol,omitempty"`
	AgentWork          *BotAgentWork          `json:"agent_work,omitempty"`
	SkillState         *BotSkillRunState      `json:"skill_state,omitempty"`
	ResponseOutcome    BotWorkResponseOutcome `json:"response_outcome,omitempty"`
	SchemaVersion      int                    `json:"schema_version"`
	Revision           int64                  `json:"revision"`
	Phase              BotWorkPhase           `json:"phase"`
	Contract           BotWorkContract        `json:"contract"`
	RequirementsDigest string                 `json:"requirements_digest"`
	Criteria           []BotWorkCriterion     `json:"criteria,omitempty"`
	Candidate          *BotWorkCandidate      `json:"candidate,omitempty"`
	Review             *BotWorkReview         `json:"review,omitempty"`
	Repairs            int                    `json:"repairs"`
	Unresolved         []string               `json:"unresolved,omitempty"`
}

// Agent work describes judgment and progress, not a permission grant or a proof
// that the whole assignment is factually correct. Observations remain separate.
const (
	BotAgentWorkSchemaVersion    = 1
	BotAgentWorkMaxItems         = 25
	BotAgentWorkMaxObligations   = 25
	BotAgentWorkMaxNotesBytes    = 8 << 10
	BotAgentWorkMaxChecks        = 100
	BotAgentWorkMaxMetadataBytes = 64 << 10
)

type AgentWorkDisposition string

const (
	AgentWorkAnswered AgentWorkDisposition = "answered"
	AgentWorkComplete AgentWorkDisposition = "complete"
	AgentWorkPartial  AgentWorkDisposition = "partial"
	AgentWorkBlocked  AgentWorkDisposition = "blocked"
)

type BotWorkItemStatus string

const (
	BotWorkItemPending    BotWorkItemStatus = "pending"
	BotWorkItemInProgress BotWorkItemStatus = "in_progress"
	BotWorkItemCompleted  BotWorkItemStatus = "completed"
	BotWorkItemBlocked    BotWorkItemStatus = "blocked"
	BotWorkItemCancelled  BotWorkItemStatus = "cancelled"
)

type BotWorkContinuationKind string

const (
	BotWorkContinueOwnerRequest     BotWorkContinuationKind = "owner_request"
	BotWorkContinueExistingSchedule BotWorkContinuationKind = "existing_schedule"
	BotWorkContinueExistingApproval BotWorkContinuationKind = "existing_approval"
)

type BotWorkCheckStatus string
type BotWorkCheckKind string
type BotWorkCheckJudgment string

const (
	BotWorkCheckReturned              BotWorkCheckStatus   = "returned"
	BotWorkCheckFailed                BotWorkCheckStatus   = "failed"
	BotWorkCheckInterrupted           BotWorkCheckStatus   = "interrupted"
	BotWorkCheckModelFeedback         BotWorkCheckKind     = "model_feedback"
	BotWorkCheckMechanicalObservation BotWorkCheckKind     = "mechanical_observation"
	BotWorkCheckSupported             BotWorkCheckJudgment = "supported"
	BotWorkCheckNeedsWork             BotWorkCheckJudgment = "needs_work"
	BotWorkCheckUnverified            BotWorkCheckJudgment = "unverified"
)

type BotWorkItem struct {
	ID           RunID             `json:"id"`
	Summary      string            `json:"summary"`
	Status       BotWorkItemStatus `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	EvidenceRefs []string          `json:"evidence_refs,omitempty"`
}

type BotWorkObligation struct {
	ID           RunID                   `json:"id"`
	SourceRunID  RunID                   `json:"source_run_id"`
	WorkItemID   *RunID                  `json:"work_item_id,omitempty"`
	Summary      string                  `json:"summary"`
	Reason       string                  `json:"reason"`
	Continuation BotWorkContinuationKind `json:"continuation"`
	// References identify existing records only. They do not authorize execution.
	ContinuationID *RunID `json:"continuation_id,omitempty"`
}

type BotWorkCheckReceipt struct {
	ID           RunID                `json:"id"`
	CallID       string               `json:"call_id"`
	Kind         BotWorkCheckKind     `json:"kind"`
	Status       BotWorkCheckStatus   `json:"status"`
	Judgment     BotWorkCheckJudgment `json:"judgment"`
	Summary      string               `json:"summary"`
	DraftDigest  string               `json:"draft_digest"`
	EvidenceRefs []string             `json:"evidence_refs,omitempty"`
	ObservedAt   time.Time            `json:"observed_at"`
}

// BotWorkEvidenceReference addresses captured run-local evidence. The scoped
// service must resolve the event/call and digest; shape validation is not trust.
type BotWorkEvidenceReference struct {
	Ref     string `json:"ref"`
	RunID   RunID  `json:"run_id"`
	EventID string `json:"event_id"`
	CallID  string `json:"call_id"`
	Digest  string `json:"digest"`
}

type BotSourceAccessStatus string

const (
	BotSourceAccessReturned BotSourceAccessStatus = "returned"
	BotSourceAccessFailed   BotSourceAccessStatus = "failed"
)

// HTTP status and returned bytes do not establish relevance or source truth.
type BotSourceAccessFact struct {
	EvidenceRef string                `json:"evidence_ref"`
	CallID      string                `json:"call_id"`
	OriginalURL string                `json:"original_url"`
	FinalURL    string                `json:"final_url,omitempty"`
	Status      BotSourceAccessStatus `json:"status"`
	HTTPStatus  int                   `json:"http_status,omitempty"`
	ObservedAt  time.Time             `json:"observed_at"`
	Truncated   bool                  `json:"truncated,omitempty"`
	BodyDigest  string                `json:"body_digest"`
}

// BotWorkCallReceipt binds optional bookkeeping retries to the original request.
// A reserved check means execution may have started; it must never be replayed.
type BotWorkCallReceipt struct {
	CallID        string `json:"call_id"`
	RequestDigest string `json:"request_digest"`
	Revision      int64  `json:"revision"`
	Check         bool   `json:"check,omitempty"`
}

type BotAgentWork struct {
	Calls         []BotWorkCallReceipt       `json:"calls,omitempty"`
	SchemaVersion int                        `json:"schema_version"`
	Notes         string                     `json:"notes,omitempty"`
	Items         []BotWorkItem              `json:"items,omitempty"`
	Remaining     []BotWorkObligation        `json:"remaining,omitempty"`
	Checks        []BotWorkCheckReceipt      `json:"checks,omitempty"`
	Evidence      []BotWorkEvidenceReference `json:"evidence,omitempty"`
	SourceAccess  []BotSourceAccessFact      `json:"source_access,omitempty"`
	Disposition   AgentWorkDisposition       `json:"disposition,omitempty"`
}

// BotWorkProjection excludes raw evidence and private notes. Transport callers
// must additionally redact model-authored summaries using the scoped redactor.
type BotWorkProjection struct {
	SchemaVersion   int                   `json:"schema_version"`
	Protocol        BotWorkProtocol       `json:"protocol"`
	BotID           BotID                 `json:"bot_id"`
	RunID           RunID                 `json:"run_id"`
	WorkRevision    int64                 `json:"work_revision"`
	Phase           BotWorkPhase          `json:"phase"`
	Disposition     AgentWorkDisposition  `json:"disposition,omitempty"`
	CandidateDigest string                `json:"candidate_digest,omitempty"`
	Items           []BotWorkItem         `json:"items,omitempty"`
	Remaining       []BotWorkObligation   `json:"remaining,omitempty"`
	Checks          []BotWorkCheckReceipt `json:"checks,omitempty"`
}

func NewBotAgentWorkState(contract BotWorkContract) *BotWorkState {
	w := NewBotWorkState(contract)
	w.Protocol = BotWorkProtocolAgentLedV1
	w.AgentWork = &BotAgentWork{SchemaVersion: BotAgentWorkSchemaVersion}
	if w.Phase == BotWorkPreparing {
		w.Phase = BotWorkWorking
	}
	return w
}

func BuildBotWorkProjection(w *BotWorkState) *BotWorkProjection {
	if w == nil || w.Protocol != BotWorkProtocolAgentLedV1 || w.Validate() != nil {
		return nil
	}
	copy := w.Clone()
	p := &BotWorkProjection{SchemaVersion: BotAgentWorkSchemaVersion, Protocol: copy.Protocol,
		BotID: copy.Contract.BotID, RunID: copy.Contract.RunID, WorkRevision: copy.Revision,
		Phase: copy.Phase, Disposition: copy.AgentWork.Disposition, Items: copy.AgentWork.Items,
		Remaining: copy.AgentWork.Remaining, Checks: copy.AgentWork.Checks}
	if copy.Candidate != nil {
		p.CandidateDigest = copy.Candidate.Digest
	}
	return p
}

func (p *BotWorkProjection) Validate() error {
	if p == nil || p.SchemaVersion != BotAgentWorkSchemaVersion || p.Protocol != BotWorkProtocolAgentLedV1 || p.BotID == (BotID{}) || p.RunID == (RunID{}) || p.WorkRevision < 1 {
		return ErrBotInvalid
	}
	if p.CandidateDigest != "" && !skillDigestValid(p.CandidateDigest) {
		return ErrBotInvalid
	}
	if !validAgentWorkPhase(p.Phase) || p.Phase == BotWorkFinished && (p.CandidateDigest == "" || p.Disposition == "" || p.Disposition == AgentWorkBlocked) {
		return ErrBotInvalid
	}
	a := BotAgentWork{SchemaVersion: p.SchemaVersion, Items: p.Items, Remaining: p.Remaining, Checks: p.Checks, Disposition: p.Disposition}
	return a.validate(p.RunID, nil, false)
}

func (p *BotWorkProjection) allowsResult(status RunStatus) bool {
	if p == nil {
		return false
	}
	if status == RunSucceeded {
		return p.Phase == BotWorkFinished && (p.Disposition == AgentWorkAnswered || p.Disposition == AgentWorkComplete)
	}
	return status == RunPartial && ((p.Phase == BotWorkFinished && p.Disposition == AgentWorkPartial) || (p.Phase == BotWorkBlocked && p.Disposition == AgentWorkBlocked))
}

func validAgentWorkPhase(phase BotWorkPhase) bool {
	return phase == BotWorkWorking || phase == BotWorkFinished || phase == BotWorkBlocked || phase == BotWorkInterrupted
}

func (a *BotAgentWork) validate(runID RunID, refs map[string]bool, bindRefs bool) error {
	if a == nil || a.SchemaVersion != BotAgentWorkSchemaVersion || len(a.Notes) > BotAgentWorkMaxNotesBytes || len(a.Items) > BotAgentWorkMaxItems || len(a.Remaining) > BotAgentWorkMaxObligations || len(a.Checks) > BotAgentWorkMaxChecks {
		return ErrBotInvalid
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > BotAgentWorkMaxMetadataBytes {
		return ErrBotInvalid
	}
	switch a.Disposition {
	case "", AgentWorkAnswered, AgentWorkComplete, AgentWorkPartial, AgentWorkBlocked:
	default:
		return ErrBotInvalid
	}
	validRefs := func(values []string) bool {
		seen := map[string]bool{}
		for _, ref := range values {
			if strings.TrimSpace(ref) == "" || seen[ref] || bindRefs && !refs[ref] {
				return false
			}
			seen[ref] = true
		}
		return true
	}
	items := map[RunID]bool{}
	for _, item := range a.Items {
		if item.ID == (RunID{}) || items[item.ID] || strings.TrimSpace(item.Summary) == "" || !validRefs(item.EvidenceRefs) {
			return ErrBotInvalid
		}
		items[item.ID] = true
		switch item.Status {
		case BotWorkItemPending, BotWorkItemInProgress, BotWorkItemCompleted, BotWorkItemCancelled:
		case BotWorkItemBlocked:
			if strings.TrimSpace(item.Reason) == "" {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
		if a.Disposition == AgentWorkComplete && item.Status != BotWorkItemCompleted && item.Status != BotWorkItemCancelled {
			return ErrBotInvalid
		}
	}
	obligations := map[RunID]bool{}
	for _, remaining := range a.Remaining {
		if remaining.ID == (RunID{}) || obligations[remaining.ID] || remaining.SourceRunID != runID || strings.TrimSpace(remaining.Summary) == "" || strings.TrimSpace(remaining.Reason) == "" {
			return ErrBotInvalid
		}
		obligations[remaining.ID] = true
		if remaining.WorkItemID != nil && !items[*remaining.WorkItemID] {
			return ErrBotInvalid
		}
		switch remaining.Continuation {
		case BotWorkContinueOwnerRequest:
			if remaining.ContinuationID != nil {
				return ErrBotInvalid
			}
		case BotWorkContinueExistingSchedule, BotWorkContinueExistingApproval:
			if remaining.ContinuationID == nil || *remaining.ContinuationID == (RunID{}) {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
	}
	if a.Disposition == AgentWorkComplete && len(a.Remaining) != 0 {
		return ErrBotInvalid
	}
	checks := map[RunID]bool{}
	for _, check := range a.Checks {
		if check.ID == (RunID{}) || checks[check.ID] || strings.TrimSpace(check.CallID) == "" || strings.TrimSpace(check.Summary) == "" || check.ObservedAt.IsZero() || !skillDigestValid(check.DraftDigest) || !validRefs(check.EvidenceRefs) {
			return ErrBotInvalid
		}
		checks[check.ID] = true
		if check.Kind != BotWorkCheckModelFeedback && check.Kind != BotWorkCheckMechanicalObservation {
			return ErrBotInvalid
		}
		switch check.Judgment {
		case BotWorkCheckSupported, BotWorkCheckNeedsWork, BotWorkCheckUnverified:
		default:
			return ErrBotInvalid
		}
		switch check.Status {
		case BotWorkCheckReturned:
		case BotWorkCheckFailed, BotWorkCheckInterrupted:
			if check.Judgment != BotWorkCheckUnverified {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
		if check.Kind == BotWorkCheckMechanicalObservation && check.Status == BotWorkCheckReturned && len(check.EvidenceRefs) == 0 {
			return ErrBotInvalid
		}
	}
	return nil
}

// BotQualityReport is a safe final projection, not a replacement for trusted
// Changed/Unchanged observations or an authority to execute an action.
type BotQualityReport struct {
	ResponseOutcome    BotWorkResponseOutcome   `json:"response_outcome,omitempty"`
	BlockedResponse    *BotWorkBlockedResponse  `json:"blocked_response,omitempty"`
	SchemaVersion      int                      `json:"schema_version"`
	WorkRevision       int64                    `json:"work_revision"`
	RequirementsDigest string                   `json:"requirements_digest"`
	CandidateDigest    string                   `json:"candidate_digest,omitempty"`
	Outcome            BotWorkPhase             `json:"outcome"`
	Criteria           []BotCriterionAssessment `json:"criteria,omitempty"`
	Repairs            int                      `json:"repairs"`
	Limitations        []string                 `json:"limitations,omitempty"`
}

type BotWorkUpdate struct {
	Lease            BotLease
	ExpectedRevision int64
	State            BotWorkState
}

// BotWorkStore is worker-only. Implementations require the current scope,
// identity, live lease and CAS revision; no HTTP/model-facing write is implied.
type BotWorkStore interface {
	LoadWork(context.Context, BotLease) (*BotWorkState, error)
	SaveWork(context.Context, BotWorkUpdate) (*BotWorkState, error)
}

func botWorkDigest(v any) string {
	b, _ := json.Marshal(v) // these concrete records contain no fallible JSON values
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func (w *BotWorkState) Clone() *BotWorkState {
	if w == nil {
		return nil
	}
	raw, _ := json.Marshal(w)
	var out BotWorkState
	_ = json.Unmarshal(raw, &out)
	return &out
}

func NewBotWorkState(contract BotWorkContract) *BotWorkState {
	w := (&BotWorkState{SchemaVersion: BotWorkSchemaVersion, Revision: 1, Phase: BotWorkPreparing, Contract: contract, RequirementsDigest: botWorkDigest(contract)}).Clone()
	// Skill snapshots have their own size limit; original assignment limits stay unchanged.
	baseContract := contract
	baseContract.Skill = nil
	raw, _ := json.Marshal(baseContract)
	if len(raw) > BotWorkMaxContractBytes {
		w.Phase = BotWorkBlocked
		w.Unresolved = []string{"The complete requirement sources exceed the assessment limit; no requirements were discarded."}
	}
	for _, a := range w.Contract.Attachments {
		if a.Blocker != "" || a.Text == "" {
			w.Phase = BotWorkBlocked
			w.Unresolved = append(w.Unresolved, a.SourceID+": attachment requirements cannot be inspected. "+a.Blocker)
		}
	}
	return w
}

func (w *BotWorkState) Validate() error {
	if w != nil {
		switch w.Protocol {
		case BotWorkProtocolLegacyAssessment:
			if w.AgentWork != nil {
				return ErrBotInvalid
			}
		case BotWorkProtocolAgentLedV1:
			if w.AgentWork == nil || !validAgentWorkPhase(w.Phase) || len(w.Criteria) != 0 || w.Review != nil || w.Repairs != 0 || w.ResponseOutcome != "" {
				return ErrBotInvalid
			}
		default:
			return ErrBotUnsupported
		}
	}
	if w == nil || w.SchemaVersion != BotWorkSchemaVersion || w.Revision < 1 || w.Contract.BotID == (BotID{}) || w.Contract.RunID == (RunID{}) || w.Contract.Version < 1 || strings.TrimSpace(w.Contract.Instructions) == "" || w.RequirementsDigest != botWorkDigest(w.Contract) || w.Repairs < 0 || w.Repairs > BotWorkMaxRepairs || len(w.Criteria) > BotWorkMaxCriteria {
		return ErrBotInvalid
	}
	// Bound all assessor-controlled metadata in every phase, including preparing
	// and terminal states where semantic criterion validation may be skipped.
	for _, value := range []any{w.Criteria, w.Review, w.Unresolved, w.Contract.Attachments} {
		encoded, _ := json.Marshal(value)
		if len(encoded) > BotWorkMaxContractBytes {
			return ErrBotInvalid
		}
	}
	attachmentIDs := map[string]bool{}
	if len(w.Contract.Attachments) > 32 {
		return ErrBotInvalid
	}
	for _, a := range w.Contract.Attachments {
		if a.SourceID == "" || attachmentIDs[a.SourceID] || !strings.HasPrefix(a.SourceID, "attachment:") || len(a.SHA256) != 64 || len(a.IdentityDigest) != 64 || a.Size < 0 {
			return ErrBotInvalid
		}
		attachmentIDs[a.SourceID] = true
		if a.Blocker != "" || a.Text == "" {
			if w.Phase != BotWorkBlocked && w.Phase != BotWorkInterrupted {
				return ErrBotInvalid
			}
		} else {
			d := sha256.Sum256([]byte(a.Text))
			if len(a.Text) != a.Size || hex.EncodeToString(d[:]) != a.SHA256 {
				return ErrBotInvalid
			}
		}
	}
	if w.ResponseOutcome != "" && (w.ResponseOutcome != BotWorkResponseVerifiedBlocker || w.Phase != BotWorkBlocked || !w.HasVerifiedBlockedResponse()) {
		return ErrBotInvalid
	}
	switch w.Phase {
	case BotWorkWorking, BotWorkFinished:
		if w.Protocol != BotWorkProtocolAgentLedV1 {
			return ErrBotInvalid
		}
	case BotWorkPreparing, BotWorkProducing, BotWorkCandidateReady, BotWorkReviewing, BotWorkRepairReady, BotWorkRepairing, BotWorkAccepted, BotWorkBlocked, BotWorkInterrupted:
	default:
		return ErrBotInvalid
	}
	for _, g := range w.Contract.Goals {
		if g.BotID != w.Contract.BotID {
			return ErrBotForbidden
		}
	}
	if w.Contract.Skill != nil {
		if err := w.Contract.Skill.Validate(w.Contract.BotID); err != nil {
			return err
		}
		if w.SkillState == nil {
			return ErrBotInvalid
		}
	}
	if w.SkillState != nil {
		if err := w.SkillState.Validate(w.Contract); err != nil {
			return err
		}
	}
	baseContract := w.Contract
	baseContract.Skill = nil
	raw, _ := json.Marshal(baseContract)
	if len(raw) > BotWorkMaxContractBytes && w.Phase != BotWorkBlocked && w.Phase != BotWorkInterrupted {
		return ErrBotInvalid
	}
	if (w.Phase == BotWorkBlocked || w.Phase == BotWorkInterrupted) && len(w.Unresolved) == 0 {
		return ErrBotInvalid
	}
	if w.Protocol == BotWorkProtocolLegacyAssessment && w.Phase != BotWorkPreparing && w.Phase != BotWorkBlocked && w.Phase != BotWorkInterrupted {
		if err := w.validateCriteria(); err != nil {
			return err
		}
	}
	if w.Candidate != nil {
		c := w.Candidate
		if c.Revision < 1 || (w.Protocol == BotWorkProtocolLegacyAssessment && c.Revision > int64(BotWorkMaxRepairs+1)) || c.EventID == "" || strings.TrimSpace(c.Text) == "" || c.Digest != c.ContentDigest() {
			return ErrBotInvalid
		}
		body, _ := json.Marshal(c)
		if len(body) > BotWorkMaxCandidateBytes {
			return ErrBotInvalid
		}
		for _, a := range c.Artifacts {
			body, ok := c.Bodies[a.ID.String()]
			d := sha256.Sum256([]byte(body))
			if !ok || a.ID == (RunID{}) || a.HomeID == (RunID{}) || a.SHA256 != hex.EncodeToString(d[:]) {
				return ErrBotInvalid
			}
		}
	}
	switch w.Phase {
	case BotWorkCandidateReady, BotWorkReviewing, BotWorkRepairReady, BotWorkRepairing, BotWorkAccepted:
		if w.Candidate == nil {
			return ErrBotInvalid
		}
	}
	if w.Protocol == BotWorkProtocolAgentLedV1 {
		if err := w.validateAgentWork(); err != nil {
			return err
		}
	}
	if err := w.validateBlockers(); err != nil {
		return err
	}
	if w.Review != nil {
		if err := w.validateReview(); err != nil {
			return err
		}
	}
	if w.Phase == BotWorkAccepted || w.Phase == BotWorkRepairReady {
		if w.Phase == BotWorkAccepted && w.Review != nil && w.Review.BlockedResponse != nil && w.Review.BlockedResponse.Verdict == BotCriterionSatisfied {
			return ErrBotInvalid
		}
		if w.Review == nil {
			return ErrBotInvalid
		}
		allPass := true
		for _, a := range w.Review.Criteria {
			allPass = allPass && a.Verdict == BotCriterionSatisfied
		}
		if w.Phase == BotWorkAccepted && (!allPass || len(w.Unresolved) != 0) {
			return ErrBotInvalid
		}
		if w.Phase == BotWorkRepairReady && (allPass || w.Repairs >= BotWorkMaxRepairs) {
			return ErrBotInvalid
		}
	}
	return nil
}

func (w *BotWorkState) validateAgentWork() error {
	a := w.AgentWork
	if len(a.Calls) > 200 {
		return ErrBotInvalid
	}
	calls := map[string]bool{}
	for _, call := range a.Calls {
		if call.CallID == "" || len(call.CallID) > 512 || calls[call.CallID] || !skillDigestValid(call.RequestDigest) || call.Revision < 1 || call.Revision > w.Revision+1 {
			return ErrBotInvalid
		}
		calls[call.CallID] = true
	}
	// Agent feedback references only the append-only journal. Legacy positional
	// candidate/observation refs would change meaning when a draft is replaced.
	refs := map[string]bool{}
	for _, e := range a.Evidence {
		if !strings.HasPrefix(e.Ref, "capture:") || len(e.Ref) <= len("capture:") || refs[e.Ref] || e.RunID != w.Contract.RunID || strings.TrimSpace(e.CallID) == "" || strings.TrimSpace(e.EventID) == "" || !skillDigestValid(e.Digest) {
			return ErrBotInvalid
		}
		refs[e.Ref] = true
	}
	for _, access := range a.SourceAccess {
		if !refs[access.EvidenceRef] || strings.TrimSpace(access.CallID) == "" || strings.TrimSpace(access.OriginalURL) == "" || access.ObservedAt.IsZero() || !skillDigestValid(access.BodyDigest) || access.HTTPStatus < 0 || access.HTTPStatus > 599 || access.HTTPStatus > 0 && access.HTTPStatus < 100 {
			return ErrBotInvalid
		}
		if access.Status != BotSourceAccessReturned && access.Status != BotSourceAccessFailed {
			return ErrBotInvalid
		}
		bound := false
		for _, e := range a.Evidence {
			if e.Ref == access.EvidenceRef && e.CallID == access.CallID && e.Digest == access.BodyDigest {
				bound = true
			}
		}
		if !bound {
			return ErrBotInvalid
		}
	}
	if err := a.validate(w.Contract.RunID, refs, true); err != nil {
		return err
	}
	if w.Phase == BotWorkFinished {
		if w.Candidate == nil || a.Disposition == "" || a.Disposition == AgentWorkBlocked {
			return ErrBotInvalid
		}
		for _, item := range a.Items {
			if item.Status == BotWorkItemInProgress {
				return ErrBotInvalid
			}
		}
	}
	return nil
}

func (w *BotWorkState) validateCriteria() error {
	sources := map[string]bool{"assignment": false, "charter": false}
	for _, g := range w.Contract.Goals {
		sources["goal:"+g.ID.String()] = false
	}
	for _, a := range w.Contract.Attachments {
		sources[a.SourceID] = false
	}
	ids := map[string]bool{}
	applicable := 0
	for i, c := range w.Criteria {
		// Only fixed field names and numeric positions enter errors. Model-supplied
		// identifiers, source text and enum values can contain private data.
		invalid := func(field, reason string) error {
			return fmt.Errorf("%w: criteria[%d].%s: %s", ErrBotInvalid, i, field, reason)
		}
		switch c.EffectiveVerificationKind() {
		case BotWorkVerificationModel, BotWorkVerificationSourceURL, BotWorkVerificationRetrievalTime:
		default:
			return invalid("verification_kind", "must be model, source_url, or retrieval_time")
		}
		switch c.EffectiveFulfillmentScope() {
		case BotWorkCriterionCurrentTurn, BotWorkCriterionFutureTaskDefinition:
		default:
			return invalid("fulfillment_scope", "must be current_turn or future_task_definition")
		}
		if c.ID == "" || ids[c.ID] {
			return invalid("id", "must be nonempty and unique")
		}
		if strings.TrimSpace(c.Text) == "" {
			return invalid("text", "must be nonempty")
		}
		if len(c.SourceIDs) == 0 {
			return invalid("source_ids", "must contain at least one pinned source")
		}
		ids[c.ID] = true
		if c.Excluded {
			if strings.TrimSpace(c.ExclusionReason) == "" {
				return invalid("exclusion_reason", "required for an excluded goal")
			}
		} else {
			applicable++
		}
		for j, id := range c.SourceIDs {
			if _, ok := sources[id]; !ok {
				return invalid(fmt.Sprintf("source_ids[%d]", j), "must reference a pinned source")
			}
			if c.Excluded && !strings.HasPrefix(id, "goal:") {
				return invalid(fmt.Sprintf("source_ids[%d]", j), "only goal sources may be excluded")
			}
			sources[id] = true
		}
	}
	if applicable == 0 {
		return fmt.Errorf("%w: criteria: at least one applicable criterion is required", ErrBotInvalid)
	}
	for _, covered := range sources {
		if !covered {
			return fmt.Errorf("%w: criteria: incomplete source coverage; cover assignment, charter, every goal and attachment", ErrBotInvalid)
		}
	}
	return nil
}

// AvailableEvidenceRefs returns only references bound to this captured candidate.
func (c *BotWorkCandidate) AvailableEvidenceRefs() []string {
	if c == nil {
		return nil
	}
	refs := []string{"candidate:" + c.Digest}
	for _, a := range c.Artifacts {
		refs = append(refs, "artifact:"+a.ID.String()+":"+a.SHA256)
	}
	for i := range c.Observations {
		refs = append(refs, fmt.Sprintf("observation:%d", i))
	}
	return refs
}

func (w *BotWorkState) validateReview() error {
	invalid := func(field, reason string) error { return fmt.Errorf("%w: review.%s: %s", ErrBotInvalid, field, reason) }
	if w.Candidate == nil || w.Review.CandidateDigest != w.Candidate.Digest {
		return invalid("candidate_digest", "must match the current candidate")
	}
	criteria := map[string]bool{}
	for _, c := range w.Criteria {
		if !c.Excluded {
			criteria[c.ID] = false
		}
	}
	refs := map[string]bool{}
	for _, ref := range w.Candidate.AvailableEvidenceRefs() {
		refs[ref] = true
	}
	for i, a := range w.Review.Criteria {
		field := func(name string) string { return fmt.Sprintf("criteria[%d].%s", i, name) }
		seen, ok := criteria[a.CriterionID]
		if !ok {
			return invalid(field("criterion_id"), "must reference an applicable criterion")
		}
		if seen {
			return invalid(field("criterion_id"), "must be unique")
		}
		if strings.TrimSpace(a.Explanation) == "" {
			return invalid(field("explanation"), "must be nonempty")
		}
		criteria[a.CriterionID] = true
		switch a.Verdict {
		case BotCriterionSatisfied, BotCriterionFailed, BotCriterionUnverified:
		default:
			return invalid(field("verdict"), "must be satisfied, failed, or unverified")
		}
		switch a.Method {
		case BotAssessmentMechanical, BotAssessmentModel:
		default:
			return invalid(field("method"), "must be mechanical_observation or model_assessment")
		}
		if a.Verdict == BotCriterionSatisfied && len(a.EvidenceRefs) == 0 {
			return invalid(field("evidence_refs"), "required for a satisfied verdict")
		}
		for j, ref := range a.EvidenceRefs {
			if !refs[ref] {
				return invalid(field(fmt.Sprintf("evidence_refs[%d]", j)), "must reference current candidate evidence")
			}
		}
	}
	for _, seen := range criteria {
		if !seen {
			return invalid("criteria", "must cover every applicable criterion exactly once")
		}
	}
	return nil
}

// ValidateBotWorkUpdate cannot prove semantic correctness. It enforces immutable
// sources/candidates, complete criterion coverage and a bounded legal transition.
func ValidateBotWorkUpdate(old, next *BotWorkState) error {
	if err := old.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	// Only the dedicated skill repository can orient or stage/commit learning.
	// Generic work updates cannot mint a learned receipt or repin a method.
	if !reflect.DeepEqual(old.SkillState, next.SkillState) {
		return ErrBotInvalid
	}
	if !reflect.DeepEqual(old.Contract, next.Contract) || old.RequirementsDigest != next.RequirementsDigest || next.Revision != old.Revision {
		return ErrBotInvalid
	}
	if old.Protocol != next.Protocol {
		return ErrBotInvalid
	}
	if old.Protocol == BotWorkProtocolAgentLedV1 {
		if old.Phase != BotWorkWorking {
			return ErrBotConflict
		}
		if len(next.AgentWork.Calls) < len(old.AgentWork.Calls) || len(old.AgentWork.Calls) > 0 && !reflect.DeepEqual(old.AgentWork.Calls, next.AgentWork.Calls[:len(old.AgentWork.Calls)]) {
			return ErrBotInvalid
		}
		// Journal facts and check attempts are append-only; an edited claim cannot
		// rewrite the observation or the previous check that motivated its repair.
		if len(next.AgentWork.Evidence) < len(old.AgentWork.Evidence) || len(next.AgentWork.SourceAccess) < len(old.AgentWork.SourceAccess) || len(next.AgentWork.Checks) < len(old.AgentWork.Checks) {
			return ErrBotInvalid
		}
		if !reflect.DeepEqual(old.AgentWork.Evidence, next.AgentWork.Evidence[:len(old.AgentWork.Evidence)]) && len(old.AgentWork.Evidence) != 0 {
			return ErrBotInvalid
		}
		if botWorkDigest(old.AgentWork.SourceAccess) != botWorkDigest(next.AgentWork.SourceAccess[:len(old.AgentWork.SourceAccess)]) && len(old.AgentWork.SourceAccess) != 0 {
			return ErrBotInvalid
		}
		if botWorkDigest(old.AgentWork.Checks) != botWorkDigest(next.AgentWork.Checks[:len(old.AgentWork.Checks)]) && len(old.AgentWork.Checks) != 0 {
			return ErrBotInvalid
		}
		if !reflect.DeepEqual(old.Candidate, next.Candidate) {
			expected := int64(1)
			if old.Candidate != nil {
				expected = old.Candidate.Revision + 1
			}
			if next.Candidate == nil || next.Candidate.Revision != expected {
				return ErrBotInvalid
			}
		}
		return nil
	}
	if old.ResponseOutcome != next.ResponseOutcome && (old.Phase != BotWorkReviewing || next.Phase != BotWorkBlocked || next.ResponseOutcome != BotWorkResponseVerifiedBlocker) {
		return ErrBotInvalid
	}
	if old.Phase != BotWorkPreparing && !reflect.DeepEqual(old.Criteria, next.Criteria) {
		return ErrBotInvalid
	}
	if next.Phase == BotWorkInterrupted || next.Phase == BotWorkBlocked {
		if old.Phase == BotWorkAccepted || old.Phase == BotWorkBlocked || old.Phase == BotWorkInterrupted {
			return ErrBotConflict
		}
	} else {
		allowed := false
		switch old.Phase {
		case BotWorkPreparing:
			allowed = next.Phase == BotWorkProducing
		case BotWorkProducing, BotWorkRepairing:
			allowed = next.Phase == BotWorkCandidateReady
		case BotWorkCandidateReady:
			allowed = next.Phase == BotWorkReviewing
		case BotWorkReviewing:
			allowed = next.Phase == BotWorkAccepted || next.Phase == BotWorkRepairReady
		case BotWorkRepairReady:
			allowed = next.Phase == BotWorkRepairing
		}
		if !allowed {
			return ErrBotInvalid
		}
	}
	expectedRepairs := old.Repairs
	if next.Phase == BotWorkRepairing {
		expectedRepairs++
	}
	if next.Repairs != expectedRepairs {
		return ErrBotInvalid
	}
	if next.Phase == BotWorkCandidateReady {
		if next.Candidate.Revision != int64(next.Repairs+1) || next.Review != nil {
			return ErrBotInvalid
		}
	} else if !reflect.DeepEqual(old.Candidate, next.Candidate) {
		return ErrBotInvalid
	}
	if old.Phase != BotWorkReviewing && next.Phase != BotWorkCandidateReady && !reflect.DeepEqual(old.Review, next.Review) {
		return ErrBotInvalid
	}
	return nil
}
