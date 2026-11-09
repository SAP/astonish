package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// ReconcileBotPublications must run after committed outcomes and on subsequent
// owner-authenticated polls. It scans committed intents, not mutable run status,
// so a waiting outcome and its resumed final outcome both survive a restart.
// Replaying deferred entries revalidates once due; uncertain sends are never retried.
func ReconcileBotPublications(ctx context.Context, repo store.BotStore, id store.BotID, notifier bot.Notifier) error {
	publications, ok := repo.(store.BotPublicationStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	publisher := &bot.Publisher{Store: publications, Notifier: notifier}
	var failures []error
	var after int64
	for {
		activities, err := repo.ListActivity(ctx, id, after, 200)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, activity := range activities {
			after = activity.Sequence
			if activity.Kind != store.BotActivityReport || activity.RunID == nil {
				continue
			}
			var intent struct {
				Intent store.BotOutboxKind `json:"intent"`
			}
			if err := json.Unmarshal(activity.Payload, &intent); err != nil {
				failures = append(failures, err)
				continue
			}
			if intent.Intent != store.BotOutboxPublish {
				continue
			}
			if _, err := publisher.PublishOutcome(ctx, id, *activity.RunID, activity.Sequence); err != nil {
				failures = append(failures, err)
			}
		}
		if len(activities) < 200 {
			return errors.Join(failures...)
		}
	}
}
