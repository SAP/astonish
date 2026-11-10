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

type BotWorkPhase string

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
	Revision     int64             `json:"revision"`
	EventID      string            `json:"event_id"`
	Text         string            `json:"text"`
	Artifacts    []BotArtifact     `json:"artifacts,omitempty"`
	Bodies       map[string]string `json:"bodies,omitempty"`
	Observations []BotEvidence     `json:"observations,omitempty"`
	Digest       string            `json:"digest"`
}

func (c BotWorkCandidate) ContentDigest() string {
	c.Digest = ""
	return botWorkDigest(c)
}

type BotWorkReview struct {
	CandidateDigest string                   `json:"candidate_digest"`
	Criteria        []BotCriterionAssessment `json:"criteria"`
}

type BotWorkState struct {
	SchemaVersion      int                `json:"schema_version"`
	Revision           int64              `json:"revision"`
	Phase              BotWorkPhase       `json:"phase"`
	Contract           BotWorkContract    `json:"contract"`
	RequirementsDigest string             `json:"requirements_digest"`
	Criteria           []BotWorkCriterion `json:"criteria,omitempty"`
	Candidate          *BotWorkCandidate  `json:"candidate,omitempty"`
	Review             *BotWorkReview     `json:"review,omitempty"`
	Repairs            int                `json:"repairs"`
	Unresolved         []string           `json:"unresolved,omitempty"`
}

// BotQualityReport is a safe final projection, not a replacement for trusted
// Changed/Unchanged observations or an authority to execute an action.
type BotQualityReport struct {
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
	raw, _ := json.Marshal(contract)
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
	switch w.Phase {
	case BotWorkPreparing, BotWorkProducing, BotWorkCandidateReady, BotWorkReviewing, BotWorkRepairReady, BotWorkRepairing, BotWorkAccepted, BotWorkBlocked, BotWorkInterrupted:
	default:
		return ErrBotInvalid
	}
	for _, g := range w.Contract.Goals {
		if g.BotID != w.Contract.BotID {
			return ErrBotForbidden
		}
	}
	raw, _ := json.Marshal(w.Contract)
	if len(raw) > BotWorkMaxContractBytes && w.Phase != BotWorkBlocked && w.Phase != BotWorkInterrupted {
		return ErrBotInvalid
	}
	if (w.Phase == BotWorkBlocked || w.Phase == BotWorkInterrupted) && len(w.Unresolved) == 0 {
		return ErrBotInvalid
	}
	if w.Phase != BotWorkPreparing && w.Phase != BotWorkBlocked && w.Phase != BotWorkInterrupted {
		if err := w.validateCriteria(); err != nil {
			return err
		}
	}
	if w.Candidate != nil {
		c := w.Candidate
		if c.Revision < 1 || c.Revision > int64(BotWorkMaxRepairs+1) || c.EventID == "" || strings.TrimSpace(c.Text) == "" || c.Digest != c.ContentDigest() {
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
	if w.Review != nil {
		if err := w.validateReview(); err != nil {
			return err
		}
	}
	if w.Phase == BotWorkAccepted || w.Phase == BotWorkRepairReady {
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
	if !reflect.DeepEqual(old.Contract, next.Contract) || old.RequirementsDigest != next.RequirementsDigest || next.Revision != old.Revision {
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
