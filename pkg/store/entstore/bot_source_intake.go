package entstore

import (
	"context"
	stdsql "database/sql"
	"entgo.io/ent/dialect/sql"
	"errors"
	"fmt"
	pe "github.com/SAP/astonish/ent/personal/sessionevent"
	te "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"time"
)

func (s *personalBotStore) ProposeSourceIntake(c context.Context, q store.BotSourceIntakeRequest, t store.BotTemplateStore) (*store.BotSourceIntakeProposal, error) {
	return s.repository().proposeSourceIntake(c, q, t)
}
func (s *teamBotStore) ProposeSourceIntake(c context.Context, q store.BotSourceIntakeRequest, t store.BotTemplateStore) (*store.BotSourceIntakeProposal, error) {
	return s.repository().proposeSourceIntake(c, q, t)
}
func (s *personalBotStore) GetSourceIntake(c context.Context, id uuid.UUID) (*store.BotSourceIntakeProposal, error) {
	return s.repository().getSourceIntake(c, id)
}
func (s *teamBotStore) GetSourceIntake(c context.Context, id uuid.UUID) (*store.BotSourceIntakeProposal, error) {
	return s.repository().getSourceIntake(c, id)
}
func (s *personalBotStore) DecideSourceIntake(c context.Context, id uuid.UUID, q store.BotSourceIntakeDecision, t store.BotTemplateStore, d store.BotStore) (*store.BotSourceIntakeProposal, error) {
	return s.repository().decideSourceIntake(c, id, q, t, d)
}
func (s *teamBotStore) DecideSourceIntake(c context.Context, id uuid.UUID, q store.BotSourceIntakeDecision, t store.BotTemplateStore, d store.BotStore) (*store.BotSourceIntakeProposal, error) {
	return s.repository().decideSourceIntake(c, id, q, t, d)
}
func (r *botRepository) getSourceIntake(ctx context.Context, id uuid.UUID) (*store.BotSourceIntakeProposal, error) {
	actor, e := r.auth.authorize(ctx, uuid.Nil, false)
	if e != nil {
		return nil, e
	}
	p, _, e := sourceProposalRead(ctx, r.db, id, actor)
	return p, e
}
func sourceChatStamp(ctx context.Context, tx botDraftDB, id, actor uuid.UUID) (string, time.Time, error) {
	if e := tx.(botIntakeDB).reviewChat(ctx, id, actor); e != nil {
		return "", time.Time{}, e
	}
	var updated time.Time
	var count int
	var eventIDs any
	switch d := tx.(type) {
	case personalBotDB:
		row, e := d.client.Session.Get(ctx, id.String())
		if e != nil {
			return "", updated, e
		}
		updated, count = row.UpdatedAt, row.MessageCount
		eventIDs, e = d.client.SessionEvent.Query().Where(pe.SessionIDEQ(id.String())).Order(func(s *sql.Selector) { s.OrderBy(pe.FieldID) }).All(ctx)
		if e != nil {
			return "", updated, e
		}
	case teamBotDB:
		row, e := d.client.Session.Get(ctx, id.String())
		if e != nil {
			return "", updated, e
		}
		updated, count = row.UpdatedAt, row.MessageCount
		eventIDs, e = d.client.SessionEvent.Query().Where(te.SessionIDEQ(id.String())).Order(func(s *sql.Selector) { s.OrderBy(te.FieldID) }).All(ctx)
		if e != nil {
			return "", updated, e
		}
	}
	h, e := botHash([]any{id, updated, count, eventIDs})
	return h, updated, e
}
func (r *botRepository) sourceSnapshot(ctx context.Context, tx botDraftDB, p *store.BotSourceIntakeProposal, templates store.BotTemplateStore) error {
	q := p.Request
	switch q.Mode {
	case store.BotIntakeChat:
		h, at, e := sourceChatStamp(ctx, tx, q.SourceID, p.OwnerID)
		p.SourceHash, p.SourceUpdatedAt = h, at
		return e
	case store.BotIntakeAdaptive:
		var at time.Time
		switch d := tx.(type) {
		case personalBotDB:
			row, e := d.client.ScheduledJob.Get(ctx, q.SourceID)
			if e != nil {
				return personalBotError(e)
			}
			at = row.UpdatedAt
		case teamBotDB:
			row, e := d.client.ScheduledJob.Get(ctx, q.SourceID)
			if e != nil {
				return teamBotError(e)
			}
			at = row.UpdatedAt
		}
		j, e := tx.(botIntakeDB).reviewAdaptive(ctx, q.SourceID, p.OwnerID, at)
		if e != nil {
			return e
		}
		p.Adaptive = &store.BotAdaptiveReview{SourceJobID: q.SourceID, UpdatedAt: at, Schedule: j.Schedule, Instructions: j.Payload.Instructions}
		p.SourceUpdatedAt = at
		p.SourceHash, e = botHash(p.Adaptive)
		return e
	case store.BotIntakeTemplate:
		if templates == nil || r.auth.scope != store.BotScopeTeam {
			return store.ErrBotForbidden
		}
		// Read through the existing team transaction: a second connection can
		// deadlock single-connection SQLite and would not share its snapshot.
		if source, ok := templates.(*teamBotTemplateStore); ok {
			d, ok := tx.(teamBotDB)
			if !ok || source.auth.orgSlug != r.auth.orgSlug || source.auth.teamSlug != r.auth.teamSlug {
				return store.ErrBotForbidden
			}
			copy := *source
			copy.client = d.client
			templates = &copy
		}
		t, e := templates.Get(ctx, q.SourceID)
		if e != nil {
			return e
		}
		if q.TemplateRevision != t.Revision {
			return store.ErrBotConflict
		}
		p.Template = t
		p.SourceHash, e = botHash(t)
		return e
	default:
		return store.ErrBotInvalid
	}
}
func (r *botRepository) proposeSourceIntake(ctx context.Context, q store.BotSourceIntakeRequest, templates store.BotTemplateStore) (*store.BotSourceIntakeProposal, error) {
	actor, e := r.auth.authorize(ctx, uuid.Nil, true)
	if e != nil {
		return nil, e
	}
	if q.SourceID == uuid.Nil || strings.TrimSpace(q.Name) == "" || strings.TrimSpace(q.IdempotencyKey) == "" {
		return nil, store.ErrBotInvalid
	}
	if q.SkillSeed != nil && q.Mode != store.BotIntakeAdaptive {
		return nil, store.ErrBotInvalid
	}
	if e := validateIntakeSkillSeed(q.SkillSeed); e != nil {
		return nil, e
	}
	// Reuse the strict bounded prose/selection validator, without granting ordinary
	// unfinished saves the right to replace the source proposal envelope.
	if _, e = store.NewBotUnfinishedIntake(uuid.New(), actor, r.auth.scope, store.BotUnfinishedPut{IdempotencyKey: q.IdempotencyKey, Content: store.BotUnfinishedContent{Mode: q.Mode, Name: q.Name, Charter: q.Charter, Carryover: q.Carryover}}, r.clock()); e != nil {
		return nil, e
	}
	if q.Mode != store.BotIntakeChat && (len(q.Carryover.Memories)+len(q.Carryover.Goals)+len(q.Carryover.Artifacts) > 0 || q.Carryover.Summary != "") {
		return nil, store.ErrBotInvalid
	}
	id := uuid.NewSHA1(actor, []byte("source-intake:"+q.IdempotencyKey))
	var out *store.BotSourceIntakeProposal
	e = sourceIntakeTransaction(ctx, r.db, func(tx botDraftDB) error {
		old, _, err := sourceProposalRead(ctx, tx, id, actor)
		if err == nil {
			a, _ := botHash(old.Request)
			b, _ := botHash(q)
			if a != b {
				return store.ErrBotConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		p := &store.BotSourceIntakeProposal{ID: id, OwnerID: actor, Scope: r.auth.scope, Request: q, Status: store.BotSourceIntakeProposed, CreatedAt: r.clock()}
		if err = r.sourceSnapshot(ctx, tx, p, templates); err != nil {
			return err
		}
		p.Digest, err = botHash([]any{p.ID, p.OwnerID, p.Scope, p.Request, p.SourceHash, p.Template, p.Adaptive})
		if err != nil {
			return err
		}
		if err = sourceProposalWrite(ctx, tx, p, 0); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, e
}

// Source snapshots and their local effects use one serializable transaction.
// SQLite already serializes writers; PostgreSQL must not use statement-by-statement
// READ COMMITTED snapshots for a source-bound decision. Serialization failures are
// safe to retry with the same proposal ID and digest.
func sourceIntakeTransaction(ctx context.Context, db botDraftDB, fn func(botDraftDB) error) error {
	switch d := db.(type) {
	case personalBotDB:
		tx, err := d.client.BeginTx(ctx, &stdsql.TxOptions{Isolation: stdsql.LevelSerializable})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := fn(personalBotDB{client: tx.Client()}); err != nil {
			return err
		}
		return personalBotError(tx.Commit())
	case teamBotDB:
		tx, err := d.client.BeginTx(ctx, &stdsql.TxOptions{Isolation: stdsql.LevelSerializable})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := fn(teamBotDB{client: tx.Client()}); err != nil {
			return err
		}
		return teamBotError(tx.Commit())
	default:
		return store.ErrBotUnsupported
	}
}

func sourceDraftKey(p *store.BotSourceIntakeProposal) string {
	return "source-intake:" + p.ID.String() + ":" + p.Digest
}
func (r *botRepository) applySourceIntake(ctx context.Context, tx botDraftDB, p *store.BotSourceIntakeProposal) (*store.Bot, error) {
	q := p.Request
	v := store.DefaultBotVersion()
	v.Charter = q.Charter
	goals := []store.BotGoal{}
	for _, g := range q.Carryover.Goals {
		goals = append(goals, store.BotGoal{Title: g.Title, CompletionCriteria: g.CompletionCriteria})
	}
	for _, a := range q.Carryover.Artifacts {
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Content) == "" {
			return nil, store.ErrBotInvalid
		}
	}
	if p.Adaptive != nil {
		a := p.Adaptive
		v.Triggers = []store.TriggerSpec{{ID: uuid.NewSHA1(p.ID, []byte("schedule")), Kind: store.TriggerCron, Cron: a.Schedule.Cron, Timezone: a.Schedule.Timezone, Instructions: a.Instructions}}
	}
	rec, v, goals, e := prepareBotDraft(store.BotCreateRequest{Name: q.Name, OwnerID: p.OwnerID, IdempotencyKey: sourceDraftKey(p), Version: v, Goals: goals}, p.OwnerID, r.auth.scope, r.clock())
	if e != nil {
		return nil, e
	}
	rec.CreateHash = p.Digest
	b, e := insertIntakeDraft(ctx, tx, rec, v, goals, q.Carryover, string(q.Mode), q.SourceID, q.SkillSeed)
	if e != nil {
		return nil, e
	}
	if p.Adaptive != nil {
		if e = tx.(botIntakeDB).pauseAdaptive(ctx, q.SourceID, p.SourceUpdatedAt); e != nil {
			return nil, e
		}
	}
	return b, nil
}

// Template copies span databases. Recover a committed deterministic destination
// before checking a newer template; never report applied until its durable Bot exists.
func (r *botRepository) templateDestination(ctx context.Context, p *store.BotSourceIntakeProposal, destination store.BotStore) (*personalBotStore, *store.Bot, error) {
	d, ok := destination.(*personalBotStore)
	if !ok || d.auth.orgSlug != r.auth.orgSlug {
		return nil, nil, store.ErrBotForbidden
	}
	actor, e := d.auth.authorize(ctx, p.OwnerID, true)
	if e != nil || actor != p.OwnerID {
		return nil, nil, store.ErrBotForbidden
	}
	key, _ := botHash([]string{actor.String(), sourceDraftKey(p)})
	old, e := d.repository().db.FindCreate(ctx, key)
	if e == nil {
		return d, &old.Bot, nil
	}
	if !errors.Is(e, store.ErrBotNotFound) {
		return nil, nil, e
	}
	return d, nil, nil
}
func createSourceTemplate(ctx context.Context, d *personalBotStore, p *store.BotSourceIntakeProposal) (*store.Bot, error) {
	t := p.Template
	v := store.DefaultBotVersion()
	v.Charter = t.Definition.Charter
	v.Persona.Language = t.Definition.Language
	for i, s := range t.Definition.SuggestedSchedules {
		v.Triggers = append(v.Triggers, store.TriggerSpec{ID: uuid.NewSHA1(p.ID, []byte(fmt.Sprint(i))), Kind: store.TriggerCron, Cron: s.Cron, Timezone: s.Timezone, Instructions: s.Instructions, HookStatus: store.HookDisabled})
	}
	goals := []store.BotGoal{}
	for _, g := range t.Definition.SuggestedGoals {
		goals = append(goals, store.BotGoal{Title: g.Title, Description: g.Description, CompletionCriteria: g.CompletionCriteria})
	}
	return createSeededTemplateDraft(ctx, d, store.BotCreateRequest{Name: p.Request.Name, OwnerID: p.OwnerID, IdempotencyKey: sourceDraftKey(p), Version: v, Goals: goals}, t)
}
func (r *botRepository) decideSourceIntake(ctx context.Context, id uuid.UUID, q store.BotSourceIntakeDecision, templates store.BotTemplateStore, destination store.BotStore) (*store.BotSourceIntakeProposal, error) {
	if _, worker := execution.BotRunIdentityFromContext(ctx); worker {
		return nil, store.ErrBotForbidden
	}
	actor, e := r.auth.authorize(ctx, uuid.Nil, true)
	if e != nil {
		return nil, e
	}
	if q.Decision != "confirm" && q.Decision != "reject" || q.Digest == "" {
		return nil, store.ErrBotInvalid
	}
	// A template fork has an independently committed private destination. Persist
	// confirmation intent first so a crash cannot leave a falsely rejectable draft.
	peek, _, err := sourceProposalRead(ctx, r.db, id, actor)
	if err != nil {
		return nil, err
	}
	if peek.Request.Mode == store.BotIntakeTemplate && q.Decision == "confirm" {
		e = sourceIntakeTransaction(ctx, r.db, func(tx botDraftDB) error {
			p, rev, err := sourceProposalRead(ctx, tx, id, actor)
			if err != nil {
				return err
			}
			if p.Digest != q.Digest {
				return store.ErrBotConflict
			}
			if p.Status != store.BotSourceIntakeProposed {
				return nil
			}
			if _, _, err = r.templateDestination(ctx, p, destination); err != nil {
				return err
			}
			fresh := *p
			if err = r.sourceSnapshot(ctx, tx, &fresh, templates); err != nil {
				return err
			}
			if fresh.SourceHash != p.SourceHash {
				return store.ErrBotConflict
			}
			p.Status = store.BotSourceIntakeApplying
			now := r.clock()
			p.DecidedAt = &now
			return sourceProposalWrite(ctx, tx, p, rev)
		})
		if e != nil {
			return nil, e
		}
	}
	var out *store.BotSourceIntakeProposal
	e = sourceIntakeTransaction(ctx, r.db, func(tx botDraftDB) error {
		p, rev, err := sourceProposalRead(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		if p.Digest != q.Digest {
			return store.ErrBotConflict
		}
		if p.Status != store.BotSourceIntakeProposed && !(p.Status == store.BotSourceIntakeApplying && q.Decision == "confirm") {
			if p.Status == store.BotSourceIntakeApplied && q.Decision == "confirm" || p.Status == store.BotSourceIntakeRejected && q.Decision == "reject" {
				out = p
				return nil
			}
			return store.ErrBotConflict
		}
		var b *store.Bot
		var dest *personalBotStore
		if p.Request.Mode == store.BotIntakeTemplate {
			// Acquire the proposal's write fence before any independently committed
			// destination operation. A concurrent stale/reject/retry cannot overtake
			// a worker after it observed an absent destination.
			if err = sourceProposalWrite(ctx, tx, p, rev); err != nil {
				return err
			}
			rev++
			dest, b, err = r.templateDestination(ctx, p, destination)
			if err != nil {
				return err
			}
		}
		// A recovered committed destination wins over a late rejection; rejection
		// cannot pretend an already created private Bot did not commit.
		if b != nil && q.Decision == "reject" {
			return store.ErrBotConflict
		}
		if q.Decision == "reject" {
			p.Status = store.BotSourceIntakeRejected
		} else {
			if r.auth.scope == store.BotScopeTeam && p.Request.Mode != store.BotIntakeTemplate && !q.ReviewedForTeam {
				return store.ErrBotForbidden
			}
			if b == nil {
				fresh := *p
				if err = r.sourceSnapshot(ctx, tx, &fresh, templates); err != nil {
					return err
				}
				if fresh.SourceHash != p.SourceHash {
					p.Status = store.BotSourceIntakeStale
					out = p
					return sourceProposalWrite(ctx, tx, p, rev)
				}
				if dest != nil {
					b, err = createSourceTemplate(ctx, dest, p)
				} else {
					b, err = r.applySourceIntake(ctx, tx, p)
				}
				if err != nil {
					return err
				}
			}
			p.Status = store.BotSourceIntakeApplied
			p.BotID = &b.ID
		}
		now := r.clock()
		p.DecidedAt = &now
		if err = sourceProposalWrite(ctx, tx, p, rev); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, e
}
