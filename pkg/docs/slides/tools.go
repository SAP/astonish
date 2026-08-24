package slides

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// AssetInfo is one lightweight entry in a deck's image-asset catalog. It carries
// the identity the AI needs to reference or swap an image (Ref is the Assets map
// key, e.g. "sha256-<hex>", used verbatim as an ast-image asset-ref) plus small
// hints (MIME, approximate decoded Bytes, and a Kind heuristic). It DELIBERATELY
// never carries the base64 data: URI value — that heavy payload must stay out of
// the model context and off the wire (see TestSlidesResponsesOmitHeavyManifestFields).
type AssetInfo struct {
	Ref   string `json:"ref"`
	MIME  string `json:"mime,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// assetCatalog projects a deck's Assets map (ref -> data: URI) into a sorted,
// data-free catalog of AssetInfo. MIME is parsed from the "data:<mime>;base64,"
// prefix, Bytes is the approximate decoded size, and Kind is a heuristic hint
// ("logo" for SVG or very small images, else "image"). The data: value is never
// copied into the result. Order is deterministic (sorted by Ref).
func assetCatalog(assets map[string]string) []AssetInfo {
	if len(assets) == 0 {
		return nil
	}
	out := make([]AssetInfo, 0, len(assets))
	for ref, dataURI := range assets {
		// Embedded fonts share this Assets map (keyed "font:<family>:<variant>",
		// value data:font/...). They are NOT images: they must never appear in the
		// image catalog the AI browses, never be selectable as an ast-image
		// asset-ref, and never expose their heavy data: bytes. Skip them entirely.
		if strings.HasPrefix(ref, "font:") {
			continue
		}
		info := AssetInfo{Ref: ref, Kind: "image"}
		// Parse "data:<mime>;base64,<payload>" without retaining the payload.
		if strings.HasPrefix(dataURI, "data:") {
			rest := dataURI[len("data:"):]
			if semi := strings.IndexByte(rest, ';'); semi >= 0 {
				info.MIME = rest[:semi]
			} else if comma := strings.IndexByte(rest, ','); comma >= 0 {
				info.MIME = rest[:comma]
			}
			if comma := strings.IndexByte(rest, ','); comma >= 0 {
				payload := rest[comma+1:]
				// base64 decodes to ~3/4 of its length (ignoring padding).
				info.Bytes = len(payload) * 3 / 4
			}
		}
		if info.MIME == "image/svg+xml" || (info.Bytes > 0 && info.Bytes < 4096) {
			info.Kind = "logo"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

const (
	ActionDeckCreated  = "deck_created"
	ActionDeckViewed   = "deck_viewed"
	ActionSlideWritten = "slide_written"
)

// CreateDeckArgs defines the create_deck tool input.
type CreateDeckArgs struct {
	Slug        string            `json:"slug" jsonschema:"Stable URL-safe deck slug."`
	Title       string            `json:"title" jsonschema:"Human-readable deck title."`
	Description string            `json:"description,omitempty" jsonschema:"Optional short deck description."`
	Theme       map[string]string `json:"theme,omitempty" jsonschema:"Optional ASD theme token overrides."`
	Template    string            `json:"template,omitempty" jsonschema:"Optional template name from list_templates. Seeds a coherent theme + assets; reuse its title/section/content archetypes as starting markup."`
}

// DeckView is the slim deck projection returned by slide TOOL results. It drops
// the two heavy store.DeckManifest fields — the Assets map (base64 data: URIs)
// and the TemplateModel string (the multi-MB imported-template IR) — which
// would otherwise flood the model context on every create_deck/get_deck/
// list_decks/write_slide/validate_deck call. It keeps the small identity/theme
// fields the model actually reasons about, and replaces the heavy fields with a
// count/flag so the model still knows assets/an IR exist. The deck itself is
// unchanged in the store; only the returned view is slim.
type DeckView struct {
	ID               string            `json:"id"`
	Slug             string            `json:"slug"`
	Title            string            `json:"title"`
	Description      string            `json:"description,omitempty"`
	SchemaVersion    int               `json:"schemaVersion"`
	Theme            map[string]string `json:"theme,omitempty"`
	Scope            string            `json:"scope,omitempty"`
	AssetCount       int               `json:"assetCount,omitempty"`
	HasTemplateModel bool              `json:"hasTemplateModel,omitempty"`
	// Assets is the lightweight image catalog (ref/mime/bytes/kind hints, never
	// data: URIs). It is populated only on single-deck views (create_deck /
	// get_deck) via deckViewWithAssets so the AI can see which images a deck
	// carries; list_decks uses deckView (no catalog) to stay light.
	Assets []AssetInfo `json:"assets,omitempty"`
}

// deckView projects a store.DeckManifest to the slim DeckView WITHOUT the asset
// catalog (used by list_decks, which spans many decks).
func deckView(d *store.DeckManifest) *DeckView {
	if d == nil {
		return nil
	}
	return &DeckView{
		ID:               d.ID,
		Slug:             d.Slug,
		Title:            d.Title,
		Description:      d.Description,
		SchemaVersion:    d.SchemaVersion,
		Theme:            d.Theme,
		Scope:            d.Scope,
		AssetCount:       len(d.Assets),
		HasTemplateModel: d.TemplateModel != "",
	}
}

// deckViewWithAssets is deckView plus the lightweight image catalog. Use it for
// single-deck views (create_deck / get_deck / list_deck_assets) so the AI sees
// the deck's images (imported template media, logos, photos) it can reference or
// swap. The catalog carries hints only — never the base64 data: URIs.
func deckViewWithAssets(d *store.DeckManifest) *DeckView {
	v := deckView(d)
	if v == nil {
		return nil
	}
	if d != nil {
		v.Assets = assetCatalog(d.Assets)
	}
	return v
}

// deckViews maps a slice of manifests to slim views.
func deckViews(decks []*store.DeckManifest) []*DeckView {
	out := make([]*DeckView, 0, len(decks))
	for _, d := range decks {
		out = append(out, deckView(d))
	}
	return out
}

// DeckResult is the common deck payload returned by slide tools.
type DeckResult struct {
	Deck       *DeckView             `json:"deck"`
	Slides     []*store.SlideContent `json:"slides,omitempty"`
	SlideCount int                   `json:"slideCount"`
	Archetypes []themes.Archetype    `json:"archetypes,omitempty"`
}

// WriteSlideArgs defines the write_slide tool input.
type WriteSlideArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck to update."`
	Position int    `json:"position" jsonschema:"Zero-based slide position. Writing an occupied position replaces that slide."`
	Markup   string `json:"markup" jsonschema:"One complete validated ast-slide element using ASD v1 markup. Geometry x/y/w/h uses integer logical pixels on a fixed 1920x1080 canvas, never percentages or a 0-100 coordinate system. ast-text is plain text: use size, weight, font-token, and color-token attributes instead of Markdown markers."`
	Notes    string `json:"notes,omitempty" jsonschema:"Optional speaker notes. Use this field instead of embedding ast-notes in markup."`
}

// WriteSlideResult reports the persisted slide and all validation diagnostics.
type WriteSlideResult struct {
	Deck        *DeckView           `json:"deck"`
	Slide       *store.SlideContent `json:"slide"`
	SlideCount  int                 `json:"slideCount"`
	Diagnostics []Diagnostic        `json:"diagnostics,omitempty"`
}

// GetDeckArgs defines the get_deck tool input.
type GetDeckArgs struct {
	Slug string `json:"slug" jsonschema:"Deck slug to read."`
}

// ListDecksArgs defines the list_decks tool input.
type ListDecksArgs struct{}

// ListDecksResult contains the user's private decks.
type ListDecksResult struct {
	Decks []*DeckView `json:"decks"`
}

// ListTemplatesArgs defines the list_templates tool input.
type ListTemplatesArgs struct{}

// TemplateSummary is a lightweight catalog entry for a slide template. It
// deliberately omits the heavy archetype markup, theme tokens, and asset data
// URIs so listing every template does not flood the model context. The full
// payload (archetype markup + tokens + assets) is delivered only when a
// template is chosen, via create_deck's template argument.
type TemplateSummary struct {
	Name           string             `json:"name"`
	Label          string             `json:"label,omitempty"`
	Description    string             `json:"description,omitempty"`
	Scope          string             `json:"scope,omitempty"`
	ArchetypeKinds []string           `json:"archetypeKinds,omitempty"`
	Archetypes     []ArchetypeVariant `json:"archetypes,omitempty"`
}

// ArchetypeVariant names one fillable slide skeleton in a template: its Kind
// (role, e.g. title/section/content/agenda) plus a human-readable Label. A
// template may carry MULTIPLE variants per role (title, title-2, ...); the model
// uses Label to ask the user which to use. Each variant also reports its Tier
// ("fixed" brand chrome vs "flexible" content) and, for fixed chrome, the
// FillSlots (ast-text ids the AI may edit). Markup is deliberately omitted here.
type ArchetypeVariant struct {
	Kind      string   `json:"kind"`
	Label     string   `json:"label,omitempty"`
	Tier      string   `json:"tier,omitempty"`
	FillSlots []string `json:"fillSlots,omitempty"`
}

// ListTemplatesResult contains lightweight summaries of the available slide
// templates (built-in + saved). It carries no archetype markup; call create_deck
// with a template name to seed the full theme, assets, and archetypes.
type ListTemplatesResult struct {
	Templates []TemplateSummary `json:"templates"`
}

// ValidateDeckArgs defines the validate_deck tool input.
type ValidateDeckArgs struct {
	Slug string `json:"slug" jsonschema:"Deck slug to validate."`
}

// DeckDiagnostic associates one ASD diagnostic with its persisted slide.
type DeckDiagnostic struct {
	SlideID    string     `json:"slideId"`
	Position   int        `json:"position"`
	Diagnostic Diagnostic `json:"diagnostic"`
}

// ValidateDeckResult reports whether every persisted slide is valid ASD v1.
type ValidateDeckResult struct {
	Deck        *DeckView        `json:"deck"`
	SlideCount  int              `json:"slideCount"`
	Valid       bool             `json:"valid"`
	Diagnostics []DeckDiagnostic `json:"diagnostics,omitempty"`
}

func personalService(ctx context.Context) (Service, error) {
	svc := store.FromContext(ctx)
	if svc == nil || svc.PersonalDocs == nil {
		return Service{}, fmt.Errorf("personal docs store unavailable")
	}
	return Service{Store: svc.PersonalDocs}, nil
}

func createDeck(ctx context.Context, args CreateDeckArgs) (DeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return DeckResult{}, err
	}
	slug := strings.TrimSpace(args.Slug)
	title := strings.TrimSpace(args.Title)
	description := strings.TrimSpace(args.Description)

	if name := strings.TrimSpace(args.Template); name != "" {
		tmpl, ok := svc.resolveTemplate(ctx, name)
		if !ok {
			return DeckResult{}, fmt.Errorf("unknown template %q", name)
		}
		// Merge tokens: template tokens first, explicit args.Theme overlays win.
		merged := make(map[string]string, len(tmpl.Tokens)+len(args.Theme))
		for k, v := range tmpl.Tokens {
			merged[k] = v
		}
		for k, v := range args.Theme {
			merged[k] = v
		}
		deck, err := svc.CreateDeckWithAssets(ctx, slug, title, description, merged, tmpl.Assets)
		if err != nil {
			return DeckResult{}, err
		}
		return DeckResult{Deck: deckViewWithAssets(deck), Archetypes: tmpl.Archetypes}, nil
	}

	deck, err := svc.CreateDeck(ctx, slug, title, description, args.Theme)
	if err != nil {
		return DeckResult{}, err
	}
	return DeckResult{Deck: deckView(deck)}, nil
}

func writeSlide(ctx context.Context, args WriteSlideArgs) (WriteSlideResult, error) {
	if args.Position < 0 {
		return WriteSlideResult{}, fmt.Errorf("position must be zero or greater")
	}
	svc, err := personalService(ctx)
	if err != nil {
		return WriteSlideResult{}, err
	}
	slide, diagnostics, err := svc.WriteSlide(ctx, strings.TrimSpace(args.DeckSlug), args.Position, args.Markup, args.Notes)
	if err != nil {
		return WriteSlideResult{Diagnostics: diagnostics}, err
	}
	deck, slides, err := svc.Deck(ctx, args.DeckSlug)
	if err != nil {
		return WriteSlideResult{}, fmt.Errorf("reload deck: %w", err)
	}
	return WriteSlideResult{Deck: deckView(deck), Slide: slide, SlideCount: len(slides), Diagnostics: diagnostics}, nil
}

func getDeck(ctx context.Context, args GetDeckArgs) (DeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return DeckResult{}, err
	}
	deck, slides, err := svc.Deck(ctx, strings.TrimSpace(args.Slug))
	if err != nil {
		return DeckResult{}, err
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Position < slides[j].Position })
	return DeckResult{Deck: deckViewWithAssets(deck), Slides: slides, SlideCount: len(slides)}, nil
}

func listDecks(ctx context.Context, _ ListDecksArgs) (ListDecksResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ListDecksResult{}, err
	}
	decks, err := svc.ListDecks(ctx)
	if err != nil {
		return ListDecksResult{}, fmt.Errorf("list decks: %w", err)
	}
	return ListDecksResult{Decks: deckViews(decks)}, nil
}

func listTemplates(ctx context.Context, _ ListTemplatesArgs) (ListTemplatesResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ListTemplatesResult{}, err
	}
	summaries := make([]TemplateSummary, 0)
	seen := make(map[string]bool)
	for _, t := range themes.ListTemplates() {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		summaries = append(summaries, templateSummary(t, "builtin"))
	}
	scoped, err := svc.ListTemplates(ctx)
	if err != nil {
		return ListTemplatesResult{}, fmt.Errorf("list templates: %w", err)
	}
	for _, t := range scoped {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		summaries = append(summaries, templateSummary(t, "scope"))
	}
	return ListTemplatesResult{Templates: summaries}, nil
}

// templateSummary projects a full themes.Template down to the lightweight
// catalog entry returned by list_templates: identity plus the archetype KINDS
// (e.g. title/section/content) but never the archetype markup, tokens, or assets.
func templateSummary(t themes.Template, scope string) TemplateSummary {
	kinds := make([]string, 0, len(t.Archetypes))
	variants := make([]ArchetypeVariant, 0, len(t.Archetypes))
	for _, arch := range t.Archetypes {
		kinds = append(kinds, arch.Kind)
		variants = append(variants, ArchetypeVariant{Kind: arch.Kind, Label: arch.Title, Tier: arch.Tier, FillSlots: arch.FillSlots})
	}
	return TemplateSummary{
		Name:           t.Name,
		Label:          t.Label,
		Description:    t.Description,
		Scope:          scope,
		ArchetypeKinds: kinds,
		Archetypes:     variants,
	}
}

func validateDeck(ctx context.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
	deckResult, err := getDeck(ctx, GetDeckArgs{Slug: args.Slug})
	if err != nil {
		return ValidateDeckResult{}, err
	}
	result := ValidateDeckResult{Deck: deckResult.Deck, SlideCount: deckResult.SlideCount, Valid: true}
	for _, persisted := range deckResult.Slides {
		_, diagnostics, parseErr := ParseSlide(persisted.Content)
		if parseErr != nil {
			result.Valid = false
			result.Diagnostics = append(result.Diagnostics, DeckDiagnostic{
				SlideID: persisted.ID, Position: persisted.Position,
				Diagnostic: Diagnostic{Severity: "error", Code: "invalid_markup", Message: parseErr.Error()},
			})
			continue
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == "error" {
				result.Valid = false
			}
			result.Diagnostics = append(result.Diagnostics, DeckDiagnostic{SlideID: persisted.ID, Position: persisted.Position, Diagnostic: diagnostic})
		}
	}
	return result, nil
}

// ListDeckAssetsArgs defines the list_deck_assets tool input.
type ListDeckAssetsArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck whose image assets to list."`
}

// ListDeckAssetsResult is the deck's image-asset catalog (hints only, no data URIs).
type ListDeckAssetsResult struct {
	Deck   *DeckView   `json:"deck"`
	Assets []AssetInfo `json:"assets"`
}

// listDeckAssets returns the deck's image-asset catalog so the AI can discover
// which asset-refs exist (imported template media, logos, photos) to reference
// in an ast-image or to swap. It loads the full manifest (Assets present) and
// projects it to the data-free catalog.
func listDeckAssets(ctx context.Context, args ListDeckAssetsArgs) (ListDeckAssetsResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ListDeckAssetsResult{}, err
	}
	deck, _, err := svc.Deck(ctx, strings.TrimSpace(args.DeckSlug))
	if err != nil {
		return ListDeckAssetsResult{}, err
	}
	return ListDeckAssetsResult{Deck: deckView(deck), Assets: assetCatalog(deck.Assets)}, nil
}

// AddDeckImageArgs defines the add_deck_image tool input.
type AddDeckImageArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck to add the image to."`
	URL      string `json:"url" jsonschema:"Public https URL of an image to fetch and add to the deck asset library."`
	Alt      string `json:"alt,omitempty" jsonschema:"Optional alt text describing the image."`
}

// AddDeckImageResult reports the newly added asset's ref (usable directly as an
// ast-image asset-ref) plus MIME/size hints.
type AddDeckImageResult struct {
	Deck     *DeckView `json:"deck"`
	AssetRef string    `json:"assetRef"`
	MIME     string    `json:"mime"`
	Bytes    int       `json:"bytes"`
}

// newAssetIngestor builds the AssetIngestor used by add_deck_image. It is a
// package-level var so tests can inject a custom http.RoundTripper (the
// SSRF-protected default rejects the loopback addresses httptest servers bind
// to). Production always uses the zero-value ingestor with its safe defaults.
var newAssetIngestor = func() AssetIngestor { return AssetIngestor{} }

// addDeckImage fetches a public image URL through the SSRF-protected
// AssetIngestor (MIME/SVG validation, 20MB cap) and adds it to the deck's asset
// library, returning the content-addressed asset-ref the AI can put in an
// ast-image. The ref is "sha256-<hex>" to match the importer's key convention so
// resolveImageSrc lookups are uniform. Adding the same URL twice is idempotent.
func addDeckImage(ctx context.Context, args AddDeckImageArgs) (AddDeckImageResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return AddDeckImageResult{}, err
	}
	slug := strings.TrimSpace(args.DeckSlug)
	url := strings.TrimSpace(args.URL)
	if slug == "" || url == "" {
		return AddDeckImageResult{}, fmt.Errorf("deck_slug and url are required")
	}
	asset, err := newAssetIngestor().Fetch(ctx, url)
	if err != nil {
		return AddDeckImageResult{}, fmt.Errorf("fetch image: %w", err)
	}
	ref := "sha256-" + asset.ID
	dataURI := "data:" + asset.MIME + ";base64," + base64.StdEncoding.EncodeToString(asset.Bytes)
	deck, err := svc.AddDeckAsset(ctx, slug, ref, dataURI)
	if err != nil {
		return AddDeckImageResult{}, fmt.Errorf("add deck image: %w", err)
	}
	return AddDeckImageResult{Deck: deckViewWithAssets(deck), AssetRef: ref, MIME: asset.MIME, Bytes: len(asset.Bytes)}, nil
}

// GetTools returns the chat tools for authoring and inspecting private slide decks.
func GetTools() ([]tool.Tool, error) {
	specs := []struct {
		name        string
		description string
		newTool     func() (tool.Tool, error)
	}{
		{"create_deck", "Create a new private Astonish Slides deck before writing slides. Pass an optional template (see list_templates) to seed a coherent theme, assets, and starting archetypes tagged fixed|flexible; reproduce fixed chrome verbatim, editing only its fillSlots.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "create_deck", Description: "Create a new private Astonish Slides deck before writing slides. Pass an optional template (see list_templates) to seed a coherent theme, assets, and starting archetypes tagged fixed|flexible; reproduce fixed chrome verbatim, editing only its fillSlots."}, func(ctx tool.Context, args CreateDeckArgs) (DeckResult, error) {
				return createDeck(ctx, args)
			})
		}},
		{"write_slide", "Validate and write one complete ASD v1 ast-slide at a zero-based position in a private deck.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "write_slide", Description: "Validate and write one complete ASD v1 ast-slide at a zero-based position in a private deck."}, func(ctx tool.Context, args WriteSlideArgs) (WriteSlideResult, error) {
				return writeSlide(ctx, args)
			})
		}},
		{"get_deck", "Read a private slide deck and its ordered canonical ASD slide markup.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_deck", Description: "Read a private slide deck and its ordered canonical ASD slide markup."}, func(ctx tool.Context, args GetDeckArgs) (DeckResult, error) {
				return getDeck(ctx, args)
			})
		}},
		{"list_decks", "List private Astonish Slides decks available to the current user.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_decks", Description: "List private Astonish Slides decks available to the current user."}, func(ctx tool.Context, args ListDecksArgs) (ListDecksResult, error) {
				return listDecks(ctx, args)
			})
		}},
		{"list_templates", "List available slide templates (built-in + imported) as a lightweight catalog: each entry has name, label, description, scope, and archetype variants reporting {kind,label,tier,fillSlots} — no markup, tokens, or assets. Pass a template name to create_deck to seed the full theme + assets and receive the archetype markup to fill.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_templates", Description: "List available slide templates (built-in + imported) as a lightweight catalog: each entry has name, label, description, scope, and archetype variants reporting {kind,label,tier,fillSlots} — no markup, tokens, or assets. Pass a template name to create_deck to seed the full theme + assets and receive the archetype markup to fill."}, func(ctx tool.Context, args ListTemplatesArgs) (ListTemplatesResult, error) {
				return listTemplates(ctx, args)
			})
		}},
		{"validate_deck", "Validate every persisted slide in a private deck and return structured ASD diagnostics.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "validate_deck", Description: "Validate every persisted slide in a private deck and return structured ASD diagnostics."}, func(ctx tool.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
				return validateDeck(ctx, args)
			})
		}},
		{"list_deck_assets", "List the image assets already in a deck (imported template media, logos, photos) with their asset-ref ids; use an id as an ast-image asset-ref. Returns hints only (ref, mime, size, kind) — never the image data.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_deck_assets", Description: "List the image assets already in a deck (imported template media, logos, photos) with their asset-ref ids; use an id as an ast-image asset-ref. Returns hints only (ref, mime, size, kind) — never the image data."}, func(ctx tool.Context, args ListDeckAssetsArgs) (ListDeckAssetsResult, error) {
				return listDeckAssets(ctx, args)
			})
		}},
		{"add_deck_image", "Fetch a public https image URL and add it to the deck asset library; returns the asset-ref to reference in an ast-image. Use list_deck_assets first to see existing images and to swap one. The fetch is SSRF-protected and rejects non-image or private-network URLs.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "add_deck_image", Description: "Fetch a public https image URL and add it to the deck asset library; returns the asset-ref to reference in an ast-image. Use list_deck_assets first to see existing images and to swap one. The fetch is SSRF-protected and rejects non-image or private-network URLs."}, func(ctx tool.Context, args AddDeckImageArgs) (AddDeckImageResult, error) {
				return addDeckImage(ctx, args)
			})
		}},
	}

	out := make([]tool.Tool, 0, len(specs))
	for _, spec := range specs {
		t, err := spec.newTool()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.name, err)
		}
		out = append(out, t)
	}
	return out, nil
}
