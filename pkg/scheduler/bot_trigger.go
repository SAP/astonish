package scheduler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BotTriggerRequest describes an existing row's due occurrence, not permission to
// activate or run a Bot. No instructions, credentials, policy or principal are
// accepted from the job payload. Trusted composition must resolve these afresh.
type BotTriggerRequest struct {
	JobID          string
	BotID          uuid.UUID
	Version        int64
	TriggerID      uuid.UUID
	OccurrenceAt   time.Time
	IdempotencyKey string
	OwnerID        string
	Scope          string
	TeamSlug       string
}

type BotAdmissionStatus string

const (
	BotAdmissionAdmitted  BotAdmissionStatus = "admitted"
	BotAdmissionDuplicate BotAdmissionStatus = "duplicate"
	BotAdmissionSkipped   BotAdmissionStatus = "skipped"
)

// BotAdmission is an admission receipt, never an execution result/report.
// Admitted and duplicate require the durable run ID; skipped requires no run.
type BotScheduleAcknowledgement string

const BotScheduleAcknowledged BotScheduleAcknowledgement = "bot_managed"

type botAcknowledgementKey struct{}

// WithBotAcknowledgement carries a typed receipt without changing ExecuteFunc.
func WithBotAcknowledgement(ctx context.Context) (context.Context, *BotScheduleAcknowledgement) {
	if ctx == nil {
		ctx = context.Background()
	}
	ack := new(BotScheduleAcknowledgement)
	return context.WithValue(ctx, botAcknowledgementKey{}, ack), ack
}

type BotAdmission struct {
	Acknowledgement BotScheduleAcknowledgement
	Status          BotAdmissionStatus
	RunID           uuid.UUID
}

// BotTriggerFunc must atomically deduplicate by IdempotencyKey, persist admission
// and dispatch intent before returning, and recheck the live Bot/version/trigger
// binding, scope authorization, lifecycle and budgets. Caller-provided scope and
// owner fields are hints, NOT authenticated authority. Duplicate must return the
// original run. Do not wire this directly to an untrusted HTTP activation path.
// The callback receives the normal scheduler timeout; execution belongs to the
// independently owned Bot worker. No production callback is installed here.
type BotTriggerFunc func(context.Context, BotTriggerRequest) (BotAdmission, error)

func botTriggerRequest(job *Job, now time.Time) (BotTriggerRequest, error) {
	var req BotTriggerRequest
	if job == nil || job.Mode != ModeBotTrigger || !job.Enabled || strings.TrimSpace(job.ID) == "" {
		return req, fmt.Errorf("invalid Bot-trigger job")
	}
	if job.NextRun == nil || job.NextRun.IsZero() || job.NextRun.After(now) {
		return req, fmt.Errorf("Bot-trigger job requires a persisted due occurrence")
	}
	botID, err := uuid.Parse(job.Payload.BotID)
	if err != nil || botID == uuid.Nil {
		return req, fmt.Errorf("Bot-trigger job requires a valid Bot ID")
	}
	triggerID, err := uuid.Parse(job.Payload.BotTriggerID)
	if err != nil || triggerID == uuid.Nil || job.Payload.BotVersion < 1 {
		return req, fmt.Errorf("Bot-trigger job requires a valid trigger and version")
	}
	if job.Payload.Flow != "" || job.Payload.Instructions != "" || len(job.Payload.Params) != 0 {
		return req, fmt.Errorf("Bot-trigger job cannot supply executable payload")
	}
	occurrence := job.NextRun.UTC()
	// Canonical UUIDs and UTC timestamps avoid timezone/serialization-dependent
	// identities. Do not use wall-clock dispatch time, LastRun or a random UUID.
	// Deliberately exclude row ID: duplicate rows for one trigger must deduplicate.
	identity := fmt.Sprintf("%s/%d/%s/%s", botID, job.Payload.BotVersion, triggerID, occurrence.Format(time.RFC3339Nano))
	key := fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
	return BotTriggerRequest{
		JobID: job.ID, BotID: botID, Version: job.Payload.BotVersion,
		TriggerID: triggerID, OccurrenceAt: occurrence, IdempotencyKey: key,
		OwnerID: job.OwnerID, Scope: job.Scope, TeamSlug: job.TeamSlug,
	}, nil
}

func (e *Executor) executeBotTrigger(ctx context.Context, job *Job) (string, error) {
	if e.BotTrigger == nil {
		return "", fmt.Errorf("Bot-trigger admission callback not configured")
	}
	req, err := botTriggerRequest(job, time.Now())
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	receipt, err := e.BotTrigger(ctx, req)
	if err != nil {
		var rejected *store.BotScheduleRejected
		if errors.As(err, &rejected) {
			// Managed failures never fall back to an outer whole-row write, even
			// when persistence failed or the CAS lost to a concurrent edit.
			if ack, ok := ctx.Value(botAcknowledgementKey{}).(*BotScheduleAcknowledgement); ok {
				*ack = BotScheduleAcknowledged
			}
		}
		return "", fmt.Errorf("Bot-trigger admission: %w", err)
	}
	switch receipt.Status {
	case BotAdmissionAdmitted, BotAdmissionDuplicate:
		if receipt.RunID == uuid.Nil {
			return "", fmt.Errorf("Bot-trigger admission missing durable run ID")
		}
	case BotAdmissionSkipped:
		if receipt.RunID != uuid.Nil {
			return "", fmt.Errorf("skipped Bot-trigger admission contains a run ID")
		}
	default:
		return "", fmt.Errorf("invalid Bot-trigger admission receipt")
	}
	if receipt.Acknowledgement == BotScheduleAcknowledged {
		if ack, ok := ctx.Value(botAcknowledgementKey{}).(*BotScheduleAcknowledgement); ok {
			*ack = BotScheduleAcknowledged
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Success means admission handled, not Bot run success. Never synthesize a
	// successful report or deliver a placeholder through legacy notifications.
	return "", nil
}

// WithoutBotTriggerDelivery suppresses ALL legacy delivery for Bot rows,
// including admission errors. Install around the delivery callback wherever
// Executor.Execute is composed with a scheduler (including multi-tenant lanes).
// Routine/adaptive/fleet delivery behavior is otherwise unchanged. Bot workers
// own their durable reporting/notification pipeline.
func WithoutBotTriggerDelivery(deliver DeliverFunc) DeliverFunc {
	return func(ctx context.Context, job *Job, result string, err error) error {
		if job.Mode == ModeBotTrigger || deliver == nil {
			return nil
		}
		return deliver(ctx, job, result, err)
	}
}
