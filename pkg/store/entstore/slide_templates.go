package entstore

import (
	"context"
	"encoding/json"

	orgent "github.com/SAP/astonish/ent/org"
	"github.com/SAP/astonish/ent/org/orgslidetemplate"
	platforment "github.com/SAP/astonish/ent/platform"
	"github.com/SAP/astonish/ent/platform/platformslidetemplate"
	"github.com/SAP/astonish/pkg/store"
)

type platformSlideTemplateStore struct {
	client *platforment.Client
}

func (s *Store) PlatformSlideTemplates() store.SlideTemplateStore {
	if s == nil || s.platformClient == nil {
		return nil
	}
	return &platformSlideTemplateStore{client: s.platformClient}
}

var _ store.SlideTemplateStore = (*platformSlideTemplateStore)(nil)

func (ps *platformSlideTemplateStore) Save(ctx context.Context, rec *store.SlideTemplateRecord) error {
	if rec == nil || rec.Name == "" {
		return nil
	}
	existing, err := ps.client.PlatformSlideTemplate.Query().
		Where(platformslidetemplate.NameEQ(rec.Name)).
		Only(ctx)
	if err != nil && !platforment.IsNotFound(err) {
		return err
	}
	palettes := rawToAny(rec.Palettes)
	archetypes := rawToAny(rec.Archetypes)
	if existing != nil {
		upd := existing.Update().
			SetLabel(rec.Label).
			SetDescription(rec.Description).
			SetSchemaVersion(rec.SchemaVersion).
			SetSkin(rec.Skin).
			SetTokens(rec.Tokens).
			SetAssets(rec.Assets).
			SetPalettes(palettes).
			SetArchetypes(archetypes).
			SetTemplateModel(rec.TemplateModel)
		return upd.Exec(ctx)
	}
	create := ps.client.PlatformSlideTemplate.Create().
		SetName(rec.Name).
		SetLabel(rec.Label).
		SetDescription(rec.Description).
		SetSchemaVersion(rec.SchemaVersion).
		SetSkin(rec.Skin).
		SetTokens(rec.Tokens).
		SetAssets(rec.Assets).
		SetPalettes(palettes).
		SetArchetypes(archetypes).
		SetTemplateModel(rec.TemplateModel)
	if rec.CreatedBy != "" {
		create.SetNillableCreatedBy(&rec.CreatedBy)
	}
	_, err = create.Save(ctx)
	return err
}

func (ps *platformSlideTemplateStore) Get(ctx context.Context, name string) (*store.SlideTemplateRecord, error) {
	ent, err := ps.client.PlatformSlideTemplate.Query().
		Where(platformslidetemplate.NameEQ(name)).
		Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return platformEntToRecord(ent), nil
}

func (ps *platformSlideTemplateStore) List(ctx context.Context) ([]store.SlideTemplateRecord, error) {
	ents, err := ps.client.PlatformSlideTemplate.Query().
		Order(platformslidetemplate.ByName()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.SlideTemplateRecord, len(ents))
	for i, e := range ents {
		out[i] = *platformEntToRecord(e)
	}
	return out, nil
}

func (ps *platformSlideTemplateStore) Delete(ctx context.Context, name string) error {
	_, err := ps.client.PlatformSlideTemplate.Delete().
		Where(platformslidetemplate.NameEQ(name)).
		Exec(ctx)
	return err
}

func platformEntToRecord(e *platforment.PlatformSlideTemplate) *store.SlideTemplateRecord {
	rec := &store.SlideTemplateRecord{
		Name:          e.Name,
		Label:         e.Label,
		Description:   e.Description,
		SchemaVersion: e.SchemaVersion,
		Skin:          e.Skin,
		Tokens:        e.Tokens,
		Assets:        e.Assets,
		TemplateModel: e.TemplateModel,
		Palettes:      anyToRaw(e.Palettes),
		Archetypes:    anyToRaw(e.Archetypes),
	}
	if e.CreatedBy != nil {
		rec.CreatedBy = *e.CreatedBy
	}
	return rec
}

type orgSlideTemplateStore struct {
	client *orgent.Client
}

var _ store.SlideTemplateStore = (*orgSlideTemplateStore)(nil)

func (ss *orgSlideTemplateStore) Save(ctx context.Context, rec *store.SlideTemplateRecord) error {
	if rec == nil || rec.Name == "" {
		return nil
	}
	existing, err := ss.client.OrgSlideTemplate.Query().
		Where(orgslidetemplate.NameEQ(rec.Name)).
		Only(ctx)
	if err != nil && !orgent.IsNotFound(err) {
		return err
	}
	palettes := rawToAny(rec.Palettes)
	archetypes := rawToAny(rec.Archetypes)
	if existing != nil {
		upd := existing.Update().
			SetLabel(rec.Label).
			SetDescription(rec.Description).
			SetSchemaVersion(rec.SchemaVersion).
			SetSkin(rec.Skin).
			SetTokens(rec.Tokens).
			SetAssets(rec.Assets).
			SetPalettes(palettes).
			SetArchetypes(archetypes).
			SetTemplateModel(rec.TemplateModel)
		return upd.Exec(ctx)
	}
	create := ss.client.OrgSlideTemplate.Create().
		SetName(rec.Name).
		SetLabel(rec.Label).
		SetDescription(rec.Description).
		SetSchemaVersion(rec.SchemaVersion).
		SetSkin(rec.Skin).
		SetTokens(rec.Tokens).
		SetAssets(rec.Assets).
		SetPalettes(palettes).
		SetArchetypes(archetypes).
		SetTemplateModel(rec.TemplateModel)
	if rec.CreatedBy != "" {
		create.SetNillableCreatedBy(&rec.CreatedBy)
	}
	_, err = create.Save(ctx)
	return err
}

func (ss *orgSlideTemplateStore) Get(ctx context.Context, name string) (*store.SlideTemplateRecord, error) {
	ent, err := ss.client.OrgSlideTemplate.Query().
		Where(orgslidetemplate.NameEQ(name)).
		Only(ctx)
	if err != nil {
		if orgent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return orgEntToRecord(ent), nil
}

func (ss *orgSlideTemplateStore) List(ctx context.Context) ([]store.SlideTemplateRecord, error) {
	ents, err := ss.client.OrgSlideTemplate.Query().
		Order(orgslidetemplate.ByName()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.SlideTemplateRecord, len(ents))
	for i, e := range ents {
		out[i] = *orgEntToRecord(e)
	}
	return out, nil
}

func (ss *orgSlideTemplateStore) Delete(ctx context.Context, name string) error {
	_, err := ss.client.OrgSlideTemplate.Delete().
		Where(orgslidetemplate.NameEQ(name)).
		Exec(ctx)
	return err
}

func orgEntToRecord(e *orgent.OrgSlideTemplate) *store.SlideTemplateRecord {
	rec := &store.SlideTemplateRecord{
		Name:          e.Name,
		Label:         e.Label,
		Description:   e.Description,
		SchemaVersion: e.SchemaVersion,
		Skin:          e.Skin,
		Tokens:        e.Tokens,
		Assets:        e.Assets,
		TemplateModel: e.TemplateModel,
		Palettes:      anyToRaw(e.Palettes),
		Archetypes:    anyToRaw(e.Archetypes),
	}
	if e.CreatedBy != nil {
		rec.CreatedBy = *e.CreatedBy
	}
	return rec
}

func rawToAny(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func anyToRaw(v []any) json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
