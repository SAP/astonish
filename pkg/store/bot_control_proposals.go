package store

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"time"
)

type BotControlOperation string

const (
	BotControlResponsibility         BotControlOperation = "responsibility"
	BotControlPersona                BotControlOperation = "persona"
	BotControlConfigurationOperation BotControlOperation = "configuration"
	BotControlGoalCreate             BotControlOperation = "goal_create"
	BotControlGoalEdit               BotControlOperation = "goal_edit"
	BotControlMemoryRemember         BotControlOperation = "memory_remember"
	BotControlMemoryCorrect          BotControlOperation = "memory_correct"
	BotControlMemoryForget           BotControlOperation = "memory_forget"
	BotControlScheduleCreate         BotControlOperation = "schedule_create"
	BotControlScheduleEdit           BotControlOperation = "schedule_edit"
	BotControlSchedulePause          BotControlOperation = "schedule_pause"
	BotControlScheduleRemove         BotControlOperation = "schedule_remove"
	BotControlTaskOnceOperation      BotControlOperation = "task_once"
	BotControlPriorityPause          BotControlOperation = "priority_pause"
	BotControlResumeTasks            BotControlOperation = "resume_tasks"
	BotControlActivate               BotControlOperation = "activate"
	BotControlResume                 BotControlOperation = "resume"
)

type BotControlProposalStatus string

const (
	BotControlProposed BotControlProposalStatus = "proposed"
	BotControlApplied  BotControlProposalStatus = "applied"
	BotControlRejected BotControlProposalStatus = "rejected"
	BotControlExpired  BotControlProposalStatus = "expired"
	BotControlStale    BotControlProposalStatus = "stale"
)

type BotControlConfiguration struct {
	Pause    bool           `json:"pause,omitempty"`
	Autonomy BotAutonomy    `json:"autonomy"`
	Budget   BudgetSpec     `json:"budget"`
	Delivery DeliveryPolicy `json:"delivery"`
}
type BotControlGoal struct {
	Goal             BotGoal `json:"goal"`
	ExpectedRevision int64   `json:"expected_revision"`
}
type BotControlMemory struct {
	ExpectedFingerprint string `json:"expected_fingerprint,omitempty"`
	MemoryID            string `json:"memory_id,omitempty"`
	Content             string `json:"content,omitempty"`
	Category            string `json:"category,omitempty"`
	ExpectedRevision    int64  `json:"expected_revision"`
}
type BotControlSchedule struct {
	Trigger          TriggerSpec `json:"trigger"`
	ExpectedRevision int64       `json:"expected_revision"`
}
type BotControlResumeTargets struct {
	ScheduleIDs []uuid.UUID `json:"schedule_ids"`
}

type BotControlTaskOnce struct {
	SkillID      *uuid.UUID `json:"skill_id,omitempty"`
	Invocation   string     `json:"invocation,omitempty"`
	Instructions string     `json:"instructions"`
}

// BotControlPayload is a closed union. Prose cannot supply authority or a decision.
type BotControlPayload struct {
	Operation      BotControlOperation      `json:"operation"`
	Reason         string                   `json:"reason"`
	Responsibility *string                  `json:"responsibility,omitempty"`
	Persona        *BotPersona              `json:"persona,omitempty"`
	Configuration  *BotControlConfiguration `json:"configuration,omitempty"`
	Goal           *BotControlGoal          `json:"goal,omitempty"`
	Memory         *BotControlMemory        `json:"memory,omitempty"`
	Schedule       *BotControlSchedule      `json:"schedule,omitempty"`
	ResumeTargets  *BotControlResumeTargets `json:"resume_targets,omitempty"`
	TaskOnce       *BotControlTaskOnce      `json:"task_once,omitempty"`
}

func (p BotControlPayload) Validate() error {
	n := 0
	for _, present := range []bool{p.Responsibility != nil, p.Persona != nil, p.Configuration != nil, p.Goal != nil, p.Memory != nil, p.Schedule != nil, p.TaskOnce != nil, p.ResumeTargets != nil} {
		if present {
			n++
		}
	}
	if strings.TrimSpace(p.Reason) == "" {
		return ErrBotInvalid
	}
	valid := false
	switch p.Operation {
	case BotControlResponsibility:
		valid = p.Responsibility != nil && strings.TrimSpace(*p.Responsibility) != ""
	case BotControlPersona:
		valid = p.Persona != nil
	case BotControlConfigurationOperation:
		valid = p.Configuration != nil
	case BotControlGoalCreate, BotControlGoalEdit:
		valid = p.Goal != nil
	case BotControlMemoryRemember, BotControlMemoryCorrect, BotControlMemoryForget:
		valid = p.Memory != nil
	case BotControlScheduleCreate, BotControlScheduleEdit, BotControlSchedulePause, BotControlScheduleRemove:
		valid = p.Schedule != nil
		if p.Schedule != nil {
			if err := ValidateSkillSelection(p.Schedule.Trigger.SkillID, p.Schedule.Trigger.Invocation); err != nil {
				return err
			}
		}
		// A schedule is not an external delivery configuration. Reject transport
		// bindings before durable proposal capture, not only when applying it.
		if p.Schedule != nil && p.Schedule.Trigger.ChannelBinding != nil {
			return ErrBotForbidden
		}
	case BotControlTaskOnceOperation:
		valid = p.TaskOnce != nil && strings.TrimSpace(p.TaskOnce.Instructions) != ""
		if p.TaskOnce != nil {
			if err := ValidateSkillSelection(p.TaskOnce.SkillID, p.TaskOnce.Invocation); err != nil {
				return err
			}
			valid = valid || p.TaskOnce.SkillID != nil
		}
	case BotControlResumeTasks:
		if p.ResumeTargets == nil || len(p.ResumeTargets.ScheduleIDs) == 0 || len(p.ResumeTargets.ScheduleIDs) > 32 {
			return ErrBotInvalid
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range p.ResumeTargets.ScheduleIDs {
			if id == uuid.Nil || seen[id] {
				return ErrBotInvalid
			}
			seen[id] = true
		}
		valid = true
	case BotControlPriorityPause, BotControlActivate, BotControlResume:
		if n == 0 {
			return nil
		}
	}
	if !valid || n != 1 {
		return ErrBotInvalid
	}
	return nil
}

type BotControlProposal struct {
	ID                    uuid.UUID                `json:"id"`
	BotID                 BotID                    `json:"bot_id"`
	SourceRunID           RunID                    `json:"source_run_id"`
	SourceUserID          uuid.UUID                `json:"source_user_id"`
	Version               int64                    `json:"version"`
	ExpectedRevision      int64                    `json:"expected_revision"`
	Digest                string                   `json:"digest"`
	Payload               BotControlPayload        `json:"payload"`
	Status                BotControlProposalStatus `json:"status"`
	CreatedAt             time.Time                `json:"created_at"`
	ExpiresAt             time.Time                `json:"expires_at"`
	DecidedAt             *time.Time               `json:"decided_at,omitempty"`
	DecidedBy             *uuid.UUID               `json:"decided_by,omitempty"`
	ReconciliationPending bool                     `json:"reconciliation_pending"`
}
type BotControlProposalRequest struct {
	Lease          BotLease          `json:"-"`
	IdempotencyKey string            `json:"idempotency_key"`
	ExpiresAt      time.Time         `json:"expires_at"`
	Payload        BotControlPayload `json:"payload"`
}
type BotControlProposalDecision struct {
	BotID      BotID     `json:"-"`
	ProposalID uuid.UUID `json:"proposal_id"`
	Digest     string    `json:"digest"`
}
type BotControlProposalStore interface {
	ProposeControl(context.Context, BotControlProposalRequest) (*BotControlProposal, error)
	ReadControlProposal(context.Context, BotID, uuid.UUID) (*BotControlProposal, error)
	ListControlProposals(context.Context, BotID) ([]BotControlProposal, error)
	ConfirmControlProposal(context.Context, BotControlProposalDecision) (*BotControlProposal, error)
	RejectControlProposal(context.Context, BotControlProposalDecision) (*BotControlProposal, error)
}
