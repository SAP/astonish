// Package bot defines durable Bot orchestration contracts. Execution adapters use
// the existing ChatAgent; this package must not import API or launcher packages.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/model"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

type LifecycleEvent string

const (
	LifecycleActivate LifecycleEvent = "activate"
	LifecyclePause    LifecycleEvent = "pause"
	LifecycleStop     LifecycleEvent = "stop"
	LifecycleResume   LifecycleEvent = "resume"
	LifecycleDelete   LifecycleEvent = "delete"
	LifecycleRestore  LifecycleEvent = "restore"
)

// NextBotStatus validates state shape only. The repository must authenticate the
// event, check expected revision, revoke/fence work and commit related intent.
func NextBotStatus(from store.BotStatus, event LifecycleEvent, now, restoreUntil time.Time) (store.BotStatus, error) {
	valid := from == store.BotDraft || from == store.BotActive || from == store.BotPaused || from == store.BotStopped || from == store.BotDeleted
	if !valid {
		return "", fmt.Errorf("%w: unknown lifecycle state", store.ErrBotInvalid)
	}
	switch event {
	case LifecycleDelete:
		if from != store.BotDeleted {
			return store.BotDeleted, nil
		}
	case LifecycleActivate:
		if from == store.BotDraft {
			return store.BotActive, nil
		}
	case LifecyclePause:
		if from == store.BotActive {
			return store.BotPaused, nil
		}
	case LifecycleStop:
		if from == store.BotActive || from == store.BotPaused {
			return store.BotStopped, nil
		}
	case LifecycleResume:
		if from == store.BotPaused || from == store.BotStopped {
			return store.BotActive, nil
		}
	case LifecycleRestore:
		if from == store.BotDeleted {
			if restoreUntil.IsZero() || !now.Before(restoreUntil) {
				return "", store.ErrBotExpired
			}
			return store.BotPaused, nil
		}
	}
	return "", fmt.Errorf("%w: cannot %s a %s bot", store.ErrBotConflict, event, from)
}

type RunTransitionGuard struct {
	BotActive      bool
	Authorized     bool
	CheckpointSafe bool
}

func ValidateRunTransition(from, to store.RunStatus, g RunTransitionGuard) error {
	allowed := false
	switch from {
	case store.RunQueued:
		allowed = to == store.RunSkipped || to == store.RunCancelled || to == store.RunRunning && g.BotActive && g.Authorized
	case store.RunRunning:
		allowed = to.Terminal() || to == store.RunWaitingApproval || to == store.RunInterrupted
	case store.RunWaitingApproval, store.RunInterrupted:
		allowed = to == store.RunPartial || to == store.RunCancelled || to == store.RunQueued && g.BotActive && g.Authorized && g.CheckpointSafe
	}
	if !allowed {
		return fmt.Errorf("%w: run transition %s -> %s", store.ErrBotConflict, from, to)
	}
	return nil
}

func ValidateApprovalTransition(from, to store.ApprovalStatus, now, expires time.Time) error {
	allowed := false
	switch from {
	case store.ApprovalPending:
		allowed = to == store.ApprovalRevoked || to == store.ApprovalExpired && !now.Before(expires) || (to == store.ApprovalApproved || to == store.ApprovalDenied) && now.Before(expires)
	case store.ApprovalApproved:
		allowed = to == store.ApprovalRevoked || to == store.ApprovalExpired && !now.Before(expires) || to == store.ApprovalConsumed && now.Before(expires)
	}
	if !allowed {
		return fmt.Errorf("%w: approval transition %s -> %s", store.ErrBotConflict, from, to)
	}
	return nil
}

// ValidateVersion is structural validation, not approval of its permissions.
// Cron parsing, backend capabilities and linked resources are checked by adapters.
func ValidateVersion(v store.BotVersion) error {
	if v.SchemaVersion != store.BotSchemaVersion {
		return store.ErrBotUnsupported
	}
	if v.BotID == uuid.Nil || v.Version < 1 || strings.TrimSpace(v.Charter) == "" {
		return fmt.Errorf("%w: missing bot/version/charter", store.ErrBotInvalid)
	}
	if v.Persona.Timezone == "" {
		return fmt.Errorf("%w: missing timezone", store.ErrBotInvalid)
	}
	if _, err := time.LoadLocation(v.Persona.Timezone); err != nil {
		return fmt.Errorf("%w: timezone", store.ErrBotInvalid)
	}
	switch v.Autonomy.Level {
	case execution.AutonomyAsk, execution.AutonomySupervised, execution.AutonomyReversible, execution.AutonomyScoped:
	default:
		return fmt.Errorf("%w: autonomy", store.ErrBotInvalid)
	}
	if v.Budget.MaxRuntimeSeconds <= 0 || v.Autonomy.MaxSpendPerRunMinor < 0 || v.Autonomy.MaxSpendPerMonthMinor < 0 {
		return fmt.Errorf("%w: budget", store.ErrBotInvalid)
	}
	for _, limit := range []*int64{v.Budget.MaxTokens, v.Budget.MaxToolCalls, v.Budget.MaxRunsPerDay, v.Budget.MaxModelCostMicros} {
		if limit != nil && *limit < 0 {
			return fmt.Errorf("%w: negative budget", store.ErrBotInvalid)
		}
	}
	switch v.Delivery.Notify {
	case store.NotifyNever, store.NotifyOnChange, store.NotifyOnDecision, store.NotifyAlways:
	default:
		return fmt.Errorf("%w: notification policy", store.ErrBotInvalid)
	}
	return nil
}

type RunRequest struct {
	Mandate       *MandateExecution
	Bot           store.Bot
	Version       store.BotVersion
	Run           store.BotRun
	Principal     execution.Principal
	Authorization execution.BotTurnAuthorizer
	Policy        *execution.RunPolicy
	Budget        execution.RunBudget
	Goals         []store.BotGoal
	Instructions  string
	Attachments   []store.ChatFile
	Now           time.Time
	Interactive   bool
}

// RuntimeEvent retains the existing Chat wire payload; Sequence is monotonic per
// logical turn. Persist-before-publish and replay cursors are adapter obligations.
type RuntimeEvent struct {
	SessionID uuid.UUID       `json:"session_id"`
	RunID     store.RunID     `json:"run_id"`
	TurnID    uuid.UUID       `json:"turn_id"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}
type EventSink func(context.Context, RuntimeEvent) error

type RunResult struct {
	Status     store.RunStatus
	Report     store.BotReport
	Usage      store.BotUsage
	Checkpoint json.RawMessage
	Todos      []store.BotTodo
	Failure    *RunError
}

// Runtime methods return on checkpoint or completion, not viewer disconnection.
// Cancel must fence before the next action; it cannot undo a committed effect.
type Runtime interface {
	Start(context.Context, RunRequest, EventSink) (RunResult, error)
	Resume(context.Context, RunRequest, EventSink) (RunResult, error)
	Cancel(context.Context, store.BotLease) error
}

type RunErrorClass string

const (
	ErrorTransient   RunErrorClass = "transient"
	ErrorPermanent   RunErrorClass = "permanent"
	ErrorPermission  RunErrorClass = "permission"
	ErrorRateLimited RunErrorClass = "rate_limited"
	ErrorBlocked     RunErrorClass = "blocked"
	ErrorBudget      RunErrorClass = "budget"
	ErrorInterrupted RunErrorClass = "interrupted"
)

type RunError struct {
	Class              RunErrorClass
	Message            string
	TerminalForAttempt bool
	UncertainEffect    bool
	Cause              error
}

func (e *RunError) Error() string { return e.Message }
func (e *RunError) Unwrap() error { return e.Cause }

type HomeRequest struct {
	BotID    store.BotID
	Scope    store.BotScope
	Template string
}
type HomeRunBinding struct {
	Home              store.BotHomeBinding
	PhysicalSessionID uuid.UUID
	Lease             store.BotLease
}

// HomeManager owns storage, not a long-lived physical sandbox. ReadArtifact must
// authorize scope, confine paths and work after the originating run is destroyed.
type HomeManager interface {
	Ensure(context.Context, HomeRequest) (store.BotHomeBinding, error)
	BindRun(context.Context, HomeRunBinding) error
	ReadArtifact(context.Context, store.BotHomeBinding, store.BotArtifact) (io.ReadCloser, error)
	Delete(context.Context, store.BotHomeBinding) error
}
type ResolvedModel struct {
	Provider string
	Model    string
	LLM      model.LLM
}
type ModelResolver interface {
	Resolve(context.Context, uuid.UUID) (ResolvedModel, error)
}
type Clock interface{ Now() time.Time }
type NotificationRequest struct {
	ID         uuid.UUID
	BotID      store.BotID
	RunID      store.RunID
	Decision   store.NotificationDecision
	Reason     string
	Target     store.BotChannelBinding
	Body       string
	NotBefore  *time.Time
	DeferCount int
}
type NotificationReceipt struct {
	ProviderID  string
	DeliveredAt *time.Time
	Uncertain   bool
}

// Send must not broadcast or blindly repeat an uncertain external delivery.
type Notifier interface {
	Send(context.Context, NotificationRequest) (NotificationReceipt, error)
}
