package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/bottemplate"
	"github.com/SAP/astonish/pkg/store"
)

// teamBotTemplateStore stores reviewed, independently copied definitions only.
// No source Bot lookup is performed, so private records cannot leak implicitly.
type teamBotTemplateStore struct {
	client *teament.Client
	auth   botRepositoryAuth
}

var _ store.BotTemplateStore = (*teamBotTemplateStore)(nil)

func (s *teamBotTemplateStore) authorize(ctx context.Context, publish bool) (uuid.UUID, error) {
	if s == nil || s.client == nil || s.auth.scope != store.BotScopeTeam {
		return uuid.Nil, store.ErrBotUnsupported
	}
	actor, err := s.auth.authorize(ctx, uuid.Nil, publish)
	if err != nil {
		return uuid.Nil, err
	}
	if publish {
		role, err := s.auth.check(ctx, actor.String())
		if err != nil {
			return uuid.Nil, err
		}
		if role != "admin" && role != "owner" {
			return uuid.Nil, store.ErrBotForbidden
		}
	}
	return actor, nil
}
func validateBotTemplate(def store.BotTemplateDefinition) error {
	if def.SchemaVersion != store.BotSchemaVersion {
		return store.ErrBotUnsupported
	}
	if strings.TrimSpace(def.Name) == "" || def.Name != strings.TrimSpace(def.Name) || len(def.Name) > 200 || strings.TrimSpace(def.Charter) == "" || len(def.Charter) > 16000 || len(def.SuggestedGoals) > 100 || len(def.SuggestedSchedules) > 25 {
		return store.ErrBotInvalid
	}
	for _, goal := range def.SuggestedGoals {
		if strings.TrimSpace(goal.Title) == "" || strings.TrimSpace(goal.CompletionCriteria) == "" || len(goal.Title) > 200 || len(goal.Description) > 16000 || len(goal.CompletionCriteria) > 16000 {
			return store.ErrBotInvalid
		}
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, schedule := range def.SuggestedSchedules {
		if err := validateIntakeSkillSeed(schedule.SkillSeed); err != nil {
			return err
		}
		// Do not accept CRON_TZ prefixes or descriptors that override the reviewed
		// timezone/cadence through the parser. The saved format is five fields only.
		if len(strings.Fields(schedule.Cron)) != 5 || schedule.SkillSeed == nil && strings.TrimSpace(schedule.Instructions) == "" || len(schedule.Instructions) > 16000 || schedule.Timezone == "" || schedule.Timezone == "Local" {
			return store.ErrBotInvalid
		}
		if _, err := parser.Parse(schedule.Cron); err != nil {
			return fmt.Errorf("%w: cron", store.ErrBotInvalid)
		}
		if _, err := time.LoadLocation(schedule.Timezone); err != nil {
			return fmt.Errorf("%w: timezone", store.ErrBotInvalid)
		}
	}
	return nil
}
func decodeBotTemplate(row *teament.BotTemplate) (*store.BotTemplate, error) {
	// Reject unknown fields instead of silently dropping an accidentally stored
	// credential/memory/grant field when presenting a supposedly safe definition.
	raw, err := json.Marshal(row.Definition)
	if err != nil {
		return nil, err
	}
	if envelope, ok := row.Definition["bot_document"]; ok {
		text, ok := envelope.(string)
		if !ok || len(row.Definition) != 1 {
			return nil, store.ErrBotInvalid
		}
		raw = []byte(text)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var def store.BotTemplateDefinition
	if err := d.Decode(&def); err != nil {
		return nil, store.ErrBotInvalid
	}
	if err := validateBotTemplate(def); err != nil {
		return nil, err
	}
	if def.Name != row.Name {
		return nil, store.ErrBotInvalid
	}
	return &store.BotTemplate{ID: row.ID, Revision: row.Revision, Definition: def, PublishedBy: row.PublishedBy, PublishedAt: row.PublishedAt}, nil
}
func (s *teamBotTemplateStore) Get(ctx context.Context, id uuid.UUID) (*store.BotTemplate, error) {
	if _, err := s.authorize(ctx, false); err != nil {
		return nil, err
	}
	row, err := s.client.BotTemplate.Get(ctx, id)
	if err != nil {
		return nil, teamBotError(err)
	}
	return decodeBotTemplate(row)
}
func (s *teamBotTemplateStore) List(ctx context.Context, page store.BotPage) ([]store.BotTemplate, error) {
	if _, err := s.authorize(ctx, false); err != nil {
		return nil, err
	}
	limit, after, err := botPage(page)
	if err != nil {
		return nil, err
	}
	q := s.client.BotTemplate.Query().Order(teament.Asc(bottemplate.FieldID)).Limit(limit)
	if after != uuid.Nil {
		q = q.Where(bottemplate.IDGT(after))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.BotTemplate, 0, len(rows))
	for _, row := range rows {
		v, err := decodeBotTemplate(row)
		if err != nil {
			return nil, err
		}
		result = append(result, *v)
	}
	return result, nil
}

// Publish uses exact template name and expected revision: zero creates, positive
// replaces the existing named definition using CAS. Renaming is not implicit.
// Free-text privacy review/redaction must happen in the authenticated service
// before this call; structural allowlisting is not prose sanitization.
func (s *teamBotTemplateStore) Publish(ctx context.Context, def store.BotTemplateDefinition, expected int64) (*store.BotTemplate, error) {
	actor, err := s.authorize(ctx, true)
	if err != nil {
		return nil, err
	}
	if expected < 0 {
		return nil, store.ErrBotInvalid
	}
	if err := validateBotTemplate(def); err != nil {
		return nil, err
	}
	data, err := botJSONMap(def)
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var row *teament.BotTemplate
	if expected == 0 {
		row, err = tx.BotTemplate.Create().SetName(def.Name).SetDefinition(data).SetRevision(1).SetPublishedBy(actor).SetPublishedAt(now).Save(ctx)
		if err != nil {
			return nil, teamBotError(err)
		}
	} else {
		old, err := tx.BotTemplate.Query().Where(bottemplate.NameEQ(def.Name)).Only(ctx)
		if err != nil {
			return nil, teamBotError(err)
		}
		n, err := tx.BotTemplate.Update().Where(bottemplate.IDEQ(old.ID), bottemplate.RevisionEQ(expected)).AddRevision(1).SetDefinition(data).SetPublishedBy(actor).SetPublishedAt(now).Save(ctx)
		if err != nil {
			return nil, teamBotError(err)
		}
		if n != 1 {
			return nil, store.ErrBotConflict
		}
		row, err = tx.BotTemplate.Get(ctx, old.ID)
		if err != nil {
			return nil, err
		}
	}
	// Keep immutable version evidence in the existing append-only team audit log,
	// in the SAME transaction. This is safe published content, not source history.
	err = (&teamAuditStore{client: tx.Client()}).Log(ctx, &store.AuditEntry{Timestamp: now, UserID: actor.String(), Action: "bot_template.publish", Resource: row.ID.String(), Detail: map[string]any{"revision": fmt.Sprintf("%d", row.Revision), "definition": data}})
	if err != nil {
		return nil, err
	}
	result, err := decodeBotTemplate(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, teamBotError(err)
	}
	return result, nil
}

func (s *teamBotTemplateStore) ForkToPrivate(ctx context.Context, destination store.BotStore, req store.BotTemplateForkRequest) (*store.Bot, error) {
	dest, ok := destination.(*personalBotStore)
	if !ok {
		return nil, store.ErrBotForbidden
	}
	return s.ForkToPersonal(ctx, dest, req)
}

// ForkToPersonal copies only the selected template snapshot. The destination
// owns its transaction; no fictitious cross-database transaction is claimed.
// The private create key includes source identity/revision and is deterministic.
func (s *teamBotTemplateStore) ForkToPersonal(ctx context.Context, dest *personalBotStore, req store.BotTemplateForkRequest) (*store.Bot, error) {
	if s == nil || dest == nil || dest.client == nil || dest.auth.scope != store.BotScopePersonal || dest.auth.orgSlug != s.auth.orgSlug {
		return nil, store.ErrBotForbidden
	}
	actor, err := dest.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	if req.TemplateID == uuid.Nil || req.TemplateRevision < 1 || strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 {
		return nil, store.ErrBotInvalid
	}
	source, err := s.Get(ctx, req.TemplateID)
	if err != nil {
		return nil, err
	}
	if source.Revision != req.TemplateRevision {
		return nil, store.ErrBotConflict
	}
	key, err := botHash([]string{"template-fork", s.auth.orgSlug, s.auth.teamSlug, req.TemplateID.String(), fmt.Sprintf("%d", req.TemplateRevision), req.IdempotencyKey})
	if err != nil {
		return nil, err
	}
	v := store.DefaultBotVersion()
	v.Charter = source.Definition.Charter
	v.Persona.Language = source.Definition.Language
	for i, suggestion := range source.Definition.SuggestedSchedules {
		// Deterministic IDs make retry inputs byte-identical before CreateDraft hashes.
		id := uuid.NewSHA1(source.ID, []byte(fmt.Sprintf("schedule:%d:%d", source.Revision, i)))
		v.Triggers = append(v.Triggers, store.TriggerSpec{ID: id, Kind: store.TriggerCron, Cron: suggestion.Cron, Timezone: suggestion.Timezone, Instructions: suggestion.Instructions, HookStatus: store.HookDisabled})
	}
	goals := make([]store.BotGoal, 0, len(source.Definition.SuggestedGoals))
	for _, g := range source.Definition.SuggestedGoals {
		goals = append(goals, store.BotGoal{Title: g.Title, Description: g.Description, CompletionCriteria: g.CompletionCriteria})
	}
	v.ChangeReason = fmt.Sprintf("Forked team template %s revision %d", source.ID, source.Revision)
	return createSeededTemplateDraft(ctx, dest, store.BotCreateRequest{Name: req.Name, OwnerID: actor, Version: v, Goals: goals, IdempotencyKey: key}, source)
}

// A fork's Bot, local owner expectation, skill and initial revision commit in
// one destination transaction. The source contains no private skill identities.
func createSeededTemplateDraft(ctx context.Context, dest *personalBotStore, req store.BotCreateRequest, source *store.BotTemplate) (*store.Bot, error) {
	seeds := make([]*store.BotSkillSeed, len(source.Definition.SuggestedSchedules))
	hasSeed := false
	for i, schedule := range source.Definition.SuggestedSchedules {
		seeds[i] = schedule.SkillSeed
		hasSeed = hasSeed || schedule.SkillSeed != nil
	}
	if !hasSeed {
		return dest.CreateDraft(ctx, req)
	}
	r := dest.repository()
	actor, err := r.auth.authorize(ctx, req.OwnerID, true)
	if err != nil {
		return nil, err
	}
	rec, v, goals, err := prepareBotDraft(req, actor, r.auth.scope, r.clock())
	if err != nil {
		return nil, err
	}
	rec.CreateHash, err = botHash([]any{req, source})
	if err != nil {
		return nil, err
	}
	var result *store.Bot
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		old, e := tx.FindCreate(ctx, rec.CreateKey)
		if e == nil {
			if old.CreateHash != rec.CreateHash {
				return store.ErrBotConflict
			}
			result = &old.Bot
			return nil
		}
		if e != store.ErrBotNotFound {
			return e
		}
		result, e = insertIntakeDraft(ctx, tx, rec, v, goals, store.BotCarryover{}, "template_fork", source.ID, seeds...)
		return e
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
