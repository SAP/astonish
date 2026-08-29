package slides

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
)

const (
	ScopeBuiltin  = "builtin"
	ScopePlatform = "platform"
	ScopeOrg      = "org"
	ScopeTeam     = "team"
	ScopePersonal = "personal"
)

// TemplateCatalog reads imported templates from every tenant scope the caller
// can see. Built-ins are not stored here; callers merge themes.ListTemplates.
type TemplateCatalog struct {
	Platform store.SlideTemplateStore
	Org      store.SlideTemplateStore
	Team     store.SlideTemplateStore
	Personal store.SlideTemplateStore
}

// CatalogFromServices wires platform/org stores from Services and wraps
// team/personal DocsStores as template stores.
func CatalogFromServices(svc *store.Services) TemplateCatalog {
	if svc == nil {
		return TemplateCatalog{}
	}
	c := TemplateCatalog{
		Platform: svc.PlatformSlideTemplates,
		Org:      svc.OrgSlideTemplates,
	}
	if svc.Docs != nil {
		c.Team = DocsTemplateStore{Store: svc.Docs}
	}
	if svc.PersonalDocs != nil {
		c.Personal = DocsTemplateStore{Store: svc.PersonalDocs}
	}
	return c
}

func catalogFromContext(ctx context.Context) TemplateCatalog {
	return CatalogFromServices(store.FromContext(ctx))
}

func (c TemplateCatalog) store(scope string) store.SlideTemplateStore {
	switch scope {
	case ScopePlatform:
		return c.Platform
	case ScopeOrg:
		return c.Org
	case ScopeTeam:
		return c.Team
	case ScopePersonal:
		return c.Personal
	default:
		return nil
	}
}

// ListScope returns imported templates in one store, tagged with scope.
func (c TemplateCatalog) ListScope(ctx context.Context, scope string) ([]themes.Template, error) {
	st := c.store(scope)
	if st == nil {
		return nil, nil
	}
	recs, err := st.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]themes.Template, 0, len(recs))
	for i := range recs {
		t, err := TemplateFromRecord(&recs[i])
		if err != nil {
			return nil, err
		}
		t.Scope = scope
		out = append(out, t)
	}
	return out, nil
}

// ListAll returns built-ins plus every imported template (no name collapse).
func (c TemplateCatalog) ListAll(ctx context.Context) ([]themes.Template, error) {
	out := make([]themes.Template, 0)
	for _, t := range themes.ListTemplates() {
		t.Scope = ScopeBuiltin
		out = append(out, HydrateTemplateFonts(t))
	}
	for _, scope := range []string{ScopePlatform, ScopeOrg, ScopeTeam, ScopePersonal} {
		got, err := c.ListScope(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("list %s templates: %w", scope, err)
		}
		out = append(out, got...)
	}
	return out, nil
}

// ListResolved returns unique names: built-ins always win, otherwise
// personal > team > org > platform. Used by chat pickers whose option ids
// are template names.
func (c TemplateCatalog) ListResolved(ctx context.Context) ([]themes.Template, error) {
	all, err := c.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]themes.Template{}
	var builtins []themes.Template
	var order []string
	for _, t := range all {
		if t.Scope == ScopeBuiltin {
			builtins = append(builtins, t)
			byName[t.Name] = t
			continue
		}
		if existing, ok := byName[t.Name]; ok && existing.Scope == ScopeBuiltin {
			continue
		}
		if _, ok := byName[t.Name]; !ok {
			order = append(order, t.Name)
		}
		byName[t.Name] = t
	}
	out := append([]themes.Template{}, builtins...)
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// Resolve looks up a template by name: built-ins always win, otherwise
// personal > team > org > platform.
func (c TemplateCatalog) Resolve(ctx context.Context, name string) (themes.Template, bool, error) {
	name = themes.CanonicalTemplateName(name)
	if name == "" {
		return themes.Template{}, false, nil
	}
	if t, ok := themes.LookupTemplate(name); ok {
		t.Scope = ScopeBuiltin
		return HydrateTemplateFonts(t), true, nil
	}
	for _, scope := range []string{ScopePersonal, ScopeTeam, ScopeOrg, ScopePlatform} {
		st := c.store(scope)
		if st == nil {
			continue
		}
		rec, err := st.Get(ctx, name)
		if err != nil {
			return themes.Template{}, false, err
		}
		if rec == nil {
			continue
		}
		t, err := TemplateFromRecord(rec)
		if err != nil {
			return themes.Template{}, false, err
		}
		t.Scope = scope
		return t, true, nil
	}
	return themes.Template{}, false, nil
}

// Save writes rec into the given scope's store.
func (c TemplateCatalog) Save(ctx context.Context, scope string, t themes.Template) error {
	st := c.store(scope)
	if st == nil {
		return fmt.Errorf("%s slide template store unavailable", scope)
	}
	return st.Save(ctx, RecordFromTemplate(t))
}

// Delete removes name from the given scope's store. Missing names are ok.
func (c TemplateCatalog) Delete(ctx context.Context, scope, name string) error {
	st := c.store(scope)
	if st == nil {
		return fmt.Errorf("%s slide template store unavailable", scope)
	}
	return st.Delete(ctx, name)
}

// DocsTemplateStore persists templates as hidden tmpl/ decks on a DocsStore.
type DocsTemplateStore struct {
	Store store.DocsStore
}

func (d DocsTemplateStore) Save(ctx context.Context, rec *store.SlideTemplateRecord) error {
	t, err := TemplateFromRecord(rec)
	if err != nil {
		return err
	}
	return (Service{Store: d.Store}).SaveTemplate(ctx, t)
}

func (d DocsTemplateStore) Get(ctx context.Context, name string) (*store.SlideTemplateRecord, error) {
	t, ok, err := (Service{Store: d.Store}).Template(ctx, name)
	if err != nil || !ok {
		return nil, err
	}
	return RecordFromTemplate(t), nil
}

func (d DocsTemplateStore) List(ctx context.Context) ([]store.SlideTemplateRecord, error) {
	tmpls, err := (Service{Store: d.Store}).ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.SlideTemplateRecord, 0, len(tmpls))
	for _, t := range tmpls {
		rec := RecordFromTemplate(t)
		if rec != nil {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (d DocsTemplateStore) Delete(ctx context.Context, name string) error {
	svc := Service{Store: d.Store}
	return svc.DeleteDeck(ctx, svc.TemplateSlug(name))
}

// RecordFromTemplate projects a themes.Template into a persistable record.
func RecordFromTemplate(t themes.Template) *store.SlideTemplateRecord {
	rec := &store.SlideTemplateRecord{
		Name:          t.Name,
		Label:         t.Label,
		Description:   t.Description,
		SchemaVersion: t.Schema,
		Skin:          t.Skin,
		Tokens:        t.Tokens,
		Assets:        t.Assets,
	}
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = SchemaV2
	}
	if t.Model != nil {
		raw, err := json.Marshal(t.Model)
		if err == nil {
			rec.TemplateModel = string(raw)
			rec.SchemaVersion = SchemaV3
		}
	}
	if len(t.Palettes) > 0 {
		if raw, err := json.Marshal(t.Palettes); err == nil {
			rec.Palettes = raw
		}
	}
	if len(t.Archetypes) > 0 {
		if raw, err := json.Marshal(t.Archetypes); err == nil {
			rec.Archetypes = raw
		}
	}
	return rec
}

// TemplateFromRecord rebuilds a themes.Template from a persisted record.
func TemplateFromRecord(rec *store.SlideTemplateRecord) (themes.Template, error) {
	if rec == nil {
		return themes.Template{}, fmt.Errorf("nil slide template record")
	}
	t := themes.Template{
		Schema:      rec.SchemaVersion,
		Name:        rec.Name,
		Label:       rec.Label,
		Description: rec.Description,
		Skin:        rec.Skin,
		Tokens:      rec.Tokens,
		Assets:      rec.Assets,
	}
	if t.Schema == 0 {
		t.Schema = SchemaV2
	}
	if len(rec.Palettes) > 0 {
		if err := json.Unmarshal(rec.Palettes, &t.Palettes); err != nil {
			return themes.Template{}, fmt.Errorf("decode palettes: %w", err)
		}
	}
	if len(rec.Archetypes) > 0 {
		if err := json.Unmarshal(rec.Archetypes, &t.Archetypes); err != nil {
			return themes.Template{}, fmt.Errorf("decode archetypes: %w", err)
		}
	}
	if rec.TemplateModel != "" {
		var model themes.TemplateModel
		if err := json.Unmarshal([]byte(rec.TemplateModel), &model); err == nil {
			t.Model = &model
			t.StyleGuide = styleGuideFromModel(&model)
		}
	}
	return t, nil
}
