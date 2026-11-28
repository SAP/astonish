package entstore

import (
	"context"
	"encoding/json"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func botIntakeScope(org, team string, id, owner uuid.UUID, scope store.BotScope) store.BotContentScope {
	if scope == store.BotScopePersonal && org != "" {
		team = personalSchemaName(owner.String())
	}
	return store.BotContentScope{OrgID: org, TeamID: team, OwnerID: owner.String(), IntakeID: id}
}

func newBotIntakeOpener(directory, org, team string) func(context.Context, uuid.UUID, uuid.UUID, store.BotScope) (store.BotContentStaging, error) {
	return func(ctx context.Context, id, owner uuid.UUID, scope store.BotScope) (store.BotContentStaging, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if directory == "" {
			return nil, store.ErrBotUnsupported
		}
		if id == uuid.Nil || owner == uuid.Nil || (scope != store.BotScopePersonal && scope != store.BotScopeTeam) {
			return nil, store.ErrBotInvalid
		}
		return newBotDiskContent(directory, botIntakeScope(org, team, id, owner, scope))
	}
}

// Exact expected scope comes from the opened, server-owned adapter, not SQL refs.
func (c *botContentCodec) intakeContent(ctx context.Context, b *store.BotUnfinishedIntake) (*botIndependentContent, error) {
	if c == nil || c.OpenIntake == nil {
		return nil, store.ErrBotUnsupported
	}
	disk, err := c.OpenIntake(ctx, b.ID, b.OwnerID, b.Scope)
	if err != nil {
		return nil, err
	}
	scoped, ok := disk.(*botDiskContent)
	if !ok {
		disk.Close()
		return nil, store.ErrBotUnsupported
	}
	scope := scoped.scope
	if err := disk.Close(); err != nil {
		return nil, err
	}
	return &botIndependentContent{Scope: scope, Open: func(ctx context.Context) (store.BotContentStaging, error) {
		return c.OpenIntake(ctx, b.ID, b.OwnerID, b.Scope)
	}}, nil
}

func (c *botContentCodec) encodeIntake(ctx context.Context, b *store.BotUnfinishedIntake) ([]byte, error) {
	if b.Content == nil {
		return nil, store.ErrBotInvalid
	}
	content, err := c.intakeContent(ctx, b)
	if err != nil {
		return nil, err
	}
	data, err := content.encode(ctx, b.ID, store.BotContentIntake, b.Revision, b.Content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(data)
}

func (c *botContentCodec) decodeIntake(ctx context.Context, b *store.BotUnfinishedIntake, raw []byte) error {
	if b.Discarded && len(raw) == 0 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return errBotDevelopmentContentFormat
	}
	// Reject inline development content even when storage is unavailable.
	var record botIndependentRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return err
	}
	content, err := c.intakeContent(ctx, b)
	if err != nil {
		return err
	}
	var body store.BotUnfinishedContent
	if err := content.decode(ctx, b.ID, store.BotContentIntake, b.Revision, data, &body); err != nil {
		return err
	}
	b.Content = &body
	return nil
}
