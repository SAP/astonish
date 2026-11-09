package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type BotEvidenceKind string

const (
	BotEvidenceObservation       BotEvidenceKind = "observation"
	BotEvidenceOwnerConfirmation BotEvidenceKind = "owner_confirmation"
	BotEvidenceInference         BotEvidenceKind = "inference"
)

type BotEvidence struct {
	Kind       BotEvidenceKind `json:"kind"`
	Summary    string          `json:"summary"`
	Source     string          `json:"source"`
	ObservedAt time.Time       `json:"observed_at"`
	ActorID    uuid.UUID       `json:"actor_id"`
}

func (e BotEvidence) ValidateCompletion() error {
	if e.Kind != BotEvidenceObservation && e.Kind != BotEvidenceOwnerConfirmation {
		return fmt.Errorf("%w: completion requires observation or owner confirmation", ErrBotInvalid)
	}
	if strings.TrimSpace(e.Summary) == "" || strings.TrimSpace(e.Source) == "" || e.ObservedAt.IsZero() || e.ActorID == uuid.Nil {
		return fmt.Errorf("%w: incomplete completion evidence", ErrBotInvalid)
	}
	return nil
}

type BotGoalStatus string

const (
	BotGoalActive    BotGoalStatus = "active"
	BotGoalCompleted BotGoalStatus = "completed"
)

type BotTrackedStatus string

const (
	BotTrackedOpen      BotTrackedStatus = "open"
	BotTrackedBlocked   BotTrackedStatus = "blocked"
	BotTrackedCompleted BotTrackedStatus = "completed"
	BotTrackedCancelled BotTrackedStatus = "cancelled"
)

type BotGoal struct {
	ID                 uuid.UUID     `json:"id"`
	BotID              BotID         `json:"bot_id"`
	Revision           int64         `json:"revision"`
	Title              string        `json:"title"`
	Description        string        `json:"description"`
	CompletionCriteria string        `json:"completion_criteria"`
	Status             BotGoalStatus `json:"status"`
	DueAt              *time.Time    `json:"due_at,omitempty"`
	WorkspacePath      string        `json:"workspace_path"`
	CompletedAt        *time.Time    `json:"completed_at,omitempty"`
	Evidence           []BotEvidence `json:"evidence"`
}
type BotTrackedItem struct {
	ID       uuid.UUID        `json:"id"`
	GoalID   uuid.UUID        `json:"goal_id"`
	Title    string           `json:"title"`
	Status   BotTrackedStatus `json:"status"`
	Reason   string           `json:"reason,omitempty"`
	DueAt    *time.Time       `json:"due_at,omitempty"`
	Evidence []BotEvidence    `json:"evidence"`
}
type BotTodoStatus string

const (
	BotTodoPending    BotTodoStatus = "pending"
	BotTodoInProgress BotTodoStatus = "in_progress"
	BotTodoCompleted  BotTodoStatus = "completed"
	BotTodoCancelled  BotTodoStatus = "cancelled"
	BotTodoBlocked    BotTodoStatus = "blocked"
	MaxBotTodos                     = 25
)

type BotTodo struct {
	ID           uuid.UUID     `json:"id"`
	Outcome      string        `json:"outcome"`
	Status       BotTodoStatus `json:"status"`
	UnblocksWhen string        `json:"unblocks_when,omitempty"`
	Evidence     []BotEvidence `json:"evidence,omitempty"`
}

// ValidateBotTodos validates working state, never promotes todos to memory/goals.
func ValidateBotTodos(todos []BotTodo, working bool) error {
	if len(todos) > MaxBotTodos {
		return fmt.Errorf("%w: too many run todos", ErrBotInvalid)
	}
	seen := make(map[uuid.UUID]bool, len(todos))
	active := 0
	for _, todo := range todos {
		if todo.ID == uuid.Nil || seen[todo.ID] || strings.TrimSpace(todo.Outcome) == "" {
			return fmt.Errorf("%w: missing or duplicate todo identity/outcome", ErrBotInvalid)
		}
		seen[todo.ID] = true
		switch todo.Status {
		case BotTodoInProgress:
			active++
		case BotTodoPending, BotTodoCancelled:
		case BotTodoBlocked:
			if strings.TrimSpace(todo.UnblocksWhen) == "" {
				return fmt.Errorf("%w: blocked todo needs unblock condition", ErrBotInvalid)
			}
		case BotTodoCompleted:
			if len(todo.Evidence) == 0 {
				return fmt.Errorf("%w: completed todo needs evidence", ErrBotInvalid)
			}
			for _, e := range todo.Evidence {
				if err := e.ValidateCompletion(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%w: unknown todo status", ErrBotInvalid)
		}
	}
	if active > 1 || working && active != 1 {
		return fmt.Errorf("%w: actionable work requires exactly one active todo", ErrBotInvalid)
	}
	return nil
}

type BotGoalUpdate struct {
	BotID            BotID
	Goal             BotGoal
	ExpectedRevision int64
}
type BotGoalCompletion struct {
	BotID            BotID
	GoalID           uuid.UUID
	ExpectedRevision int64
	Evidence         []BotEvidence
	ActorID          uuid.UUID
}

// Corrections identify the superseded source explicitly. Reconciliation across
// memory/goals/schedules uses durable intent; it is not a cross-store transaction.
type BotCorrection struct {
	// ExpectedRevision binds the correction to the reviewed Bot snapshot.
	ExpectedRevision int64       `json:"expected_revision"`
	ID               uuid.UUID   `json:"id"`
	BotID            BotID       `json:"bot_id"`
	SupersededSource string      `json:"superseded_source"`
	Replacement      string      `json:"replacement"`
	Evidence         BotEvidence `json:"evidence"`
	GoalIDs          []uuid.UUID `json:"goal_ids"`
}

// BotCorrectionReconciler is an optional capability of scoped Bot stores. The
// memory store must be the same router-resolved personal/team store; arbitrary
// wrappers and cross-scope stores fail closed. Only memory:<UUID> sources are
// executable. Other source kinds remain durable intents for explicit review.
// Reconciliation never grants permissions or changes the Bot's policy/version.
type BotCorrectionReconciler interface {
	ReconcileCorrection(context.Context, BotID, uuid.UUID, MemoryStore) error
}

type BotGoalStore interface {
	ListGoals(context.Context, BotID) ([]BotGoal, error)
	SaveGoal(context.Context, BotGoalUpdate) (*BotGoal, error)
	CompleteGoal(context.Context, BotGoalCompletion) (*BotGoal, error)
	SaveTrackedItem(context.Context, BotID, BotTrackedItem, int64) error
	ListTrackedItems(context.Context, BotID, uuid.UUID) ([]BotTrackedItem, error)
	Correct(context.Context, BotCorrection) error
}
