package entstore

import (
	"context"
	"encoding/json"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const webhookBindingPrefix = "bot-webhook-binding:"

type botWebhookBinding struct {
	Org         string         `json:"org"`
	Team        string         `json:"team"`
	Owner       string         `json:"owner"`
	Scope       store.BotScope `json:"scope"`
	BotID       uuid.UUID      `json:"bot_id"`
	TriggerID   uuid.UUID      `json:"trigger_id"`
	Fingerprint string         `json:"fingerprint"`
}

// RegisterBotWebhook creates an opaque platform routing record only after the
// scoped repository has authorized the owner and verified a reviewed live trigger.
// No tenant or owner supplied in an ingress URL is ever treated as authority.
func (s *Store) RegisterBotWebhook(ctx context.Context, botID, triggerID uuid.UUID, scope store.BotScope) (string, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok {
		return "", store.ErrBotForbidden
	}
	repo := store.BotStoreFromContext(ctx, scope)
	if repo == nil {
		return "", store.ErrBotUnsupported
	}
	b, err := repo.Get(ctx, botID)
	if err != nil {
		return "", err
	}
	if b.OwnerID.String() != p.Subject || b.Status != store.BotActive {
		return "", store.ErrBotForbidden
	}
	v, err := repo.GetVersion(ctx, botID, b.Version)
	if err != nil {
		return "", err
	}
	for _, tr := range v.Triggers {
		if tr.ID != triggerID {
			continue
		}
		if tr.Kind != store.TriggerWebhook || !tr.Enabled || tr.HookStatus != store.HookEnabled || tr.Fingerprint == "" {
			return "", store.ErrBotForbidden
		}
		binding := botWebhookBinding{Org: p.OrgSlug, Team: p.TeamSlug, Owner: p.Subject, Scope: scope, BotID: botID, TriggerID: triggerID, Fingerprint: tr.Fingerprint}
		raw, _ := json.Marshal(binding)
		var value map[string]interface{}
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		id := uuid.NewString()
		_, err := s.platformClient.PlatformSetting.Create().SetID(webhookBindingPrefix + id).SetValue(value).Save(ctx)
		return id, err
	}
	return "", store.ErrBotNotFound
}

type webhookContext struct{ context.Context }

func (webhookContext) Value(any) any { return nil }

// AdmitBotWebhook looks up exactly one server-owned opaque registration, rechecks
// live membership through the scoped repository, then validates signature and
// replay inside admission. The internal owner context never escapes this method.
func (s *Store) AdmitBotWebhook(ctx context.Context, bindingID string, event store.BotExternalEvent) (*store.BotRun, error) {
	id, err := uuid.Parse(bindingID)
	if err != nil || id.String() != bindingID {
		return nil, store.ErrBotForbidden
	}
	row, err := s.platformClient.PlatformSetting.Get(ctx, webhookBindingPrefix+id.String())
	if err != nil {
		return nil, store.ErrBotForbidden
	}
	raw, _ := json.Marshal(row.Value)
	var b botWebhookBinding
	if json.Unmarshal(raw, &b) != nil || b.BotID != event.BotID || b.TriggerID != event.TriggerID {
		return nil, store.ErrBotForbidden
	}
	org, err := s.ForOrg(b.Org)
	if err != nil {
		return nil, store.ErrBotForbidden
	}
	const auth execution.AuthMethod = "webhook_binding"
	const surface execution.Surface = "webhook"
	owner, err := execution.WithPrincipal(webhookContext{ctx}, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: auth, Surface: surface, Subject: b.Owner, OrgSlug: b.Org, TeamSlug: b.Team, Authenticated: true})
	if err != nil {
		return nil, store.ErrBotForbidden
	}
	svc := &store.Services{}
	var repo store.BotStore
	switch b.Scope {
	case store.BotScopePersonal:
		scoped := org.ForUser(b.Owner)
		if scoped == nil {
			return nil, store.ErrBotForbidden
		}
		repo = scoped.Bots()
		svc.PersonalCredentials = scoped.Credentials()
	case store.BotScopeTeam:
		scoped := org.ForTeam(b.Team)
		if scoped == nil {
			return nil, store.ErrBotForbidden
		}
		repo = scoped.Bots()
		svc.Credentials = scoped.Credentials()
	default:
		return nil, store.ErrBotForbidden
	}
	owner = store.WithServices(owner, svc)
	bot, err := repo.Get(owner, b.BotID)
	if err != nil || bot.OwnerID.String() != b.Owner {
		return nil, store.ErrBotForbidden
	}
	version, err := repo.GetVersion(owner, b.BotID, bot.Version)
	if err != nil {
		return nil, store.ErrBotForbidden
	}
	for _, tr := range version.Triggers {
		if tr.ID == b.TriggerID && tr.Kind == store.TriggerWebhook && tr.Fingerprint == b.Fingerprint {
			triggers, ok := repo.(store.BotTriggerStore)
			if !ok {
				return nil, store.ErrBotUnsupported
			}
			return triggers.AdmitExternalRun(owner, event)
		}
	}
	return nil, store.ErrBotForbidden
}
