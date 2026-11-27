package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BotRunPresentation controls transcript destinations independently of execution consent.
// The zero value retains the historical per-intent projection contract.
type BotRunPresentation string

const (
	BotRunPresentationLegacy          BotRunPresentation = ""
	BotRunPresentationHomeInteractive BotRunPresentation = "home_interactive"
)

// BotMessageRequest is a home conversation turn, not consent to activate a Bot.
type BotMessageRequest struct {
	PlanMode              *bool                    `json:"planMode,omitempty"`
	ApprovedPlanExecution bool                     `json:"approvedPlanExecution,omitempty"`
	PlanExecution         *BotPlanExecutionConsent `json:"planExecution,omitempty"`
	TaskID                *uuid.UUID               `json:"task_id,omitempty"`
	ScheduleCommand       *BotOwnerScheduleCommand `json:"schedule_command,omitempty"`
	BotID                 BotID                    `json:"-"`
	Version               int64                    `json:"version"`
	IdempotencyKey        string                   `json:"idempotency_key"`
	Instructions          string                   `json:"instructions"`
	Attachments           []ChatFile               `json:"attachments,omitempty"`
}

type BotMessageReceipt struct {
	Run           BotRun    `json:"run"`
	HomeSessionID uuid.UUID `json:"home_session_id"`
	UserEventID   string    `json:"user_event_id"`
	HistoryCursor int64     `json:"history_cursor"`
}

// BotMessageStore atomically admits a run and its canonical user transcript event.
// Optional so legacy repositories and callers retain their existing contracts.
type BotMessageStore interface {
	AdmitMessage(context.Context, BotMessageRequest) (*BotMessageReceipt, error)
}

// BotEmployeePresentationState belongs to the private repository envelope, never
// the editable report. Pending -> claimed -> completed/unavailable; terminal
// records are immutable and a started, unsettled model turn is not replayable.
type BotEmployeePresentationState string

const (
	BotEmployeePresentationVersion                                  = 1
	BotEmployeePresentationPending     BotEmployeePresentationState = "pending"
	BotEmployeePresentationClaimed     BotEmployeePresentationState = "claimed"
	BotEmployeePresentationCompleted   BotEmployeePresentationState = "completed"
	BotEmployeePresentationUnavailable BotEmployeePresentationState = "unavailable"
)

type BotEmployeeModelTurnState string

const (
	BotEmployeeModelTurnStarted BotEmployeeModelTurnState = "started"
	BotEmployeeModelTurnSettled BotEmployeeModelTurnState = "settled"
)

type BotEmployeeModelTurn struct {
	ID            string
	State         BotEmployeeModelTurnState
	DispatchToken string
	StartedAt     time.Time
	SettledAt     time.Time
	Reserved      BotUsage
	Usage         BotUsage
}

type BotEmployeeWorkerRole string

const (
	BotEmployeeWorkerUser  BotEmployeeWorkerRole = "user"
	BotEmployeeWorkerModel BotEmployeeWorkerRole = "model"
	BotEmployeeWorkerTool  BotEmployeeWorkerRole = "tool"
)

type BotEmployeeWorkerMessage struct {
	Role BotEmployeeWorkerRole
	Text string
}

type BotEmployeeWorkerHistory struct {
	WorkerID  string // opaque server-issued handle, not session/owner authority
	SessionID uuid.UUID
	Messages  []BotEmployeeWorkerMessage
	Turns     []BotEmployeeModelTurn
}

type BotEmployeeOperationIdentity struct {
	OperationID   uuid.UUID
	Revision      int64
	ReceiptDigest string
}

// BotEmployeePresentation is tools-disabled continuation ownership. All fields
// are ignored by public JSON. Only botStoredRun's explicit private codec persists
// them. Go callers are trusted, just as with the existing private caller ceiling.
type BotEmployeePresentation struct {
	SchemaVersion                  int
	State                          BotEmployeePresentationState
	BotID                          BotID
	RunID                          RunID
	BotVersion                     int64
	OwnerID                        uuid.UUID
	OrgSlug, TeamSlug              string
	HomeSessionID, SourceSessionID uuid.UUID
	UserEventID                    string
	EffectiveLimits                BudgetSpec
	Deadline, AuthorityExpiresAt   time.Time
	Provider, Model                string
	ReportSequence                 int64
	ReportDigest                   string
	Operations                     []BotEmployeeOperationIdentity
	ClaimToken, DispatchToken      string
	Usage                          BotUsage
	Turns                          []BotEmployeeModelTurn
	Workers                        []BotEmployeeWorkerHistory
	FinalText, FinalTextDigest     string
	UnavailableReason              BotEmployeeFailureReason // neutral status, never assistant speech
}

func (BotEmployeePresentation) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (p *BotEmployeePresentation) UnmarshalJSON([]byte) error {
	*p = BotEmployeePresentation{}
	return nil
}

// MarshalPrivateJSON/UnmarshalPrivateJSON are repository codec hooks, not public
// payload entry points. No claim can be created by decoding BotRun/report JSON.
func (p BotEmployeePresentation) MarshalPrivateJSON() ([]byte, error) {
	type private BotEmployeePresentation
	return json.Marshal(private(p))
}
func (p *BotEmployeePresentation) UnmarshalPrivateJSON(data []byte, run BotRun) error {
	type private BotEmployeePresentation
	var value private
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	candidate := BotEmployeePresentation(value)
	if err := candidate.ValidateBinding(run); err != nil {
		return err
	}
	*p = candidate
	return nil
}
func (p *BotEmployeePresentation) ValidateBinding(run BotRun) error {
	if p == nil || p.SchemaVersion != BotEmployeePresentationVersion {
		return ErrBotUnsupported
	}
	if p.BotID == uuid.Nil || p.BotID != run.BotID || p.RunID == uuid.Nil || p.RunID != run.ID || p.BotVersion != run.Version || p.OwnerID == uuid.Nil || p.OrgSlug == "" || p.TeamSlug == "" || p.HomeSessionID == uuid.Nil || p.SourceSessionID != run.SessionID || p.UserEventID == "" || p.UserEventID != run.UserEventID || p.EffectiveLimits.MaxRuntimeSeconds < 0 || p.AuthorityExpiresAt.IsZero() || p.Provider == "" || p.Model == "" || p.ReportSequence <= 0 || p.ReportSequence != run.StateSequence || p.ReportDigest != run.Report.EmployeeMessageSourceDigest(run.Status) {
		return ErrBotInvalid
	}
	if p.BotVersion < 1 || p.SourceSessionID == uuid.Nil {
		return ErrBotInvalid
	}
	for _, limit := range []*int64{p.EffectiveLimits.MaxTokens, p.EffectiveLimits.MaxToolCalls, p.EffectiveLimits.MaxRunsPerDay, p.EffectiveLimits.MaxModelCostMicros} {
		if limit != nil && *limit < 0 {
			return ErrBotInvalid
		}
	}
	if !employeeUsageValid(p.Usage) {
		return ErrBotInvalid
	}
	for _, op := range p.Operations {
		if op.OperationID == uuid.Nil || op.Revision < 1 || op.ReceiptDigest == "" {
			return ErrBotInvalid
		}
	}
	workers := map[string]bool{}
	for _, worker := range p.Workers {
		if worker.WorkerID == "" || workers[worker.WorkerID] || worker.SessionID == uuid.Nil {
			return ErrBotInvalid
		}
		workers[worker.WorkerID] = true
		for _, message := range worker.Messages {
			if message.Role != BotEmployeeWorkerUser && message.Role != BotEmployeeWorkerModel && message.Role != BotEmployeeWorkerTool {
				return ErrBotInvalid
			}
		}
		if err := validateEmployeeModelTurns(worker.Turns); err != nil {
			return err
		}
	}
	if err := validateEmployeeModelTurns(p.Turns); err != nil {
		return err
	}
	switch p.State {
	case BotEmployeePresentationPending:
		if p.ClaimToken != "" || p.DispatchToken != "" || p.FinalText != "" || p.FinalTextDigest != "" || p.UnavailableReason != "" {
			return ErrBotInvalid
		}
	case BotEmployeePresentationClaimed:
		if p.ClaimToken == "" || p.FinalText != "" || p.FinalTextDigest != "" || p.UnavailableReason != "" {
			return ErrBotInvalid
		}
	case BotEmployeePresentationCompleted:
		if p.ClaimToken == "" || p.DispatchToken == "" || p.FinalText == "" || p.FinalTextDigest != BotEmployeeTextDigest(p.FinalText) || p.UnavailableReason != "" {
			return ErrBotInvalid
		}
	case BotEmployeePresentationUnavailable:
		if p.FinalText != "" || p.FinalTextDigest != "" || p.UnavailableReason == "" {
			return ErrBotInvalid
		}
	default:
		return ErrBotInvalid
	}
	return nil
}
func validateEmployeeModelTurns(turns []BotEmployeeModelTurn) error {
	seen := map[string]bool{}
	for _, turn := range turns {
		if turn.ID == "" || seen[turn.ID] || turn.DispatchToken == "" || turn.StartedAt.IsZero() || !employeeUsageValid(turn.Reserved) || !employeeUsageValid(turn.Usage) {
			return ErrBotInvalid
		}
		seen[turn.ID] = true
		switch turn.State {
		case BotEmployeeModelTurnStarted:
			if !turn.SettledAt.IsZero() {
				return ErrBotInvalid
			}
		case BotEmployeeModelTurnSettled:
			if turn.SettledAt.Before(turn.StartedAt) {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
	}
	return nil
}
func employeeUsageValid(u BotUsage) bool {
	return u.RuntimeNanos >= 0 && u.ToolCalls >= 0 && u.InputTokens >= 0 && u.OutputTokens >= 0 && (u.CostMicros == nil || *u.CostMicros >= 0)
}

type BotEmployeePresentationRequest struct {
	BotID      BotID
	RunID      RunID
	ClaimToken string // bound by server dispatch, not public/model input
	Now        time.Time
}
type BotEmployeePresentationClaim struct {
	BotEmployeePresentationRequest
	ExpectedReportSequence int64
	ExpectedReportDigest   string
}
type BotEmployeePresentationModelReservation struct {
	BotEmployeePresentationRequest
	TurnID, DispatchToken string
	Reserved              BotUsage
}
type BotEmployeePresentationModelSettlement struct {
	BotEmployeePresentationRequest
	TurnID, DispatchToken string
	Usage                 BotUsage
}
type BotEmployeePresentationCompletion struct {
	BotEmployeePresentationRequest
	DispatchToken     string
	Text              string
	UnavailableReason BotEmployeeFailureReason
}

// Optional request-scoped interface. Implementations reauthorize the source,
// preserve original authority expiry/model pin and usage, CAS claim/turn identities and
// never revive an unsettled dispatch or gain a tool lease from presentation.
type BotEmployeePresentationStore interface {
	ClaimEmployeePresentation(context.Context, BotEmployeePresentationClaim) (*BotEmployeePresentation, error)
	ReadEmployeePresentation(context.Context, BotEmployeePresentationRequest) (*BotEmployeePresentation, error)
	ReserveEmployeePresentationModel(context.Context, BotEmployeePresentationModelReservation) (*BotEmployeePresentation, error)
	SettleEmployeePresentationModel(context.Context, BotEmployeePresentationModelSettlement) (*BotEmployeePresentation, error)
	CompleteEmployeePresentation(context.Context, BotEmployeePresentationCompletion) (*BotEmployeePresentation, error)
}

func (r BotRun) ControlOnly() bool {
	// Unknown modes never acquire execution merely through a regular intent.
	if !r.ExecutionMode.Valid() {
		return true
	}
	return r.Intent == BotIntentConversation
}
func (r BotRun) UsesHomeHistory() bool {
	return r.Presentation == BotRunPresentationHomeInteractive || r.Intent == BotIntentConversation
}
