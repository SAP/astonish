package entstore

import (
	"context"
	"errors"
	"fmt"

	personalent "github.com/SAP/astonish/ent/personal"
	personaldeck "github.com/SAP/astonish/ent/personal/deck"
	personalslide "github.com/SAP/astonish/ent/personal/slide"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type personalDocsStore struct{ client *personalent.Client }

func (s *personalDocsStore) CreateDeck(ctx context.Context, d *store.DeckManifest) error {
	id, err := docsUUID(d.ID)
	if err != nil {
		return err
	}
	row, err := s.client.Deck.Create().SetID(id).SetSlug(d.Slug).SetTitle(d.Title).SetDescription(d.Description).SetSchemaVersion(d.SchemaVersion).SetTheme(d.Theme).SetAssets(d.Assets).Save(ctx)
	if err == nil {
		fillPersonalDeck(d, row)
	}
	return err
}
func (s *personalDocsStore) GetDeck(ctx context.Context, slug string) (*store.DeckManifest, error) {
	row, err := s.client.Deck.Query().Where(personaldeck.SlugEQ(slug)).Only(ctx)
	if personalent.IsNotFound(err) {
		return nil, store.ErrDocsNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &store.DeckManifest{}
	fillPersonalDeck(d, row)
	return d, nil
}
func (s *personalDocsStore) ListDecks(ctx context.Context) ([]*store.DeckManifest, error) {
	rows, err := s.client.Deck.Query().Order(personalent.Desc(personaldeck.FieldUpdatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.DeckManifest, 0, len(rows))
	for _, row := range rows {
		d := &store.DeckManifest{}
		fillPersonalDeck(d, row)
		out = append(out, d)
	}
	return out, nil
}
func (s *personalDocsStore) UpdateDeck(ctx context.Context, d *store.DeckManifest) error {
	row, err := s.client.Deck.Query().Where(personaldeck.SlugEQ(d.Slug)).Only(ctx)
	if personalent.IsNotFound(err) {
		return store.ErrDocsNotFound
	}
	if err != nil {
		return err
	}
	row, err = row.Update().SetTitle(d.Title).SetDescription(d.Description).SetSchemaVersion(d.SchemaVersion).SetTheme(d.Theme).SetAssets(d.Assets).Save(ctx)
	if err == nil {
		fillPersonalDeck(d, row)
	}
	return err
}
func (s *personalDocsStore) DeleteDeck(ctx context.Context, slug string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	deckID, err := tx.Deck.Query().Where(personaldeck.SlugEQ(slug)).OnlyID(ctx)
	if personalent.IsNotFound(err) {
		return store.ErrDocsNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Slide.Delete().Where(personalslide.HasDeckWith(personaldeck.IDEQ(deckID))).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Deck.DeleteOneID(deckID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *personalDocsStore) UpsertSlide(ctx context.Context, in *store.SlideContent) error {
	deckID, err := uuid.Parse(in.DeckID)
	if err != nil {
		return fmt.Errorf("invalid deck id: %w", err)
	}
	id, err := docsUUID(in.ID)
	if err != nil {
		return err
	}
	row, err := s.client.Slide.Query().Where(personalslide.IDEQ(id), personalslide.HasDeckWith(personaldeck.IDEQ(deckID))).Only(ctx)
	if personalent.IsNotFound(err) {
		row, err = s.client.Slide.Create().SetID(id).SetDeckID(deckID).SetPosition(in.Position).SetTitle(in.Title).SetContent(in.Content).SetNotes(in.Notes).SetSchemaVersion(in.SchemaVersion).Save(ctx)
	} else if err == nil {
		row, err = row.Update().SetPosition(in.Position).SetTitle(in.Title).SetContent(in.Content).SetNotes(in.Notes).SetSchemaVersion(in.SchemaVersion).Save(ctx)
	}
	if err == nil {
		fillPersonalSlide(in, row)
	}
	return err
}
func (s *personalDocsStore) GetSlide(ctx context.Context, deckID, slideID string) (*store.SlideContent, error) {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	sid, err := uuid.Parse(slideID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	row, err := s.client.Slide.Query().Where(personalslide.IDEQ(sid), personalslide.HasDeckWith(personaldeck.IDEQ(did))).Only(ctx)
	if personalent.IsNotFound(err) {
		return nil, store.ErrDocsNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &store.SlideContent{}
	fillPersonalSlide(out, row)
	return out, nil
}
func (s *personalDocsStore) ListSlides(ctx context.Context, deckID string) ([]*store.SlideContent, error) {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return nil, store.ErrDocsNotFound
	}
	rows, err := s.client.Slide.Query().Where(personalslide.HasDeckWith(personaldeck.IDEQ(did))).WithDeck().Order(personalent.Asc(personalslide.FieldPosition)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.SlideContent, 0, len(rows))
	for _, row := range rows {
		v := &store.SlideContent{}
		fillPersonalSlide(v, row)
		out = append(out, v)
	}
	return out, nil
}
func (s *personalDocsStore) DeleteSlide(ctx context.Context, deckID, slideID string) error {
	did, e1 := uuid.Parse(deckID)
	sid, e2 := uuid.Parse(slideID)
	if e1 != nil || e2 != nil {
		return store.ErrDocsNotFound
	}
	n, err := s.client.Slide.Delete().Where(personalslide.IDEQ(sid), personalslide.HasDeckWith(personaldeck.IDEQ(did))).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrDocsNotFound
	}
	return nil
}
func (s *personalDocsStore) ReorderSlides(ctx context.Context, deckID string, ids []string) error {
	did, err := uuid.Parse(deckID)
	if err != nil {
		return store.ErrDocsNotFound
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Slide.Query().Where(personalslide.HasDeckWith(personaldeck.IDEQ(did))).All(ctx)
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
	if _, err := tx.Slide.Update().Where(personalslide.HasDeckWith(personaldeck.IDEQ(did))).AddPosition(temporaryPosition).Save(ctx); err != nil {
		return err
	}
	for pos, sid := range parsed {
		if _, err := tx.Slide.UpdateOneID(sid).SetPosition(pos).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func docsUUID(raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.New(), nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid id: %w", err)
	}
	return id, nil
}
func fillPersonalDeck(out *store.DeckManifest, in *personalent.Deck) {
	out.ID = in.ID.String()
	out.Slug = in.Slug
	out.Title = in.Title
	out.Description = in.Description
	out.SchemaVersion = in.SchemaVersion
	out.Theme = in.Theme
	out.Assets = in.Assets
	out.CreatedAt = in.CreatedAt
	out.UpdatedAt = in.UpdatedAt
}
func fillPersonalSlide(out *store.SlideContent, in *personalent.Slide) {
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

var _ store.DocsStore = (*personalDocsStore)(nil)
var _ = errors.Is
