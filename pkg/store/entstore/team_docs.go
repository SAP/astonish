package entstore

import (
	"context"
	"fmt"

	teament "github.com/SAP/astonish/ent/team"
	teamdeck "github.com/SAP/astonish/ent/team/deck"
	teamslide "github.com/SAP/astonish/ent/team/slide"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type teamDocsStore struct{ client *teament.Client }

func (s *teamDocsStore) CreateDeck(ctx context.Context, d *store.DeckManifest) error {
	id, err := docsUUID(d.ID)
	if err != nil {
		return err
	}
	row, err := s.client.Deck.Create().SetID(id).SetSlug(d.Slug).SetTitle(d.Title).SetDescription(d.Description).SetSchemaVersion(d.SchemaVersion).SetTheme(d.Theme).SetAssets(d.Assets).SetTemplateModel(d.TemplateModel).Save(ctx)
	if err == nil {
		fillTeamDeck(d, row)
	}
	return err
}
func (s *teamDocsStore) GetDeck(ctx context.Context, slug string) (*store.DeckManifest, error) {
	row, err := s.client.Deck.Query().Where(teamdeck.SlugEQ(slug)).Only(ctx)
	if teament.IsNotFound(err) {
		return nil, store.ErrDocsNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &store.DeckManifest{}
	fillTeamDeck(d, row)
	return d, nil
}
func (s *teamDocsStore) ListDecks(ctx context.Context) ([]*store.DeckManifest, error) {
	rows, err := s.client.Deck.Query().Order(teament.Desc(teamdeck.FieldUpdatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.DeckManifest, 0, len(rows))
	for _, row := range rows {
		d := &store.DeckManifest{}
		fillTeamDeck(d, row)
		out = append(out, d)
	}
	return out, nil
}

// ListDecksLite mirrors ListDecks but projects away the two heavy columns
// (assets, template_model) at the SQL level, so the multi-MB IR and base64
// asset blobs are never read/deserialized for list views. The returned
// manifests have Assets=nil and TemplateModel="".
func (s *teamDocsStore) ListDecksLite(ctx context.Context) ([]*store.DeckManifest, error) {
	rows, err := s.client.Deck.Query().
		Order(teament.Desc(teamdeck.FieldUpdatedAt)).
		Select(
			teamdeck.FieldID,
			teamdeck.FieldSlug,
			teamdeck.FieldTitle,
			teamdeck.FieldDescription,
			teamdeck.FieldSchemaVersion,
			teamdeck.FieldTheme,
			teamdeck.FieldCreatedAt,
			teamdeck.FieldUpdatedAt,
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.DeckManifest, 0, len(rows))
	for _, row := range rows {
		d := &store.DeckManifest{}
		fillTeamDeckLite(d, row)
		out = append(out, d)
	}
	return out, nil
}
func (s *teamDocsStore) UpdateDeck(ctx context.Context, d *store.DeckManifest) error {
	row, err := s.client.Deck.Query().Where(teamdeck.SlugEQ(d.Slug)).Only(ctx)
	if teament.IsNotFound(err) {
		return store.ErrDocsNotFound
	}
	if err != nil {
		return err
	}
	row, err = row.Update().SetTitle(d.Title).SetDescription(d.Description).SetSchemaVersion(d.SchemaVersion).SetTheme(d.Theme).SetAssets(d.Assets).SetTemplateModel(d.TemplateModel).Save(ctx)
	if err == nil {
		fillTeamDeck(d, row)
	}
	return err
}
func (s *teamDocsStore) DeleteDeck(ctx context.Context, slug string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	deckID, err := tx.Deck.Query().Where(teamdeck.SlugEQ(slug)).OnlyID(ctx)
	if teament.IsNotFound(err) {
		return store.ErrDocsNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Slide.Delete().Where(teamslide.HasDeckWith(teamdeck.IDEQ(deckID))).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Deck.DeleteOneID(deckID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *teamDocsStore) UpsertSlide(ctx context.Context, in *store.SlideContent) error {
	deckID, err := uuid.Parse(in.DeckID)
	if err != nil {
		return fmt.Errorf("invalid deck id: %w", err)
	}
	id, err := docsUUID(in.ID)
	if err != nil {
		return err
	}
	row, err := s.client.Slide.Query().Where(teamslide.IDEQ(id), teamslide.HasDeckWith(teamdeck.IDEQ(deckID))).Only(ctx)
	if teament.IsNotFound(err) {
		row, err = s.client.Slide.Create().SetID(id).SetDeckID(deckID).SetPosition(in.Position).SetTitle(in.Title).SetContent(in.Content).SetNotes(in.Notes).SetSchemaVersion(in.SchemaVersion).Save(ctx)
	} else if err == nil {
		row, err = row.Update().SetPosition(in.Position).SetTitle(in.Title).SetContent(in.Content).SetNotes(in.Notes).SetSchemaVersion(in.SchemaVersion).Save(ctx)
	}
	if err == nil {
		fillTeamSlide(in, row)
	}
	return err
}
func (s *teamDocsStore) GetSlide(ctx context.Context, deckID, slideID string) (*store.SlideContent, error) {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	sid, err := uuid.Parse(slideID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	row, err := s.client.Slide.Query().Where(teamslide.IDEQ(sid), teamslide.HasDeckWith(teamdeck.IDEQ(did))).Only(ctx)
	if teament.IsNotFound(err) {
		return nil, store.ErrDocsNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &store.SlideContent{}
	fillTeamSlide(out, row)
	return out, nil
}
func (s *teamDocsStore) ListSlides(ctx context.Context, deckID string) ([]*store.SlideContent, error) {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	rows, err := s.client.Slide.Query().Where(teamslide.HasDeckWith(teamdeck.IDEQ(did))).WithDeck().Order(teament.Asc(teamslide.FieldPosition)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.SlideContent, 0, len(rows))
	for _, row := range rows {
		v := &store.SlideContent{}
		fillTeamSlide(v, row)
		out = append(out, v)
	}
	return out, nil
}
func (s *teamDocsStore) DeleteSlide(ctx context.Context, deckID, slideID string) error {
	did, e1 := uuid.Parse(deckID)
	sid, e2 := uuid.Parse(slideID)
	if e1 != nil || e2 != nil {
		return store.ErrDocsNotFound
	}
	n, err := s.client.Slide.Delete().Where(teamslide.IDEQ(sid), teamslide.HasDeckWith(teamdeck.IDEQ(did))).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrDocsNotFound
	}
	return nil
}
func (s *teamDocsStore) ReorderSlides(ctx context.Context, deckID string, ids []string) error {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return store.ErrDocsNotFound
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Slide.Query().Where(teamslide.HasDeckWith(teamdeck.IDEQ(did))).All(ctx)
	if err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return store.ErrDocsNotFound
	}
	known := make(map[uuid.UUID]struct{}, len(rows))
	temporaryPosition := len(rows)
	for _, row := range rows {
		known[row.ID] = struct{}{}
		if row.Position >= temporaryPosition {
			temporaryPosition = row.Position + 1
		}
	}
	parsed := make([]uuid.UUID, len(ids))
	for i, raw := range ids {
		sid, err := uuid.Parse(raw)
		if err != nil {
			return store.ErrDocsNotFound
		}
		if _, ok := known[sid]; !ok {
			return store.ErrDocsNotFound
		}
		delete(known, sid)
		parsed[i] = sid
	}
	if _, err := tx.Slide.Update().Where(teamslide.HasDeckWith(teamdeck.IDEQ(did))).AddPosition(temporaryPosition).Save(ctx); err != nil {
		return err
	}
	for pos, sid := range parsed {
		if _, err := tx.Slide.UpdateOneID(sid).SetPosition(pos).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func fillTeamDeck(out *store.DeckManifest, in *teament.Deck) {
	out.ID = in.ID.String()
	out.Slug = in.Slug
	out.Title = in.Title
	out.Description = in.Description
	out.SchemaVersion = in.SchemaVersion
	out.Theme = in.Theme
	out.Assets = in.Assets
	out.TemplateModel = in.TemplateModel
	out.CreatedAt = in.CreatedAt
	out.UpdatedAt = in.UpdatedAt
}

// fillTeamDeckLite copies only the columns selected by ListDecksLite; Assets and
// TemplateModel are intentionally left zero because they were not read.
func fillTeamDeckLite(out *store.DeckManifest, in *teament.Deck) {
	out.ID = in.ID.String()
	out.Slug = in.Slug
	out.Title = in.Title
	out.Description = in.Description
	out.SchemaVersion = in.SchemaVersion
	out.Theme = in.Theme
	out.CreatedAt = in.CreatedAt
	out.UpdatedAt = in.UpdatedAt
}
func fillTeamSlide(out *store.SlideContent, in *teament.Slide) {
	out.ID = in.ID.String()
	if in.Edges.Deck != nil {
		out.DeckID = in.Edges.Deck.ID.String()
	} else if id, err := in.QueryDeck().OnlyID(context.Background()); err == nil {
		out.DeckID = id.String()
	}
	out.Position = in.Position
	out.Title = in.Title
	out.Content = in.Content
	out.Notes = in.Notes
	out.SchemaVersion = in.SchemaVersion
	out.CreatedAt = in.CreatedAt
	out.UpdatedAt = in.UpdatedAt
}

var _ store.DocsStore = (*teamDocsStore)(nil)
