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
func ReconcileBotPublications(ctx context.Context, repo store.BotStore, id store.BotID, notifier bot.Notifier, indexers ...func(context.Context, *store.BotPublication) error) error {
	publications, ok := repo.(store.BotPublicationStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	list := repo.ListActivity
	if reader, ok := repo.(interface {
		ListBotPublicationActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error)
	}); ok {
		list = reader.ListBotPublicationActivity
	}
	publisher := &bot.Publisher{Store: publications, Notifier: notifier}
	var failures []error
	var after int64
	for {
		activities, err := list(ctx, id, after, 200)
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
			publication, err := publisher.PublishOutcome(ctx, id, *activity.RunID, activity.Sequence)
			if err != nil {
				failures = append(failures, err)
			}
			// A notification error can coexist with a committed Home publication.
			// Index failures leave the same receipt available for metadata-only retry.
			if publication != nil && !publication.Suppressed {
				for _, index := range indexers {
					if err := index(ctx, publication); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
		if len(activities) < 200 {
			return errors.Join(failures...)
		}
	}
}
