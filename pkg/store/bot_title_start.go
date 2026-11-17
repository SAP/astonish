package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/question"
	"github.com/google/uuid"
)

// BotTitleStartStore is optional: legacy CreateDraft and imports never start work.
// Implementations commit the draft, home, title message, run and dispatch together.
type BotTitleStartStore interface {
	CreateDraftAndStart(context.Context, BotCreateRequest) (*BotTitleStartReceipt, error)
}

type BotTitleStartReceipt struct {
	Bot
	Run           BotRun `json:"run"`
	UserEventID   string `json:"user_event_id"`
	HistoryCursor int64  `json:"history_cursor"`
}

type BotTitleConfirmationState string

const (
	BotTitleConfirmationVersion                                 = 1
	BotTitleConfirmationNeedsQuestion BotTitleConfirmationState = "needs_question"
	BotTitleConfirmationWaiting       BotTitleConfirmationState = "waiting"
	BotTitleConfirmationConfirmed     BotTitleConfirmationState = "confirmed"
)

// BotTitleConfirmation binds task intent, never authority to execute an action.
// The original title and source identity are immutable through all transitions.
type BotTitleConfirmation struct {
	Version             int                       `json:"version"`
	State               BotTitleConfirmationState `json:"state"`
	BotID               BotID                     `json:"bot_id"`
	RunID               RunID                     `json:"run_id"`
	OwnerID             uuid.UUID                 `json:"owner_id"`
	OriginalTitle       string                    `json:"original_title"`
	UserEventID         string                    `json:"user_event_id"`
	ApprovalID          uuid.UUID                 `json:"approval_id,omitempty"`
	QuestionID          string                    `json:"question_id,omitempty"`
	Question            *BotInteraction           `json:"question,omitempty"`
	ConfirmedAssignment string                    `json:"confirmed_assignment,omitempty"`
	AssignmentDigest    string                    `json:"assignment_digest,omitempty"`
	DecidedBy           uuid.UUID                 `json:"decided_by,omitempty"`
	DecidedAt           time.Time                 `json:"decided_at,omitempty"`
}

func BotTitleAssignmentDigest(assignment string) string {
	sum := sha256.Sum256([]byte(assignment))
	return hex.EncodeToString(sum[:])
}

func (c *BotTitleConfirmation) Clone() *BotTitleConfirmation {
	if c == nil {
		return nil
	}
	out := *c
	if c.Question != nil {
		q := *c.Question
		q.Payload = append(json.RawMessage(nil), q.Payload...)
		out.Question = &q
	}
	return &out
}

// ValidateBotTitleQuestion adds title-only constraints without changing normal questions.
func ValidateBotTitleQuestion(i *BotInteraction) error {
	if i.Validate() != nil {
		return ErrBotInvalid
	}
	var q question.Question
	if json.Unmarshal(i.Payload, &q) != nil || q.Kind != question.Select || q.AllowCustomAnswer == nil || !*q.AllowCustomAnswer || len(q.Options) < 3 || len(q.Options) > 4 || question.ValidateText(q.Prompt) != nil {
		return ErrBotInvalid
	}
	labels, ids := map[string]bool{}, map[string]bool{}
	for _, o := range q.Options {
		label, id := strings.ToLower(strings.TrimSpace(o.Label)), strings.TrimSpace(o.ID)
		if question.ValidateText(o.Label) != nil || len(o.Label) > 2048 || len(id) > 128 || id != o.ID || ids[id] || labels[label] {
			return ErrBotInvalid
		}
		labels[label], ids[id] = true, true
	}
	return nil
}

func (c *BotTitleConfirmation) Validate() error {
	if c == nil {
		return nil
	} // historical run
	if c.Version != BotTitleConfirmationVersion || c.BotID == uuid.Nil || c.RunID == uuid.Nil || c.OwnerID == uuid.Nil || question.ValidateText(c.OriginalTitle) != nil || strings.TrimSpace(c.UserEventID) == "" {
		return ErrBotInvalid
	}
	answered := c.ConfirmedAssignment != "" || c.AssignmentDigest != "" || c.DecidedBy != uuid.Nil || !c.DecidedAt.IsZero()
	switch c.State {
	case BotTitleConfirmationNeedsQuestion:
		if c.ApprovalID != uuid.Nil || c.QuestionID != "" || c.Question != nil || answered {
			return ErrBotInvalid
		}
	case BotTitleConfirmationWaiting, BotTitleConfirmationConfirmed:
		if c.ApprovalID == uuid.Nil || c.Question == nil || c.QuestionID != c.Question.QuestionID || ValidateBotTitleQuestion(c.Question) != nil {
			return ErrBotInvalid
		}
		if c.State == BotTitleConfirmationWaiting {
			if answered {
				return ErrBotInvalid
			}
		} else if question.ValidateText(c.ConfirmedAssignment) != nil || c.AssignmentDigest != BotTitleAssignmentDigest(c.ConfirmedAssignment) || c.DecidedBy != c.OwnerID || c.DecidedAt.IsZero() {
			return ErrBotInvalid
		}
	default:
		return ErrBotInvalid
	}
	return nil
}

func (c *BotTitleConfirmation) ValidateBinding(run BotRun, owner uuid.UUID) error {
	if c == nil {
		return nil
	}
	if c.Validate() != nil || c.BotID != run.BotID || c.RunID != run.ID || c.OwnerID != owner || c.UserEventID != run.UserEventID {
		return ErrBotInvalid
	}
	return nil
}

// WithQuestion returns a new waiting record, preserving the exact persisted card.
func (c *BotTitleConfirmation) WithQuestion(a BotApproval) (*BotTitleConfirmation, error) {
	if c == nil || c.Validate() != nil || c.State != BotTitleConfirmationNeedsQuestion || a.ID == uuid.Nil || a.BotID != c.BotID || a.RunID != c.RunID || a.Status != ApprovalPending || ValidateBotTitleQuestion(a.Interaction) != nil {
		return nil, ErrBotInvalid
	}
	next := c.Clone()
	next.State = BotTitleConfirmationWaiting
	next.ApprovalID = a.ID
	next.QuestionID = a.Interaction.QuestionID
	q := *a.Interaction
	q.Payload = append(json.RawMessage(nil), q.Payload...)
	next.Question = &q
	return next, next.Validate()
}

// WithAnswer consumes an authenticated, correlated approval decision, not a grant.
// Selected IDs resolve to their displayed assignment; custom text remains verbatim.
func (c *BotTitleConfirmation) WithAnswer(a BotApproval) (*BotTitleConfirmation, error) {
	if c == nil || c.Validate() != nil || c.State != BotTitleConfirmationWaiting || a.ID != c.ApprovalID || a.BotID != c.BotID || a.RunID != c.RunID || a.Status != ApprovalApproved || a.DecidedBy == nil || *a.DecidedBy != c.OwnerID || a.DecidedAt == nil || a.DecidedAt.IsZero() || !a.ExpiresAt.After(*a.DecidedAt) || a.DecidedAt.Before(a.CreatedAt) || !reflect.DeepEqual(a.Interaction, c.Question) || c.Question.ValidateResponse(a.Response) != nil {
		return nil, ErrBotInvalid
	}
	assignment := a.Response.Answer
	var q question.Question
	if json.Unmarshal(c.Question.Payload, &q) != nil {
		return nil, ErrBotInvalid
	}
	for _, o := range q.Options {
		if o.ID == assignment {
			assignment = o.Label
			break
		}
	}
	next := c.Clone()
	next.State = BotTitleConfirmationConfirmed
	next.ConfirmedAssignment = assignment
	next.AssignmentDigest = BotTitleAssignmentDigest(assignment)
	next.DecidedBy = *a.DecidedBy
	next.DecidedAt = *a.DecidedAt
	return next, next.Validate()
}
