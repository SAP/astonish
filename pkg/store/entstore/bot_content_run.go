package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botRunRecord separates SQL authority from authenticated runtime bytes stored
// in SQL. LogicalDigest binds nested work, reports, tasks and replay receipts:
// content cannot mint execution authority. Hydrated fields do not become control columns.
type botRunRecord struct {
	botRuntimeInline
	LogicalDigest string        `json:"logical_digest"`
	Control       botRunControl `json:"control"`
}

type botRunControl struct {
	PreparedPlanHashes         map[string]string `json:",omitempty"`
	ID                         store.RunID
	BotID                      store.BotID
	Version                    int64
	SessionID                  uuid.UUID
	StateSequence              int64
	StateCommittedAt           time.Time
	SkillID                    *uuid.UUID
	Task                       *store.BotTaskRef
	MandateRef                 *store.BotMandateRef
	CallerCeiling              *execution.BotCallerCeiling
	RuntimeAccountingRecorded  bool
	ExecutionMode              execution.BotExecutionMode
	ContinuationReason         store.BotContinuationReason
	Presentation               store.BotRunPresentation
	UserEventID                string
	HistoryCursor              int64
	Intent                     store.BotExecutionIntent
	Trigger                    store.TriggerKind
	TriggerID                  uuid.UUID
	IdempotencyKey             string
	Status                     store.RunStatus
	Lease                      store.BotLease
	CreatedAt                  time.Time
	StartedAt                  *time.Time
	FinishedAt                 *time.Time
	Usage                      store.BotUsage
	Provider                   string
	Model                      string
	RequestHash                string
	CommitHash                 string
	OccurrenceAt               time.Time
	ExternalAdmissionValidated bool
	SkillAdmissionOwner        *uuid.UUID
	ContinuationRequests       map[string]string
	SkillRequestHashes         map[string]string
	ProgressOwnerContinuation  *botProgressOwnerContinuation
	CoalescedOccurrences       int64
	ProgressCursor             int64
	ProgressPending            int64
	ProgressUncertain          bool
	ProgressAccountedAt        time.Time
	ProgressQuota              *mandateRunUsage
	Progress                   *botRunProgressControl
	Title                      *botRunTitleControl
	Work                       *botRunWorkControl
}

// Work contents remain disk-owned, but state-machine and review gates are SQL
// coordination. Nested evidence/replay objects are bound by LogicalDigest.
type botRunWorkControl struct {
	SchemaVersion               int
	Revision                    int64
	Protocol                    store.BotWorkProtocol
	Phase                       store.BotWorkPhase
	RequirementsDigest          string
	EmployeeReviewPolicy        store.BotEmployeeReviewPolicy
	EmployeeReviewAttempts      int
	EmployeeReviewPending       bool
	EmployeeRepairPending       bool
	ResponseOutcome             store.BotWorkResponseOutcome
	Repairs                     int
	SkillAuthorityConstraintRef string
}

// No evaluation summary or private context bytes are included here.
type botRunProgressControl struct {
	SchemaVersion int
	Phase         store.BotProgressPhase
	Reason        store.BotProgressReason
	Checkpoint    *botRunCheckpointControl
}
type botRunCheckpointControl struct {
	Kind                 store.BotProgressCheckpointKind
	SchemaVersion        int
	Binding              store.BotProgressBinding
	Revision             int64
	TurnID               uuid.UUID
	EventCursor          int64
	TranscriptHighWater  int64
	ContextDigest        string
	NextEligibleAt       time.Time
	Usage                store.BotUsage
	DispatchLedgerCursor int64
	Evaluation           *botRunEvaluationControl
}
type botRunEvaluationControl struct {
	At       time.Time
	Decision store.BotProgressDecision
	Reason   store.BotProgressReason
}

// Source title, confirmed assignment and interaction questions stay on disk.
type botRunTitleControl struct {
	Version          int
	State            store.BotTitleConfirmationState
	BotID            store.BotID
	RunID            store.RunID
	OwnerID          uuid.UUID
	UserEventID      string
	ApprovalID       uuid.UUID
	QuestionID       string
	AssignmentDigest string
	DecidedBy        uuid.UUID
	DecidedAt        time.Time
}

type botRunContent struct {
	Format int          `json:"format"`
	Stored botStoredRun `json:"stored"`
	// Raw checkpoint formatting is preserved independently of JSON marshaling.
	PreparedPlans  map[string]botPreparedPlanBytes `json:"prepared_plans,omitempty"`
	Checkpoint     []byte                          `json:"checkpoint"`
	TaskParameters map[string][]byte               `json:"task_parameters"`
}

type botPreparedPlanBytes struct {
	Plan           store.BotPreparedPlan
	EngineRecord   []byte
	TrustedResult  []byte
	Snapshot       []byte
	Arguments      []byte
	TargetSnapshot []byte
	History        []botPreparedPlanEvidenceBytes
}
type botPreparedPlanEvidenceBytes struct {
	Evidence      store.BotPreparedPlanLeafEvidence
	EngineRecord  []byte
	TrustedResult []byte
}

func preparedPlanBytes(p store.BotPreparedPlan) botPreparedPlanBytes {
	out := botPreparedPlanBytes{Plan: p, EngineRecord: p.EngineRecord, TrustedResult: p.TrustedResult, Snapshot: p.Snapshot, Arguments: p.Action.Arguments, TargetSnapshot: p.Action.TargetSnapshot}
	out.Plan.EngineRecord, out.Plan.TrustedResult, out.Plan.Snapshot = nil, nil, nil
	out.Plan.Action.Arguments, out.Plan.Action.TargetSnapshot = nil, nil
	out.Plan.History = nil
	if p.History != nil {
		out.History = make([]botPreparedPlanEvidenceBytes, len(p.History))
		for i, e := range p.History {
			out.History[i] = botPreparedPlanEvidenceBytes{Evidence: e, EngineRecord: e.EngineRecord, TrustedResult: e.TrustedResult}
			out.History[i].Evidence.EngineRecord, out.History[i].Evidence.TrustedResult = nil, nil
		}
	}
	return out
}
func (b botPreparedPlanBytes) plan() store.BotPreparedPlan {
	p := b.Plan
	p.EngineRecord, p.TrustedResult, p.Snapshot = b.EngineRecord, b.TrustedResult, b.Snapshot
	p.Action.Arguments, p.Action.TargetSnapshot = b.Arguments, b.TargetSnapshot
	if b.History != nil {
		p.History = make([]store.BotPreparedPlanLeafEvidence, len(b.History))
		for i, e := range b.History {
			p.History[i] = e.Evidence
			p.History[i].EngineRecord, p.History[i].TrustedResult = e.EngineRecord, e.TrustedResult
		}
	}
	return p
}
func runContentControl(v botStoredRun) botRunControl {
	r := v.Run
	c := botRunControl{ID: r.ID, BotID: r.BotID, Version: r.Version, SessionID: r.SessionID,
		StateSequence: r.StateSequence, StateCommittedAt: r.StateCommittedAt, SkillID: r.SkillID, Task: r.Task,
		MandateRef: r.MandateRef, CallerCeiling: r.CallerCeiling, RuntimeAccountingRecorded: r.RuntimeAccountingRecorded,
		ExecutionMode: r.ExecutionMode, ContinuationReason: r.ContinuationReason, Presentation: r.Presentation,
		UserEventID: r.UserEventID, HistoryCursor: r.HistoryCursor, Intent: r.Intent, Trigger: r.Trigger, TriggerID: r.TriggerID,
		IdempotencyKey: r.IdempotencyKey, Status: r.Status, Lease: r.Lease, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt,
		FinishedAt: r.FinishedAt, Usage: r.Usage, Provider: r.Provider, Model: r.Model,
		RequestHash: v.RequestHash, CommitHash: v.CommitHash, OccurrenceAt: v.OccurrenceAt,
		ExternalAdmissionValidated: v.ExternalAdmissionValidated, SkillAdmissionOwner: v.SkillAdmissionOwner,
		ContinuationRequests: v.ContinuationRequests, ProgressOwnerContinuation: v.ProgressOwnerContinuation,
		CoalescedOccurrences: v.CoalescedOccurrences, ProgressCursor: v.ProgressCursor, ProgressPending: v.ProgressPending,
		ProgressUncertain: v.ProgressUncertain, ProgressAccountedAt: v.ProgressAccountedAt, ProgressQuota: v.ProgressQuota}
	if len(v.PreparedPlans) > 0 {
		c.PreparedPlanHashes = make(map[string]string, len(v.PreparedPlans))
		for key, prepared := range v.PreparedPlans {
			c.PreparedPlanHashes[key], _ = runContentDigest(prepared)
		}
	}
	if w := v.Work; w != nil {
		c.Work = &botRunWorkControl{SchemaVersion: w.SchemaVersion, Revision: w.Revision, Protocol: w.Protocol, Phase: w.Phase,
			RequirementsDigest: w.RequirementsDigest, EmployeeReviewPolicy: w.EmployeeReviewPolicy, EmployeeReviewAttempts: w.EmployeeReviewAttempts,
			EmployeeReviewPending: w.EmployeeReviewPending, EmployeeRepairPending: w.EmployeeRepairPending, ResponseOutcome: w.ResponseOutcome, Repairs: w.Repairs}
		if w.Contract.Skill != nil {
			c.Work.SkillAuthorityConstraintRef = w.Contract.Skill.AuthorityConstraintRef
		}
	}
	if v.SkillRequests != nil {
		c.SkillRequestHashes = make(map[string]string, len(v.SkillRequests))
		for k, replay := range v.SkillRequests {
			c.SkillRequestHashes[k] = replay.Hash
		}
	}
	if p := v.Progress; p != nil {
		c.Progress = &botRunProgressControl{SchemaVersion: p.SchemaVersion, Phase: p.Phase, Reason: p.Reason}
		if cp := p.Checkpoint; cp != nil {
			c.Progress.Checkpoint = &botRunCheckpointControl{Kind: cp.Kind, SchemaVersion: cp.SchemaVersion, Binding: cp.Binding,
				Revision: cp.Revision, TurnID: cp.TurnID, EventCursor: cp.EventCursor, TranscriptHighWater: cp.TranscriptHighWater,
				ContextDigest: cp.ContextDigest, NextEligibleAt: cp.NextEligibleAt, Usage: cp.Usage, DispatchLedgerCursor: cp.DispatchLedgerCursor}
			if e := cp.LastEvaluation; e != nil {
				c.Progress.Checkpoint.Evaluation = &botRunEvaluationControl{At: e.At, Decision: e.Decision, Reason: e.Reason}
			}
		}
	}
	if t := r.TitleConfirmation; t != nil {
		c.Title = &botRunTitleControl{Version: t.Version, State: t.State, BotID: t.BotID, RunID: t.RunID, OwnerID: t.OwnerID,
			UserEventID: t.UserEventID, ApprovalID: t.ApprovalID, QuestionID: t.QuestionID, AssignmentDigest: t.AssignmentDigest,
			DecidedBy: t.DecidedBy, DecidedAt: t.DecidedAt}
	}
	return c
}

func runContentDigest(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// applyRunControl reconstructs the public run from SQL coordination and only
// copies the explicitly content-bearing fields from the authenticated object.
func applyRunControl(v *botStoredRun, c botRunControl) {
	body := v.Run
	v.Run = store.BotRun{ID: c.ID, BotID: c.BotID, Version: c.Version, SessionID: c.SessionID,
		StateSequence: c.StateSequence, StateCommittedAt: c.StateCommittedAt, SkillID: c.SkillID, Task: c.Task,
		MandateRef: c.MandateRef, CallerCeiling: c.CallerCeiling, RuntimeAccountingRecorded: c.RuntimeAccountingRecorded,
		ExecutionMode: c.ExecutionMode, ContinuationReason: c.ContinuationReason, Presentation: c.Presentation,
		UserEventID: c.UserEventID, HistoryCursor: c.HistoryCursor, Intent: c.Intent, Trigger: c.Trigger, TriggerID: c.TriggerID,
		IdempotencyKey: c.IdempotencyKey, Status: c.Status, Lease: c.Lease, CreatedAt: c.CreatedAt, StartedAt: c.StartedAt,
		FinishedAt: c.FinishedAt, Usage: c.Usage, Provider: c.Provider, Model: c.Model,
		TerminalReason: body.TerminalReason, Progress: body.Progress, Work: body.Work, Invocation: body.Invocation, Todos: body.Todos,
		Checkpoint: body.Checkpoint, Report: body.Report, TitleConfirmation: body.TitleConfirmation}
	v.RequestHash, v.CommitHash, v.OccurrenceAt = c.RequestHash, c.CommitHash, c.OccurrenceAt
	v.ExternalAdmissionValidated, v.SkillAdmissionOwner = c.ExternalAdmissionValidated, c.SkillAdmissionOwner
	v.ContinuationRequests, v.ProgressOwnerContinuation = c.ContinuationRequests, c.ProgressOwnerContinuation
	v.CoalescedOccurrences, v.ProgressCursor, v.ProgressPending = c.CoalescedOccurrences, c.ProgressCursor, c.ProgressPending
	v.ProgressUncertain, v.ProgressAccountedAt, v.ProgressQuota = c.ProgressUncertain, c.ProgressAccountedAt, c.ProgressQuota
}

func (c *botContentCodec) encodeRun(ctx context.Context, v botStoredRun) (map[string]any, error) {
	if v.Run.BotID == uuid.Nil || v.Run.ID == uuid.Nil || v.Run.Version < 1 {
		return nil, store.ErrBotInvalid
	}
	logical, err := runContentDigest(v)
	if err != nil {
		return nil, err
	}
	control := runContentControl(v)
	// No caller, mandate or admission authority is read back from runtime content.
	body := v
	body.Run.MandateRef, body.Run.CallerCeiling = nil, nil
	body.Run.RuntimeAccountingRecorded = false
	body.ExternalAdmissionValidated, body.SkillAdmissionOwner = false, nil
	body.ProgressOwnerContinuation, body.ProgressQuota = nil, nil
	content := botRunContent{Format: botRuntimeRecordFormat, Stored: body, Checkpoint: v.Run.Checkpoint}
	if v.PreparedPlans != nil {
		content.PreparedPlans = make(map[string]botPreparedPlanBytes, len(v.PreparedPlans))
		for key, p := range v.PreparedPlans {
			content.PreparedPlans[key] = preparedPlanBytes(p)
		}
		content.Stored.PreparedPlans = nil
	}
	if v.Task != nil && v.Task.Manifest.Parameters != nil {
		content.TaskParameters = make(map[string][]byte, len(v.Task.Manifest.Parameters))
		for key, value := range v.Task.Manifest.Parameters {
			content.TaskParameters[key] = value
		}
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	return c.encodeRuntimeRecord(ctx, v.Run.BotID, &botRunRecord{botRuntimeInline: runtimeInline(raw), LogicalDigest: logical, Control: control})
}

func (c *botContentCodec) decodeRun(ctx context.Context, id store.BotID, data map[string]any) (_ *botStoredRun, resultErr error) {
	var record botRunRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	r := record.Control
	if id == uuid.Nil || r.BotID != id || r.ID == uuid.Nil || r.Version < 1 || len(record.LogicalDigest) != 64 {
		return nil, store.ErrBotInvalid
	}
	if err := validateRuntimePayload(record.botRuntimeInline); err != nil {
		return nil, err
	}
	raw, err := c.readRuntimeBody(ctx, id, record.botRuntimeInline)
	if err != nil {
		return nil, err
	}
	var body botRunContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		return nil, errors.Join(store.ErrBotInvalid, err)
	}
	if decoder.Decode(new(any)) != io.EOF || body.Format != botRuntimeRecordFormat || body.Stored.Run.BotID != id || body.Stored.Run.ID != r.ID || body.Stored.Run.Version != r.Version {
		return nil, store.ErrBotConflict
	}
	v := body.Stored
	if body.PreparedPlans != nil {
		v.PreparedPlans = make(map[string]store.BotPreparedPlan, len(body.PreparedPlans))
		for key, p := range body.PreparedPlans {
			v.PreparedPlans[key] = p.plan()
		}
	}
	if v.SkillRequests == nil && r.SkillRequestHashes != nil && len(r.SkillRequestHashes) == 0 {
		v.SkillRequests = make(map[string]botSkillReplay)
	}
	v.Run.Checkpoint = json.RawMessage(body.Checkpoint)
	if v.Task != nil {
		v.Task.Manifest.Parameters = nil
		if body.TaskParameters != nil {
			v.Task.Manifest.Parameters = make(map[string]json.RawMessage, len(body.TaskParameters))
			for key, value := range body.TaskParameters {
				v.Task.Manifest.Parameters[key] = json.RawMessage(value)
			}
		}
	} else if body.TaskParameters != nil {
		return nil, store.ErrBotConflict
	}
	applyRunControl(&v, r)
	// Compare the nested title/progress/replay controls as well as the complete
	// logical value. SQL cannot silently select a different run's valid object.
	actual, err := runContentDigest(v)
	if err != nil || actual != record.LogicalDigest || !reflect.DeepEqual(runContentControl(v), r) {
		return nil, store.ErrBotConflict
	}
	// Reassign nested control fields from SQL after the integrity comparison.
	if p := r.Progress; p != nil {
		v.Progress.SchemaVersion, v.Progress.Phase, v.Progress.Reason = p.SchemaVersion, p.Phase, p.Reason
		if cp := p.Checkpoint; cp != nil {
			old := v.Progress.Checkpoint
			v.Progress.Checkpoint = &store.BotProgressCheckpoint{Kind: cp.Kind, SchemaVersion: cp.SchemaVersion, Binding: cp.Binding,
				Revision: cp.Revision, TurnID: cp.TurnID, EventCursor: cp.EventCursor, TranscriptHighWater: cp.TranscriptHighWater,
				ContextRef: old.ContextRef, ContextDigest: cp.ContextDigest, NextEligibleAt: cp.NextEligibleAt, Usage: cp.Usage,
				DispatchLedgerCursor: cp.DispatchLedgerCursor}
			if e := cp.Evaluation; e != nil {
				v.Progress.Checkpoint.LastEvaluation = &store.BotProgressEvaluation{At: e.At, Decision: e.Decision, Reason: e.Reason, Summary: old.LastEvaluation.Summary}
			}
		}
	}
	if title := r.Title; title != nil {
		old := v.Run.TitleConfirmation
		v.Run.TitleConfirmation = &store.BotTitleConfirmation{Version: title.Version, State: title.State, BotID: title.BotID, RunID: title.RunID,
			OwnerID: title.OwnerID, UserEventID: title.UserEventID, ApprovalID: title.ApprovalID, QuestionID: title.QuestionID,
			AssignmentDigest: title.AssignmentDigest, DecidedBy: title.DecidedBy, DecidedAt: title.DecidedAt,
			OriginalTitle: old.OriginalTitle, Question: old.Question, ConfirmedAssignment: old.ConfirmedAssignment}
	}
	if w := r.Work; w != nil {
		v.Work.SchemaVersion, v.Work.Revision, v.Work.Protocol, v.Work.Phase = w.SchemaVersion, w.Revision, w.Protocol, w.Phase
		v.Work.RequirementsDigest, v.Work.EmployeeReviewPolicy = w.RequirementsDigest, w.EmployeeReviewPolicy
		v.Work.EmployeeReviewAttempts, v.Work.EmployeeReviewPending = w.EmployeeReviewAttempts, w.EmployeeReviewPending
		v.Work.EmployeeRepairPending, v.Work.ResponseOutcome, v.Work.Repairs = w.EmployeeRepairPending, w.ResponseOutcome, w.Repairs
		if v.Work.Contract.Skill != nil {
			v.Work.Contract.Skill.AuthorityConstraintRef = w.SkillAuthorityConstraintRef
		}
	}
	for key, hash := range r.SkillRequestHashes {
		replay := v.SkillRequests[key]
		replay.Hash = hash
		v.SkillRequests[key] = replay
	}
	return &v, nil
}
