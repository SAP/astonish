package entstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// BotHomeRegistration is an internal registry attestation, never an HTTP DTO.
// Home is opaque: persisting it neither provisions storage nor starts compute.
type BotHomeRegistration struct {
	BotID            store.BotID
	ExpectedRevision int64
	Home             store.BotHomeBinding
}

const BotHomeRegistrationScope = "bots:home:register"

// BotHomeRegistrationAuthorization is supplied only by trusted composition, never
// decoded from a request. Validate must live-check the stored owner's identity and
// membership; RequestContext preserves the actual requesting editor, not a service.
// Both checks run inside the binding transaction, including idempotent replays.
type BotHomeRegistrationAuthorization struct {
	RequestContext context.Context
	Validate       func(context.Context, store.Bot) error
}

// RegisterBotHomeBinding is deliberately separate from the public BotStore API.
// Trusted composition supplies verify for the exact already provisioned binding.
// verify runs outside the transaction. Public setup MUST also supply authorization;
// omission preserves the existing internal service-only registry seam.
func RegisterBotHomeBinding(ctx context.Context, bots store.BotStore, q BotHomeRegistration, verify func(context.Context, BotHomeRegistration) error, authorization ...BotHomeRegistrationAuthorization) error {
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.repository()
	case *teamBotStore:
		r = s.repository()
	default:
		return store.ErrBotUnsupported
	}
	return r.registerHomeBinding(ctx, q, verify, authorization...)
}

type botHomeBindingDB interface {
	saveHomeBinding(context.Context, botStoredRecord, map[string]any, time.Time) error
}

func (r *botRepository) registerHomeBinding(ctx context.Context, q BotHomeRegistration, verify func(context.Context, BotHomeRegistration) error, authorization ...BotHomeRegistrationAuthorization) error {
	if len(authorization) > 1 || len(authorization) == 1 && (authorization[0].RequestContext == nil || authorization[0].Validate == nil) {
		return store.ErrBotForbidden
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindService || p.Subject != "" || p.Surface != execution.SurfaceCode || !p.HasScope(BotHomeRegistrationScope) || r.auth.orgSlug == "" || p.OrgSlug != r.auth.orgSlug || r.auth.teamSlug != "" && p.TeamSlug != r.auth.teamSlug || verify == nil {
		return store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.ExpectedRevision < 1 || q.Home.ID == uuid.Nil || q.Home.BotID != q.BotID || q.Home.Scope != r.auth.scope || q.Home.Revision != 1 || strings.TrimSpace(q.Home.Backend) == "" || strings.TrimSpace(q.Home.StorageKey) == "" {
		return store.ErrBotInvalid
	}
	check := func(rec *botStoredRecord) error {
		if rec.Bot.ID != q.BotID || rec.Bot.Scope != q.Home.Scope || r.auth.scope == store.BotScopePersonal && rec.Bot.OwnerID.String() != r.auth.userID {
			return store.ErrBotForbidden
		}
		if rec.Bot.Status != store.BotDraft || rec.ActiveRunID != nil || rec.LeaseExpiresAt != nil {
			return store.ErrBotConflict
		}
		if rec.Bot.Home == q.Home && rec.Bot.Revision == q.ExpectedRevision+1 {
			return nil
		}
		if rec.Bot.Revision != q.ExpectedRevision || rec.Bot.Home != (store.BotHomeBinding{}) {
			return store.ErrBotConflict
		}
		return nil
	}
	rec, err := r.db.GetBot(ctx, q.BotID)
	if err != nil {
		return err
	}
	if err := check(rec); err != nil {
		return err
	}
	if err := verify(ctx, q); err != nil {
		return fmt.Errorf("verify bot home registration: %w", err)
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		if err := check(rec); err != nil {
			return err
		}
		if len(authorization) == 1 {
			a := authorization[0]
			if _, err := r.auth.authorize(a.RequestContext, rec.Bot.OwnerID, true); err != nil {
				return err
			}
			if err := a.Validate(a.RequestContext, rec.Bot); err != nil {
				return err
			}
		}
		if rec.Bot.Home == q.Home {
			return nil
		}
		d, ok := tx.(botHomeBindingDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		data, err := botJSONMap(q.Home)
		if err != nil {
			return err
		}
		return d.saveHomeBinding(ctx, *rec, data, r.clock())
	})
}

func (d teamBotDB) saveHomeBinding(ctx context.Context, rec botStoredRecord, data map[string]any, now time.Time) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.StatusEQ(teambot.StatusDraft), teambot.HomeBindingIsNil()).SetHomeBinding(data).AddRevision(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d personalBotDB) saveHomeBinding(ctx context.Context, rec botStoredRecord, data map[string]any, now time.Time) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.StatusEQ(personalbot.StatusDraft), personalbot.HomeBindingIsNil()).SetHomeBinding(data).AddRevision(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
