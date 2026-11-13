package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// Bot identities use UUID JSON encoding and are never inferred from session names.
type BotID = uuid.UUID
type RunID = uuid.UUID

const BotSchemaVersion = 1
const BotRetention = 30 * 24 * time.Hour
const DefaultBotRuntime = 15 * time.Minute

var (
	ErrBotConflict    = errors.New("bot revision or lease conflict")
	ErrBotForbidden   = errors.New("bot operation forbidden")
	ErrBotUnsupported = errors.New("bot capability unsupported")
	ErrBotExpired     = errors.New("bot approval or retention expired")
	ErrBotNotFound    = errors.New("bot resource not found")
	ErrBotInvalid     = errors.New("invalid bot request")
	// ErrBotBusy is a retryable proposal decision barrier, not a stale review.
	// Preserve errors.Is(err, ErrBotConflict) for existing callers.
	ErrBotBusy = fmt.Errorf("bot is still running or finishing cleanup; retry shortly: %w", ErrBotConflict)
)

type BotScope string

const (
	BotScopePersonal BotScope = "personal"
	BotScopeTeam     BotScope = "team"
)

type BotStatus string

const (
	BotDraft   BotStatus = "draft"
	BotActive  BotStatus = "active"
	BotPaused  BotStatus = "paused"
	BotStopped BotStatus = "stopped"
	BotDeleted BotStatus = "deleted"
)

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunInterrupted     RunStatus = "interrupted"
	RunSucceeded       RunStatus = "succeeded"
	RunPartial         RunStatus = "partial"
	RunFailed          RunStatus = "failed"
	RunSkipped         RunStatus = "skipped"
	RunCancelled       RunStatus = "cancelled"
)

func (s RunStatus) Terminal() bool {
	switch s {
	case RunSucceeded, RunPartial, RunFailed, RunSkipped, RunCancelled:
		return true
	}
	return false
}

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
	ApprovalRevoked  ApprovalStatus = "revoked"
	ApprovalConsumed ApprovalStatus = "consumed"
)

type TriggerKind string

const (
	TriggerManual       TriggerKind = "manual"
	TriggerCron         TriggerKind = "cron"
	TriggerHook         TriggerKind = "hook"
	TriggerWebhook      TriggerKind = "webhook"
	TriggerChannel      TriggerKind = "channel"
	TriggerGoalDeadline TriggerKind = "goal_deadline"
)

type NotificationDecision string

const (
	NotificationSend  NotificationDecision = "send"
	NotificationDefer NotificationDecision = "defer"
	NotificationSkip  NotificationDecision = "skip"
)

type NotifyPolicy string

const (
	NotifyNever      NotifyPolicy = "never"
	NotifyOnChange   NotifyPolicy = "on_change"
	NotifyOnDecision NotifyPolicy = "on_decision_needed"
	NotifyAlways     NotifyPolicy = "always"
)

type HookStatus string

const (
	HookDisabled     HookStatus = "disabled"
	HookDryRunPassed HookStatus = "dry_run_passed"
	HookEnabled      HookStatus = "enabled"
)

type Bot struct {
	ID              BotID          `json:"id"`
	Scope           BotScope       `json:"scope"`
	OwnerID         uuid.UUID      `json:"owner_id"`
	Name            string         `json:"name"`
	Status          BotStatus      `json:"status"`
	PreviousStatus  BotStatus      `json:"previous_status,omitempty"`
	Revision        int64          `json:"revision"`
	Version         int64          `json:"version"`
	HomeSessionID   uuid.UUID      `json:"home_session_id"`
	Home            BotHomeBinding `json:"home"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       *time.Time     `json:"deleted_at,omitempty"`
	PermanentDelete bool           `json:"permanent_delete,omitempty"`
	RestoreUntil    *time.Time     `json:"restore_until,omitempty"`
}

type BotPersona struct {
	Avatar   string `json:"avatar,omitempty"`
	Language string `json:"language"`
	Timezone string `json:"timezone"`
	Soul     string `json:"soul"`
	Identity string `json:"identity"`
	User     string `json:"user"`
}
type BotAutonomy struct {
	Level                 execution.AutonomyLevel `json:"level"`
	AllowedTools          []string                `json:"allowed_tools"`
	DeniedTools           []string                `json:"denied_tools"`
	AllowedPaths          []string                `json:"allowed_paths"`
	AllowedHosts          []string                `json:"allowed_hosts"`
	CanContactPeople      bool                    `json:"can_contact_people"`
	CanPublish            bool                    `json:"can_publish"`
	SpendCurrency         string                  `json:"spend_currency,omitempty"`
	MaxSpendPerRunMinor   int64                   `json:"max_spend_per_run_minor"`
	MaxSpendPerMonthMinor int64                   `json:"max_spend_per_month_minor"`
}

// BudgetSpec uses nil for unlimited/unknown, not zero. Spend caps are separate.
type BudgetSpec struct {
	MaxRuntimeSeconds  int64  `json:"max_runtime_seconds"`
	MaxToolCalls       *int64 `json:"max_tool_calls,omitempty"`
	MaxTokens          *int64 `json:"max_tokens,omitempty"`
	MaxRunsPerDay      *int64 `json:"max_runs_per_day,omitempty"`
	MaxModelCostMicros *int64 `json:"max_model_cost_micros,omitempty"`
}

// Binding ID resolves a server-verified linked identity, not a caller-created
// grant. Exact destinations are revalidated under the Bot owner scope at save
// and every send; unsupported installation/room proofs fail closed.
type BotChannelBinding struct {
	ID       uuid.UUID `json:"id"`
	Channel  string    `json:"channel"`
	ChatID   string    `json:"chat_id"`
	ThreadID string    `json:"thread_id,omitempty"`
}
type DeliveryPolicy struct {
	Notify      NotifyPolicy        `json:"notify"`
	Preferences string              `json:"preferences"`
	Bindings    []BotChannelBinding `json:"bindings"`
}
type TriggerSpec struct {
	SkillID        *uuid.UUID         `json:"skill_id,omitempty"`
	Invocation     string             `json:"invocation,omitempty"`
	ID             uuid.UUID          `json:"id"`
	Kind           TriggerKind        `json:"kind"`
	ScheduleID     *uuid.UUID         `json:"schedule_id,omitempty"`
	GoalID         *uuid.UUID         `json:"goal_id,omitempty"`
	Instructions   string             `json:"instructions"`
	Cron           string             `json:"cron,omitempty"`
	Timezone       string             `json:"timezone,omitempty"`
	Enabled        bool               `json:"enabled"`
	HookStatus     HookStatus         `json:"hook_status,omitempty"`
	CredentialRef  string             `json:"credential_ref,omitempty"`
	Fingerprint    string             `json:"fingerprint,omitempty"`
	CatchUpOnce    bool               `json:"catch_up_once"`
	Poll           *BotHookPoll       `json:"poll,omitempty"`
	HookWatermark  string             `json:"hook_watermark,omitempty"`
	ChannelBinding *BotChannelBinding `json:"channel_binding,omitempty"`
}

// BotVersion is an immutable snapshot; edits insert a new version, never update.
type BotVersion struct {
	SchemaVersion   int            `json:"schema_version"`
	BotID           BotID          `json:"bot_id"`
	Version         int64          `json:"version"`
	Charter         string         `json:"charter"`
	Persona         BotPersona     `json:"persona"`
	Autonomy        BotAutonomy    `json:"autonomy"`
	Budget          BudgetSpec     `json:"budget"`
	Delivery        DeliveryPolicy `json:"delivery"`
	Triggers        []TriggerSpec  `json:"triggers"`
	SandboxTemplate string         `json:"sandbox_template"`
	CreatedBy       uuid.UUID      `json:"created_by"`
	CreatedAt       time.Time      `json:"created_at"`
	ChangeReason    string         `json:"change_reason"`
}

func DefaultBotVersion() BotVersion {
	return BotVersion{SchemaVersion: BotSchemaVersion, Version: 1, Persona: BotPersona{Timezone: "UTC"}, Autonomy: BotAutonomy{Level: execution.AutonomySupervised}, Budget: BudgetSpec{MaxRuntimeSeconds: int64(DefaultBotRuntime / time.Second)}, Delivery: DeliveryPolicy{Notify: NotifyOnChange}}
}

// Home binding is opaque and independently retained, not a physical run session.
type BotHomeBinding struct {
	ID         uuid.UUID `json:"id"`
	BotID      BotID     `json:"bot_id"`
	Scope      BotScope  `json:"scope"`
	Backend    string    `json:"backend"`
	StorageKey string    `json:"storage_key"`
	Revision   int64     `json:"revision"`
}
type BotLease struct {
	BotID     BotID     `json:"bot_id"`
	RunID     RunID     `json:"run_id"`
	WorkerID  string    `json:"worker_id"`
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}
type BotArtifact struct {
	ID           uuid.UUID `json:"id"`
	HomeID       uuid.UUID `json:"home_id"`
	RelativePath string    `json:"relative_path"`
	SHA256       string    `json:"sha256"`
	MediaType    string    `json:"media_type"`
	Title        string    `json:"title"`
}
type BotUsage struct {
	// RuntimeNanos is cumulative active execution time across checkpoint resumes.
	RuntimeNanos int64  `json:"runtime_nanos,omitempty"`
	ToolCalls    int64  `json:"tool_calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicros   *int64 `json:"cost_micros,omitempty"`
}
type BotReportFailureKind string

const (
	BotReportFailureBudget          BotReportFailureKind = "budget"
	BotReportFailureExecutionFormat BotReportFailureKind = "execution_format"
	BotReportFailureOperational     BotReportFailureKind = "operational"
	BotReportFailureInterrupted     BotReportFailureKind = "interrupted"
	BotReportFailureSetup           BotReportFailureKind = "setup"
)

type BotReportFailure struct {
	Kind             BotReportFailureKind `json:"kind"`
	TechnicalDetails string               `json:"technical_details,omitempty"`
}
type BotReportAcknowledgmentKind string

const BotReportAcknowledgmentSchedulePending BotReportAcknowledgmentKind = "schedule_pending"
const BotReportAcknowledgmentControlPending BotReportAcknowledgmentKind = "control_pending"

// BotReportAcknowledgment is assigned by CommitRun from accepted private work and
// scoped operation provenance, never trusted from a caller or model report.
type BotReportAcknowledgment struct {
	Kind         BotReportAcknowledgmentKind `json:"kind"`
	OperationIDs []uuid.UUID                 `json:"operation_ids"`
}

// BotEmployeeMessage is communication, never new evidence or authority. Assessed
// messages retain the exact candidate text/digest. Model and fallback messages
// bind only to server-owned outcome facts via SourceDigest, not to new claims.
type BotEmployeeMessageOrigin string

const (
	BotEmployeeMessageAgent     BotEmployeeMessageOrigin = "agent"
	BotEmployeeMessageAssessed  BotEmployeeMessageOrigin = "assessed"
	BotEmployeeMessageModel     BotEmployeeMessageOrigin = "model"
	BotEmployeeMessageFallback  BotEmployeeMessageOrigin = "fallback"
	BotEmployeeMessageOperation BotEmployeeMessageOrigin = "operation"
)

type BotEmployeeMessage struct {
	// reviewAttestation exists only in this process after trusted composition; JSON
	// hashes alone are integrity checks, not evidence that review ran.
	reviewAttestation    *botEmployeeReviewAttestation
	Origin               BotEmployeeMessageOrigin `json:"origin"`
	Text                 string                   `json:"text"`
	SourceDigest         string                   `json:"source_digest"`
	CandidateDigest      string                   `json:"candidate_digest,omitempty"`
	Reason               BotEmployeeFailureReason `json:"reason,omitempty"`
	TextDigest           string                   `json:"text_digest,omitempty"`
	ReviewedSourceDigest string                   `json:"reviewed_source_digest,omitempty"`
	ReviewedTextDigest   string                   `json:"reviewed_text_digest,omitempty"`
	OperationID          uuid.UUID                `json:"operation_id,omitempty"`
	OperationRevision    int64                    `json:"operation_revision,omitempty"`
	FailureKind          BotReportFailureKind     `json:"failure_kind,omitempty"`
}

type BotReport struct {
	Work *BotWorkProjection `json:"work,omitempty"`
	// Skill is assigned from committed repository state, never model claims.
	Skill           *BotSkillReceipt         `json:"skill,omitempty"`
	EmployeeMessage *BotEmployeeMessage      `json:"employee_message,omitempty"`
	Acknowledgment  *BotReportAcknowledgment `json:"acknowledgment,omitempty"`
	Quality         *BotQualityReport        `json:"quality,omitempty"`
	Failure         *BotReportFailure        `json:"failure,omitempty"`
	Summary         string                   `json:"summary"`
	Changed         []BotEvidence            `json:"changed"`
	Unchanged       []BotEvidence            `json:"unchanged"`
	Unverified      []string                 `json:"unverified"`
	Artifacts       []BotArtifact            `json:"artifacts"`
	NeedsApproval   []uuid.UUID              `json:"needs_approval"`
	NextRunAt       *time.Time               `json:"next_run_at,omitempty"`
}

// EmployeeMessageSourceDigest binds the immutable communication source, excluding
// the envelope itself. Committers must validate before changing report fields.
func (r BotReport) EmployeeMessageSourceDigest(status RunStatus) string {
	r.EmployeeMessage = nil
	raw, _ := json.Marshal(struct {
		Status RunStatus
		Report BotReport
	}{status, r})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidateEmployeeMessage checks integrity and assessed-candidate identity. It
// never substitutes public quality claims for the private validated work state.
// A nil envelope is allowed for legacy persisted reports.
func (r BotReport) ValidateEmployeeMessage(status RunStatus, work *BotWorkState) error {
	m := r.EmployeeMessage
	if m == nil {
		return nil
	}
	if m.Reason == BotEmployeeReasonEmailTransport && (status != RunPartial || work.ConfirmedRequiredBlocker() == nil) {
		return ErrBotInvalid
	}
	if err := r.ValidateEmployeeMessageIntegrity(status); err != nil {
		return err
	}
	switch m.Origin {
	case BotEmployeeMessageAgent:
		if work == nil || work.Protocol != BotWorkProtocolAgentLedV1 || work.Candidate == nil || m.CandidateDigest != work.Candidate.Digest {
			return ErrBotInvalid
		}
		return ValidateBotWorkCompletion(work, BotRunCommit{Lease: BotLease{BotID: work.Contract.BotID, RunID: work.Contract.RunID}, Status: status, Report: r})
	case BotEmployeeMessageAssessed:
		if work == nil || work.Validate() != nil || work.Candidate == nil || m.CandidateDigest != work.Candidate.Digest || m.Text != work.Candidate.Text || r.Summary != work.Candidate.Text || m.FailureKind != "" {
			return ErrBotInvalid
		}
		if status == RunSucceeded && work.Phase == BotWorkAccepted {
			return nil
		}
		if status == RunPartial && work.VerifiedBlockedResponse() != nil {
			return nil
		}
		return ErrBotInvalid
	case BotEmployeeMessageModel:
		if !m.HasTrustedReviewAttestation() {
			return ErrBotInvalid
		}
		return nil
	case BotEmployeeMessageFallback:
		if m.CandidateDigest != "" {
			return ErrBotInvalid
		}
		return nil
	default:
		return ErrBotInvalid
	}
}

type BotRun struct {
	Work       *BotWorkProjection `json:"work,omitempty"`
	SkillID    *uuid.UUID         `json:"skill_id,omitempty"`
	Invocation string             `json:"invocation,omitempty"`
	// MandateRef is persisted only in the private repository envelope.
	MandateRef         *BotMandateRef             `json:"-"`
	ContinuationReason BotContinuationReason      `json:"continuation_reason,omitempty"`
	ExecutionMode      execution.BotExecutionMode `json:"execution_mode,omitempty"`
	// CallerCeiling is persisted only in the private repository envelope.
	CallerCeiling  *execution.BotCallerCeiling `json:"-"`
	Presentation   BotRunPresentation          `json:"presentation,omitempty"`
	UserEventID    string                      `json:"user_event_id,omitempty"`
	HistoryCursor  int64                       `json:"history_cursor,omitempty"`
	Intent         BotExecutionIntent          `json:"intent,omitempty"`
	ID             RunID                       `json:"id"`
	BotID          BotID                       `json:"bot_id"`
	Version        int64                       `json:"version"`
	SessionID      uuid.UUID                   `json:"session_id"`
	Trigger        TriggerKind                 `json:"trigger"`
	TriggerID      uuid.UUID                   `json:"trigger_id"`
	IdempotencyKey string                      `json:"idempotency_key"`
	Status         RunStatus                   `json:"status"`
	Lease          BotLease                    `json:"lease"`
	CreatedAt      time.Time                   `json:"created_at"`
	StartedAt      *time.Time                  `json:"started_at,omitempty"`
	FinishedAt     *time.Time                  `json:"finished_at,omitempty"`
	Todos          []BotTodo                   `json:"todos"`
	Checkpoint     json.RawMessage             `json:"checkpoint,omitempty"`
	Report         BotReport                   `json:"report"`
	Usage          BotUsage                    `json:"usage"`
	Provider       string                      `json:"provider"`
	Model          string                      `json:"model"`
}
type BotApproval struct {
	// Interaction distinguishes a typed answer checkpoint from permission to execute.
	Interaction  *BotInteraction            `json:"interaction,omitempty"`
	Response     *BotInteractionResponse    `json:"response,omitempty"`
	ID           uuid.UUID                  `json:"id"`
	BotID        BotID                      `json:"bot_id"`
	RunID        RunID                      `json:"run_id"`
	Version      int64                      `json:"version"`
	Action       execution.ActionDescriptor `json:"action"`
	ActionDigest string                     `json:"action_digest"`
	Status       ApprovalStatus             `json:"status"`
	CreatedAt    time.Time                  `json:"created_at"`
	ExpiresAt    time.Time                  `json:"expires_at"`
	DecidedAt    *time.Time                 `json:"decided_at,omitempty"`
	DecidedBy    *uuid.UUID                 `json:"decided_by,omitempty"`
}
type BotActivityKind string

const (
	BotActivityLifecycle    BotActivityKind = "lifecycle"
	BotActivityRun          BotActivityKind = "run"
	BotActivityReport       BotActivityKind = "report"
	BotActivityApproval     BotActivityKind = "approval"
	BotActivityCorrection   BotActivityKind = "correction"
	BotActivityPersona      BotActivityKind = "persona"
	BotActivityNotification BotActivityKind = "notification"
)

// BotActivity is a control-plane projection, not a duplicate tool transcript.
type BotActivity struct {
	BotID     BotID           `json:"bot_id"`
	RunID     *RunID          `json:"run_id,omitempty"`
	Sequence  int64           `json:"sequence"`
	Kind      BotActivityKind `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}
type BotReadCursor struct {
	BotID    BotID     `json:"bot_id"`
	ViewerID uuid.UUID `json:"viewer_id"`
	Sequence int64     `json:"sequence"`
}
type BotOutboxKind string

const (
	BotOutboxDispatch  BotOutboxKind = "dispatch"
	BotOutboxPublish   BotOutboxKind = "publish"
	BotOutboxNotify    BotOutboxKind = "notify"
	BotOutboxReconcile BotOutboxKind = "reconcile"
	BotOutboxCleanup   BotOutboxKind = "cleanup"
)

type BotOutboxEntry struct {
	ID               uuid.UUID       `json:"id"`
	BotID            BotID           `json:"bot_id"`
	RunID            *RunID          `json:"run_id,omitempty"`
	Kind             BotOutboxKind   `json:"kind"`
	DeduplicationKey string          `json:"deduplication_key"`
	Payload          json.RawMessage `json:"payload"`
	AvailableAt      time.Time       `json:"available_at"`
}

type BotCreateRequest struct {
	Name           string
	OwnerID        uuid.UUID
	Version        BotVersion
	Goals          []BotGoal
	IdempotencyKey string
}
type BotVersionRequest struct {
	BotID            BotID
	ExpectedRevision int64
	Version          BotVersion
}
type BotLifecycleRequest struct {
	BotID            BotID
	ExpectedRevision int64
	ActorID          uuid.UUID
	Reason           string
	Now              time.Time
}
type BotRunRequest struct {
	SkillID        *uuid.UUID         `json:",omitempty"`
	Invocation     string             `json:",omitempty"`
	Intent         BotExecutionIntent `json:",omitempty"`
	BotID          BotID
	Version        int64
	Trigger        TriggerKind
	TriggerID      uuid.UUID
	OccurrenceAt   time.Time
	IdempotencyKey string
	Instructions   string
	Attachments    []ChatFile `json:",omitempty"`
}
type BotClaimRequest struct {
	BotID     BotID
	RunID     RunID
	WorkerID  string
	Now       time.Time
	ExpiresAt time.Time
}
type BotRunCommit struct {
	// Omitted for legacy commits to preserve their idempotency hashes.
	ModelSelection *SessionPin `json:",omitempty"`
	Lease          BotLease
	Status         RunStatus
	Report         BotReport
	Usage          BotUsage
	Todos          []BotTodo
	Checkpoint     json.RawMessage
	Activity       []BotActivity
	Outbox         []BotOutboxEntry
}
type BotApprovalDecision struct {
	Response     *BotInteractionResponse
	BotID        BotID
	ApprovalID   uuid.UUID
	ActorID      uuid.UUID
	ActionDigest string
	Status       ApprovalStatus
	Now          time.Time
}
type BotApprovalConsumption struct {
	Lease        BotLease
	ApprovalID   uuid.UUID
	Version      int64
	ActionDigest string
	Now          time.Time
}
type BotPage struct {
	Limit int
	After string
}

// BotStore is already tenant-scoped by the authenticated router. Implementations
// must validate nested ownership and current authorization, not trust IDs here.
// RequestRun atomically snapshots the version and queues dispatch. CommitRun
// commits outcome/activity/publication intent under a live fencing token.
// Decide/ConsumeApproval use CAS and exact digest/expiry/version checks. No method
// implies a transaction spanning separate personal/team databases or external IO.
type BotStore interface {
	CreateDraft(context.Context, BotCreateRequest) (*Bot, error)
	Get(context.Context, BotID) (*Bot, error)
	List(context.Context, BotPage) ([]Bot, error)
	GetVersion(context.Context, BotID, int64) (*BotVersion, error)
	ApplyVersion(context.Context, BotVersionRequest) (*Bot, error)
	Activate(context.Context, BotLifecycleRequest) (*Bot, error)
	Pause(context.Context, BotLifecycleRequest) (*Bot, error)
	Stop(context.Context, BotLifecycleRequest) (*Bot, error)
	RequestRun(context.Context, BotRunRequest) (*BotRun, error)
	GetRun(context.Context, BotID, RunID) (*BotRun, error)
	ListRuns(context.Context, BotID, BotPage) ([]BotRun, error)
	ClaimRun(context.Context, BotClaimRequest) (BotLease, error)
	RenewLease(context.Context, BotLease, time.Time) (BotLease, error)
	AppendActivity(context.Context, BotLease, BotActivity) (BotActivity, error)
	ListActivity(context.Context, BotID, int64, int) ([]BotActivity, error)
	CommitRun(context.Context, BotRunCommit) error
	RequestApproval(context.Context, BotLease, BotApproval) (*BotApproval, error)
	ListApprovals(context.Context, BotID, BotPage) ([]BotApproval, error)
	DecideApproval(context.Context, BotApprovalDecision) (*BotApproval, error)
	ConsumeApproval(context.Context, BotApprovalConsumption) error
	MarkRead(context.Context, BotReadCursor) error
	BeginDelete(context.Context, BotLifecycleRequest) (*Bot, error)
	Restore(context.Context, BotLifecycleRequest) (*Bot, error)
	// PurgeDue selects expired, fenced, cleanup-confirmed tombstones only.
	PurgeDue(context.Context, time.Time, int) (int, error)
	BotGoalStore
}
