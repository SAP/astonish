package store

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"time"
)

const BotOperationViewSchemaVersion = 1
const BotOperationKey = "bot_operation"

type BotOperationPresentation string

const (
	BotOperationPresentationInternal     BotOperationPresentation = "internal"
	BotOperationPresentationConversation BotOperationPresentation = "conversation"
)

func isScheduleOperation(op BotControlOperation) bool {
	switch op {
	case BotControlScheduleCreate, BotControlScheduleEdit, BotControlSchedulePause, BotControlScheduleRemove:
		return true
	}
	return false
}

type BotMandateSummary struct {
	Available bool `json:"available"`
}
type BotOperationFieldView struct {
	Caption string `json:"caption"`
	Value   string `json:"value"`
}
type BotOperationExceptionView struct {
	Fields        []BotOperationFieldView `json:"fields,omitempty"`
	OperationID   uuid.UUID               `json:"operation_id"`
	PayloadDigest string                  `json:"payload_digest"`
	ExpiresAt     time.Time               `json:"expires_at"`
}
type BotOperationScheduleView struct {
	TriggerID  uuid.UUID  `json:"trigger_id"`
	ScheduleID uuid.UUID  `json:"schedule_id"`
	Version    int64      `json:"version"`
	Enabled    bool       `json:"enabled"`
	Removed    bool       `json:"removed"`
	NextRun    *time.Time `json:"next_run,omitempty"`
}

// BotOperationView is a closed presentation allowlist, never execution authority.
type BotOperationView struct {
	Presentation      BotOperationPresentation   `json:"presentation,omitempty"`
	SchemaVersion     int                        `json:"schema_version"`
	BotID             uuid.UUID                  `json:"bot_id"`
	HomeSessionID     uuid.UUID                  `json:"home_session_id"`
	OperationID       uuid.UUID                  `json:"operation_id"`
	Revision          int64                      `json:"revision"`
	StateSequence     int64                      `json:"state_sequence"`
	SourceRunID       uuid.UUID                  `json:"source_run_id,omitempty"`
	SourceUserEventID string                     `json:"source_user_event_id,omitempty"`
	Operation         BotControlOperation        `json:"operation"`
	Status            BotOperationStatus         `json:"status"`
	Decision          BotOperationPolicyDecision `json:"decision"`
	Summary           string                     `json:"summary"`
	Blocker           string                     `json:"blocker,omitempty"`
	VerifiedSchedule  *BotOperationScheduleView  `json:"verified_schedule,omitempty"`
	Exception         *BotOperationExceptionView `json:"exception,omitempty"`
}

func (v BotOperationView) Validate() error {
	if v.Presentation != "" {
		if !isScheduleOperation(v.Operation) || v.Decision == BotOperationRequireAuthorization {
			return ErrBotInvalid
		}
		switch v.Presentation {
		case BotOperationPresentationInternal:
			if v.Status != BotOperationPending && v.Status != BotOperationApplying {
				return ErrBotInvalid
			}
		case BotOperationPresentationConversation:
			if v.Status == BotOperationPending || v.Status == BotOperationApplying || (v.Status == BotOperationApplied && v.VerifiedSchedule == nil) {
				return ErrBotInvalid
			}
		default:
			return ErrBotInvalid
		}
	}
	if v.SchemaVersion != BotOperationViewSchemaVersion || v.BotID == uuid.Nil || v.HomeSessionID == uuid.Nil || v.OperationID == uuid.Nil || v.Revision < 1 || v.StateSequence < 1 || !v.Status.Valid() || !v.Decision.Valid() {
		return ErrBotInvalid
	}
	if v.VerifiedSchedule != nil && v.Status != BotOperationApplied {
		return ErrBotInvalid
	}
	return nil
}
func ProjectBotOperation(b Bot, o BotOperation, sequence int64) (BotOperationView, error) {
	if o.Validate() != nil || o.MandateRef.BotID != b.ID {
		return BotOperationView{}, ErrBotInvalid
	}
	v := BotOperationView{SchemaVersion: 1, BotID: b.ID, HomeSessionID: b.HomeSessionID, OperationID: o.ID, Revision: o.Revision, StateSequence: sequence, Operation: o.Payload.Operation, Status: o.Status, Decision: o.Decision}
	if o.Provenance != nil {
		v.SourceRunID = o.Provenance.SourceRunID
		v.SourceUserEventID = o.Provenance.UserEventID
	}
	switch o.Status {
	case BotOperationPending:
		v.Summary = "Change queued."
	case BotOperationApplying:
		v.Summary = "Applying change."
	case BotOperationApplied:
		v.Summary = "Change verified and applied."
	case BotOperationBlocked:
		v.Summary = "Change needs attention."
		v.Blocker = "The change cannot proceed under the current authorization."
	case BotOperationFailed:
		v.Summary = "Change could not be completed."
		v.Blocker = "The change was not verified."
	case BotOperationCancelled:
		v.Summary = "Change cancelled."
	}
	if o.Status == BotOperationApplied && o.Receipt != nil && o.Receipt.Schedule != nil {
		s := o.Receipt.Schedule
		if o.Provenance == nil || s.SourceRunID != o.Provenance.SourceRunID || s.UserEventID != o.Provenance.UserEventID || s.SourceDigest != o.Provenance.SourceDigest {
			return BotOperationView{}, ErrBotInvalid
		}
		v.VerifiedSchedule = &BotOperationScheduleView{TriggerID: s.TriggerID, ScheduleID: s.ScheduleID, Version: s.Version, Enabled: s.Enabled, Removed: s.Removed}
		if s.NextRun != nil {
			n := *s.NextRun
			v.VerifiedSchedule.NextRun = &n
		}
	}
	if o.Exception != nil && o.Status == BotOperationBlocked && o.Decision == BotOperationRequireAuthorization {
		e := o.Exception
		if e.OperationID != o.ID || e.MandateRef != o.MandateRef || o.Provenance == nil || e.PayloadDigest != o.Provenance.PayloadDigest {
			return BotOperationView{}, ErrBotInvalid
		}
		v.Exception = &BotOperationExceptionView{OperationID: e.OperationID, PayloadDigest: e.PayloadDigest, ExpiresAt: e.ExpiresAt, Fields: botOperationFields(o.Payload)}
	}
	if isScheduleOperation(o.Payload.Operation) && o.Decision != BotOperationRequireAuthorization {
		switch o.Status {
		case BotOperationPending, BotOperationApplying:
			v.Presentation = BotOperationPresentationInternal
		case BotOperationApplied:
			if v.VerifiedSchedule != nil {
				v.Presentation = BotOperationPresentationConversation
				v.Summary = scheduleOperationSummary(o.Payload, *v.VerifiedSchedule)
			}
		case BotOperationFailed, BotOperationBlocked:
			v.Presentation = BotOperationPresentationConversation
			v.Summary = "I couldn't complete the schedule change. No schedule change was verified."
		case BotOperationCancelled:
			v.Presentation = BotOperationPresentationConversation
			v.Summary = "The schedule change was cancelled."
		}
	}
	return v, v.Validate()
}

func scheduleOperationSummary(p BotControlPayload, s BotOperationScheduleView) string {
	if s.Removed {
		return "I've removed the schedule."
	}
	if !s.Enabled {
		return "I've paused the schedule."
	}
	verb := "updated"
	if p.Operation == BotControlScheduleCreate {
		verb = "created"
	}
	summary := "I've " + verb + " the schedule."
	if p.Schedule != nil && p.Schedule.Trigger.Cron == "* * * * *" {
		summary += " It runs every minute."
	}
	if s.NextRun != nil {
		summary += " Next run: " + s.NextRun.UTC().Format("2006-01-02 15:04 UTC") + "."
	}
	return summary
}
func NewBotOperationEvent(v BotOperationView, at time.Time) (*session.Event, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &session.Event{ID: uuid.NewSHA1(v.OperationID, []byte(fmt.Sprintf("bot-operation:%d", v.Revision))).String(), Author: "bot_runtime", Timestamp: at, Actions: session.EventActions{StateDelta: map[string]any{BotOperationKey: v}}}, nil
}

type botOperationNotifierKey struct{}
type BotOperationNotifier func(context.Context, BotOperationView)

func WithBotOperationNotifier(ctx context.Context, notify BotOperationNotifier) context.Context {
	return context.WithValue(ctx, botOperationNotifierKey{}, notify)
}
func BotOperationNotifierFromContext(ctx context.Context) BotOperationNotifier {
	n, _ := ctx.Value(botOperationNotifierKey{}).(BotOperationNotifier)
	return n
}

// Closed display allowlist: never marshal payloads or authority into a card.
func botOperationFields(p BotControlPayload) []BotOperationFieldView {
	fields := []BotOperationFieldView{{Caption: "Change", Value: string(p.Operation)}}
	add := func(c, v string) {
		if v != "" {
			fields = append(fields, BotOperationFieldView{Caption: c, Value: v})
		}
	}
	switch p.Operation {
	case BotControlGoalCreate, BotControlGoalEdit:
		if p.Goal != nil {
			add("Goal", p.Goal.Goal.Title)
			add("Description", p.Goal.Goal.Description)
			add("Completion criteria", p.Goal.Goal.CompletionCriteria)
		}
	case BotControlMemoryRemember, BotControlMemoryCorrect, BotControlMemoryForget:
		if p.Memory != nil {
			add("Memory", p.Memory.MemoryID)
			add("Content", p.Memory.Content)
			add("Category", p.Memory.Category)
		}
	case BotControlScheduleCreate, BotControlScheduleEdit, BotControlSchedulePause, BotControlScheduleRemove:
		if p.Schedule != nil {
			add("Schedule", p.Schedule.Trigger.ID.String())
			add("Instructions", p.Schedule.Trigger.Instructions)
			add("Cron", p.Schedule.Trigger.Cron)
			add("Timezone", p.Schedule.Trigger.Timezone)
		}
	case BotControlTaskOnceOperation:
		if p.TaskOnce != nil {
			add("Instructions", p.TaskOnce.Instructions)
		}
	case BotControlResponsibility:
		if p.Responsibility != nil {
			add("Responsibility", *p.Responsibility)
		}
	case BotControlResumeTasks:
		if p.ResumeTargets != nil {
			for _, id := range p.ResumeTargets.ScheduleIDs {
				add("Schedule", id.String())
			}
		}
	}
	return fields
}
