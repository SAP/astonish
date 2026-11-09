package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// NotificationPreferences is the machine-readable subset of the approved
// Delivery.Preferences document. Unknown prose/fields never grant send authority.
// DeferSeconds schedules one revalidation, not a repeating reminder.
type NotificationPreferences struct {
	MinChanged   int   `json:"min_changed,omitempty"`
	DeferSeconds int64 `json:"defer_seconds,omitempty"`
}

type NotificationEvaluation struct {
	Decision    store.NotificationDecision
	Reason      string
	AvailableAt time.Time
}

func EvaluateNotification(policy store.DeliveryPolicy, report store.BotReport, now time.Time, deferred bool) NotificationEvaluation {
	skip := func(reason string) NotificationEvaluation {
		return NotificationEvaluation{Decision: store.NotificationSkip, Reason: reason, AvailableAt: now}
	}
	if policy.Notify == store.NotifyNever {
		return skip("notifications disabled")
	}
	var prefs NotificationPreferences
	if strings.TrimSpace(policy.Preferences) != "" {
		dec := json.NewDecoder(strings.NewReader(policy.Preferences))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&prefs); err != nil {
			return skip("preferences require an approved structured policy")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return skip("invalid notification preferences")
		}
	}
	if prefs.MinChanged < 0 || prefs.DeferSeconds < 0 || prefs.DeferSeconds > 7*24*3600 {
		return skip("invalid notification thresholds")
	}
	changed := len(report.Changed) > 0 && len(report.Changed) >= prefs.MinChanged
	switch policy.Notify {
	case store.NotifyOnChange:
		if !changed {
			return skip("change threshold not met")
		}
	case store.NotifyOnDecision:
		if len(report.NeedsApproval) == 0 {
			return skip("no decision needed")
		}
	case store.NotifyAlways:
		if prefs.MinChanged > 0 && !changed {
			return skip("change threshold not met")
		}
	default:
		return skip("unknown notification policy")
	}
	if !deferred && prefs.DeferSeconds > 0 {
		return NotificationEvaluation{Decision: store.NotificationDefer, Reason: "deferred once by approved preferences", AvailableAt: now.Add(time.Duration(prefs.DeferSeconds) * time.Second)}
	}
	return NotificationEvaluation{Decision: store.NotificationSend, Reason: "approved policy threshold met", AvailableAt: now}
}

// Publisher uses the existing exact-target Notifier contract. Empty or uncertain
// receipts do not prove delivery and remain durably uncertain.

type Publisher struct {
	Store    store.BotPublicationStore
	Notifier Notifier
	Now      func() time.Time
}

func (p *Publisher) clock() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// Publish is replay-safe. Calling it again revalidates due deferred entries, but
// never resends a claimed/uncertain entry. Transport IO is outside DB transactions.
func (p *Publisher) Publish(ctx context.Context, botID store.BotID, runID store.RunID) (*store.BotPublication, error) {
	return p.PublishOutcome(ctx, botID, runID, 0)
}

// PublishOutcome publishes a specific committed report activity. Zero selects the
// latest outcome; reconciliation supplies explicit sequences so no outcome is lost.
func (p *Publisher) PublishOutcome(ctx context.Context, botID store.BotID, runID store.RunID, sequence int64) (*store.BotPublication, error) {
	if p.Store == nil {
		return nil, store.ErrBotUnsupported
	}
	publication, err := p.Store.PublishReport(ctx, store.BotPublicationRequest{ReportSequence: sequence, BotID: botID, RunID: runID, Now: p.clock()})
	if err != nil {
		return nil, err
	}
	if p.Notifier == nil {
		return publication, nil
	}
	var failures []error
	for i := range publication.Notifications {
		n := &publication.Notifications[i]
		claimed, send, err := p.Store.ClaimNotification(ctx, store.BotNotificationClaim{ReportSequence: publication.ReportSequence, BotID: botID, RunID: runID, BindingID: n.Target.ID, Now: p.clock()})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if claimed != nil {
			*n = *claimed
		}
		if !send {
			continue
		}
		receipt, sendErr := p.Notifier.Send(ctx, NotificationRequest{ID: n.AttemptID, BotID: botID, RunID: runID, Decision: n.Decision, Reason: n.Reason, Target: n.Target, Body: n.Text})
		delivered := sendErr == nil && !receipt.Uncertain && strings.TrimSpace(receipt.ProviderID) != ""
		finishErr := p.Store.FinishNotification(ctx, store.BotNotificationFinish{ReportSequence: publication.ReportSequence, BotID: botID, RunID: runID, BindingID: n.Target.ID, AttemptID: n.AttemptID, Delivered: delivered, Receipt: receipt.ProviderID, Now: p.clock()})
		if finishErr == nil && delivered {
			n.State = store.BotNotificationDelivered
			n.Receipt = receipt.ProviderID
		}
		if sendErr != nil {
			failures = append(failures, sendErr)
		}
		if finishErr != nil {
			failures = append(failures, finishErr)
		}
	}
	return publication, errors.Join(failures...)
}
