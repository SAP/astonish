package entstore

import (
	"context"
	"path/filepath"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// The lookup is bound at the adapter call site, so a transaction reads its own
// Bot row (including a just-inserted draft) rather than reentering the outer DB.
type botContentLookupKey struct{}
type botContentLookup func(context.Context, store.BotID) (*botStoredRecord, error)

func botContentDirectory(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "bot-content")
}

func newScopedBotContentCodec(directory, org, team string, homeBackend ...func() sandbox.BotHomeFilesBackend) *botContentCodec {
	return &botContentCodec{OpenIntake: newBotIntakeOpener(directory, org, team), Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if directory == "" {
			return nil, store.ErrBotUnsupported
		}
		lookup, ok := ctx.Value(botContentLookupKey{}).(botContentLookup)
		if !ok {
			return nil, store.ErrBotUnsupported
		}
		row, err := lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		scope := store.BotContentScope{OrgID: org, TeamID: team, OwnerID: row.Bot.OwnerID.String(), BotID: id}
		// Personal data belongs to the org/user, not whichever team the viewer has
		// selected today. Keep a stable, explicitly personal namespace in that org.
		if row.Bot.Scope == store.BotScopePersonal && org != "" {
			scope.TeamID = personalSchemaName(scope.OwnerID)
		}
		if row.Bot.ID != id {
			return nil, store.ErrBotForbidden
		}
		staging, err := newBotDiskContent(directory, scope)
		if err != nil {
			return nil, err
		}
		if purge, _ := ctx.Value(botContentPurgeKey{}).(bool); purge {
			return staging, nil
		}
		var backend sandbox.BotHomeFilesBackend
		if len(homeBackend) == 1 && homeBackend[0] != nil {
			backend = homeBackend[0]()
		}
		return openSelectedBotHome(ctx, staging, scope, row, lookup, backend)
	}}
}

func bindBotContentCodec(c *botContentCodec, lookup botContentLookup) *botContentCodec {
	if c == nil || c.Open == nil {
		return nil
	}
	return &botContentCodec{RuntimeScope: func(ctx context.Context, id store.BotID) (store.BotContentScope, error) {
		row, err := lookup(ctx, id)
		if err != nil {
			return store.BotContentScope{}, err
		}
		if row.Bot.ID != id {
			return store.BotContentScope{}, store.ErrBotForbidden
		}
		scope := store.BotContentScope{OwnerID: row.Bot.OwnerID.String(), BotID: id}
		if c.RuntimeScope != nil {
			original, err := c.RuntimeScope(ctx, id)
			if err != nil {
				return store.BotContentScope{}, err
			}
			scope.OrgID, scope.TeamID = original.OrgID, original.TeamID
		}
		return scope, nil
	}, OpenIntake: c.OpenIntake, Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		disk, err := c.Open(context.WithValue(ctx, botContentLookupKey{}, lookup), id)
		if cache, ok := ctx.Value(botContentReadOperationKey{}).(*botTransactionContent); ok && err == nil {
			cache.mu.Lock()
			if home, ok := disk.(*botHomeContent); ok && !cache.closed {
				home.readCache = cache
			}
			cache.mu.Unlock()
		}
		return disk, err
	}}
}

func (d personalBotDB) contentCodec() *botContentCodec {
	return bindBotContentCodec(d.content, d.GetBot)
}
func (d teamBotDB) contentCodec() *botContentCodec { return bindBotContentCodec(d.content, d.GetBot) }
