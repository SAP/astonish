package slides

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
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
	// ActionDeckReviewed is emitted when the model runs review_deck, its
	// declared FINAL step. The pkg/api trigger uses this to kick off
	// best-effort slide thumbnail baking.
	ActionDeckReviewed = "deck_reviewed"
)

// CreateDeckArgs defines the create_deck tool input.
type CreateDeckArgs struct {
	Slug        string            `json:"slug" jsonschema:"Hint only. In chat the server persists a unique per-session slug (s-<sessionID>) and returns it — use that in later calls. Two chats on the same topic must not share a persist slug."`
	Title       string            `json:"title" jsonschema:"Human-readable deck title (shown in the chat Slides card)."`
	Description string            `json:"description,omitempty" jsonschema:"Short deck description shown under Slides in chat. Prefer this over the persist slug for humans."`
	Theme       map[string]string `json:"theme,omitempty" jsonschema:"Optional ASD theme token overrides. Prefer palette for Product Deck colorways instead of copying hex."`
	Template    string            `json:"template,omitempty" jsonschema:"Template name from list_slide_templates after intake (user named it, delegated, or picked from slidesTemplatePicker). Seeds theme, fonts, and a slim catalog. Author with fill_slides; do not copy markup."`
	Palette     string            `json:"palette,omitempty" jsonschema:"Optional palette id from the template palettes list (e.g. orange, light-violet). Overlays surface/ink/accent/muted. Prefer this over copying hex into theme."`
	TitleKind   string            `json:"titleKind,omitempty" jsonschema:"Required when the template has 2+ title* covers: the ask_user option id (title, title-2, …) or 'default' after they saw the picker. create_deck errors if this is omitted."`
	TitleImage  string            `json:"titleImage,omitempty" jsonschema:"Required when the chosen cover has a ph-pic well: slidesImagePicker option id, or 'none' if they declined. Omit only when the cover has no image well."`
	ClosingKind string            `json:"closingKind,omitempty" jsonschema:"Official closing variant kind (closing, closing-2, …). Last slide must use this kind when the catalog lists closing family."`
	Source      string            `json:"source,omitempty" jsonschema:"Optional slug of an existing saved deck to clone. Copies theme, assets, and slides into this new session deck and sets source_slug so Save can offer Override Original."`
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
	Deck         *DeckView               `json:"deck"`
	Slides       []*store.SlideContent   `json:"slides,omitempty"`
	SlideCount   int                     `json:"slideCount"`
	Archetypes   []themes.Archetype      `json:"archetypes,omitempty"`
	Catalog      []ArchetypeCatalogEntry `json:"catalog,omitempty"`
	Palettes     []PaletteInfo           `json:"palettes,omitempty"`
	StyleGuide   string                  `json:"styleGuide,omitempty"`
	Instructions string                  `json:"instructions,omitempty"`
	// SlideIndex is the slim get_deck listing (id/position/title, no markup).
	SlideIndex []SlideInfo `json:"slideIndex,omitempty"`
}

// PaletteInfo is a lightweight colorway entry (id + label, no tokens) so the
// model knows to call ask_user slidesPalettePicker without copying hex.
type PaletteInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SlideInfo is one slide's identity without canonical markup.
type SlideInfo struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Title    string `json:"title,omitempty"`
}

// FillSlideArgs defines the fill_slide tool input: pick a catalog entry and
// supply per-slot text (or an asset-ref for image slots). The server copies
// the stored archetype markup and substitutes — the model never reprints chrome.
type FillSlideArgs struct {
	DeckSlug string            `json:"deck_slug" jsonschema:"Persist slug from create_deck, or the hint — in chat the server remaps a hint onto this session's draft."`
	Position int               `json:"position" jsonschema:"Zero-based slide position. Writing an occupied position replaces that slide."`
	Kind     string            `json:"kind,omitempty" jsonschema:"Archetype kind from the create_deck catalog (e.g. title, pattern-2)."`
	Label    string            `json:"label,omitempty" jsonschema:"Archetype label from the catalog (e.g. '3 rounded cards'). Used when kind is omitted."`
	Template string            `json:"template,omitempty" jsonschema:"Template name. Optional when create_deck stamped it on the deck."`
	Fills    map[string]string `json:"fills" jsonschema:"Map of fillSlots id to text (or sha256- asset-ref for ph-pic image slots)."`
	Notes    string            `json:"notes,omitempty" jsonschema:"Optional speaker notes."`
}

// FillSlideResult reports the persisted slide without echoing markup.
type FillSlideResult struct {
	Deck        *DeckView    `json:"deck"`
	Position    int          `json:"position"`
	SlideID     string       `json:"slideId,omitempty"`
	Filled      []string     `json:"filled,omitempty"`
	SlideCount  int          `json:"slideCount"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// FillSlideSpec is one slide in a fill_slides batch.
type FillSlideSpec struct {
	Position int               `json:"position" jsonschema:"Zero-based slide position. Writing an occupied position replaces that slide."`
	Kind     string            `json:"kind,omitempty" jsonschema:"Archetype kind from the create_deck catalog (e.g. title, pattern-2)."`
	Label    string            `json:"label,omitempty" jsonschema:"Archetype label from the catalog when kind is omitted."`
	Fills    map[string]string `json:"fills" jsonschema:"Map of fillSlots id to text (or sha256- asset-ref for ph-pic image slots)."`
	Notes    string            `json:"notes,omitempty" jsonschema:"Optional speaker notes."`
}

// FillSlidesArgs writes many slides in one tool call so the model does not pay
// an LLM round-trip per slide.
type FillSlidesArgs struct {
	DeckSlug string          `json:"deck_slug" jsonschema:"Persist slug from create_deck, or the hint — in chat the server remaps a hint onto this session's draft."`
	Slides   []FillSlideSpec `json:"slides" jsonschema:"Every slide to write in this call. Include the whole deck; do not emit one fill_slide per turn."`
}

// FillSlidesResult reports the batch without echoing markup.
type FillSlidesResult struct {
	Deck        *DeckView    `json:"deck"`
	SlideCount  int          `json:"slideCount"`
	Positions   []int        `json:"positions,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

const maxFillSlides = 40

// GetArchetypeArgs fetches one archetype's markup (escape hatch; prefer fill_slide).
type GetArchetypeArgs struct {
	Template string `json:"template" jsonschema:"Template name from list_slide_templates / create_deck."`
	Kind     string `json:"kind,omitempty" jsonschema:"Archetype kind to fetch."`
	Label    string `json:"label,omitempty" jsonschema:"Archetype label to fetch when kind is omitted."`
}

// GetArchetypeResult returns a single archetype including markup.
type GetArchetypeResult struct {
	Kind      string            `json:"kind"`
	Label     string            `json:"label,omitempty"`
	Tier      string            `json:"tier,omitempty"`
	FillSlots []string          `json:"fillSlots,omitempty"`
	SlotHints []themes.SlotHint `json:"slotHints,omitempty"`
	Markup    string            `json:"markup"`
}

// WriteSlideArgs defines the write_slide tool input.
type WriteSlideArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck to update."`
	Position int    `json:"position" jsonschema:"Zero-based slide position. Writing an occupied position replaces that slide."`
	Markup   string `json:"markup" jsonschema:"One complete validated ast-slide element. When a template was used, prefer fill_slide instead of writing markup. Geometry x/y/w/h uses integer logical pixels on a fixed 1920x1080 canvas, never percentages or a 0-100 coordinate system. ast-text is plain text: use size, weight, font-token, and color-token attributes instead of Markdown markers."`
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
	Palettes       []PaletteInfo      `json:"palettes,omitempty"`
	HasStyleGuide  bool               `json:"hasStyleGuide,omitempty"`
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

// TemplateVariantPreviewsArgs defines the get_template_variant_previews input.
type TemplateVariantPreviewsArgs struct {
	Template string `json:"template" jsonschema:"Template name (from list_slide_templates) to fetch variant previews for."`
	Kind     string `json:"kind" jsonschema:"Archetype role to filter by (title, section, agenda, closing, content, pattern). Required — omitting it used to dump every variant's markup."`
}

// TemplateVariantPreview is one archetype variant plus the render inputs a UI
// needs to show a live thumbnail: the ast-slide Markup and the template Theme
// tokens + Assets it references. This carries ASD text and asset-refs only — it
// never embeds data: image/font bytes (those resolve through the deck asset
// plumbing at render time), so it is safe to return from a tool.
type TemplateVariantPreview struct {
	Kind         string   `json:"kind"`
	Label        string   `json:"label,omitempty"`
	Tier         string   `json:"tier,omitempty"`
	FillSlots    []string `json:"fillSlots,omitempty"`
	ThumbnailRef string   `json:"thumbnailRef,omitempty"`
}

// TemplateVariantPreviewsResult carries the per-variant preview markup plus the
// shared theme so a caller can render each variant. It DELIBERATELY omits the
// asset map (asset-ref -> data URI): returning image/font bytes here would bloat
// the model's history. The frontend resolves asset-refs from the template by
// name at render time via the slides API instead.
type TemplateVariantPreviewsResult struct {
	Template string                   `json:"template"`
	Theme    map[string]string        `json:"theme,omitempty"`
	Variants []TemplateVariantPreview `json:"variants"`
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

	// Source clone: copy an existing saved deck into a new session-scoped deck.
	if src := strings.TrimSpace(args.Source); src != "" {
		srcDeck, srcSlides, err := svc.Deck(ctx, src)
		if err != nil {
			return DeckResult{}, fmt.Errorf("source deck %q: %w", src, err)
		}
		if title == "" {
			title = srcDeck.Title
		}
		if description == "" {
			description = srcDeck.Description
		}
		// Ensure the slug doesn't collide with the source deck.
		if slug == "" || slug == src {
			slug = src + "-draft"
		}
		slug = resolvePersonalDeckSlug(ctx, svc, slug)
		theme := srcDeck.Theme
		if len(args.Theme) > 0 {
			theme = args.Theme
		}
		deck, err := svc.CreateDeckWithAssets(ctx, slug, title, description, theme, srcDeck.Assets)
		if err != nil {
			return DeckResult{}, fmt.Errorf("create deck copy %q from source %q: %w", slug, src, err)
		}
		// Preserve the source deck's schema version, template model, and thumbnail
		// state so the copied slides render correctly and thumbnails display.
		deck.SchemaVersion = srcDeck.SchemaVersion
		deck.ThumbnailReady = srcDeck.ThumbnailReady
		if srcDeck.TemplateModel != "" {
			deck.TemplateModel = srcDeck.TemplateModel
		}
		// Tag with source_slug so Save can offer Override Original.
		deck.SourceSlug = src
		if err := svc.Store.UpdateDeck(ctx, deck); err != nil {
			return DeckResult{}, fmt.Errorf("set source_slug: %w", err)
		}
		// Copy slides from source.
		for _, slide := range srcSlides {
			item := &store.SlideContent{
				ID: "", DeckID: deck.ID, Position: slide.Position,
				Title: slide.Title, Content: slide.Content, Notes: slide.Notes,
				SchemaVersion: slide.SchemaVersion, ThumbnailRef: slide.ThumbnailRef,
			}
			if err := svc.Store.UpsertSlide(ctx, item); err != nil {
				return DeckResult{}, fmt.Errorf("copy slide %d: %w", slide.Position, err)
			}
		}
		slides, _ := svc.Store.ListSlides(ctx, deck.ID)
		return DeckResult{Deck: deckViewWithAssets(deck), Slides: slides, SlideCount: len(slides)}, nil
	}

	if name := strings.TrimSpace(args.Template); name != "" {
		tmpl, ok := svc.resolveTemplate(ctx, name)
		if !ok {
			return DeckResult{}, fmt.Errorf("unknown template %q", name)
		}
		// Merge tokens: template tokens, then named palette, then explicit theme.
		merged := make(map[string]string, len(tmpl.Tokens)+len(args.Theme)+8)
		for k, v := range tmpl.Tokens {
			merged[k] = v
		}
		if err := applyPaletteTokens(tmpl, args.Palette, merged); err != nil {
			return DeckResult{}, err
		}
		for k, v := range args.Theme {
			merged[k] = v
		}
		merged[themeKeyTemplateName] = tmpl.Name
		if err := requireBookendIntake(tmpl, args); err != nil {
			return DeckResult{}, err
		}
		stampBookendKinds(tmpl, args, merged)
		slug = resolvePersonalDeckSlug(ctx, svc, slug)
		deck, err := svc.CreateDeckWithAssets(ctx, slug, title, description, merged, seedLightweightAssets(tmpl.Assets))
		if err != nil {
			return DeckResult{}, err
		}
		view := deckView(deck)
		view.Assets = assetCatalog(tmpl.Assets)
		view.AssetCount = len(view.Assets)
		result := DeckResult{Deck: view, Catalog: catalogFromTemplate(tmpl), Palettes: paletteInfos(tmpl)}
		if len(result.Catalog) > 0 {
			result.Instructions = createDeckCatalogInstructions(tmpl, merged)
		}
		result.StyleGuide = RecipeGuideMarkdown()
		if tmpl.StyleGuide != nil && tmpl.StyleGuide.Markdown != "" {
			result.StyleGuide += tmpl.StyleGuide.Markdown
		}
		return result, nil
	}

	slug = resolvePersonalDeckSlug(ctx, svc, slug)
	deck, err := svc.CreateDeck(ctx, slug, title, description, args.Theme)
	if err != nil {
		return DeckResult{}, err
	}
	return DeckResult{Deck: deckView(deck)}, nil
}

// sessionDraftSlug is the stable persist key for a chat session's working deck.
// One draft per session so two chats on the same topic cannot overwrite each other.
func sessionDraftSlug(sessionID string) string {
	return "s-" + strings.TrimSpace(sessionID)
}

// resolvePersonalDeckSlug picks the persist slug for create_deck.
// In a chat session the deck is always stored as s-<sessionID>. A retry in the
// SAME session replaces that leftover. Another session's draft and saved decks
// are never deleted.
func resolvePersonalDeckSlug(ctx context.Context, svc Service, requested string) string {
	sid := strings.TrimSpace(store.SessionIDFromContext(ctx))
	slug := strings.TrimSpace(requested)
	if sid != "" {
		slug = sessionDraftSlug(sid)
	}
	if slug == "" {
		slug = "deck"
	}
	existing, _ := svc.Store.GetDeck(ctx, slug)
	if existing == nil {
		return slug
	}
	if sid != "" && existing.SessionID == sid {
		_ = svc.Store.DeleteDeck(ctx, slug)
		return slug
	}
	h := sha256.Sum256([]byte(slug + "|" + sid + "|" + requested))
	return slug + "-" + fmt.Sprintf("%x", h[:3])
}

// resolveWorkingDeckSlug maps a model-supplied hint onto this chat's draft.
// create_deck persists s-<sessionID>, but the model often still passes the
// human hint (e.g. steve-jobs-life). Prefer the session draft when it exists
// so fill/write never touch another session's deck or a saved deck that
// happens to share the hint.
func resolveWorkingDeckSlug(ctx context.Context, svc Service, requested string) string {
	requested = strings.TrimSpace(requested)
	sid := strings.TrimSpace(store.SessionIDFromContext(ctx))
	if sid != "" {
		draft := sessionDraftSlug(sid)
		if d, err := svc.Store.GetDeck(ctx, draft); err == nil && d != nil {
			return draft
		}
		if requested != "" {
			if d, err := svc.Store.GetDeck(ctx, requested); err == nil && d != nil {
				if d.SessionID == sid || d.SessionID == "" {
					return requested
				}
			}
		}
		return draft
	}
	return requested
}

func fillSlide(ctx context.Context, args FillSlideArgs) (FillSlideResult, error) {
	batch, lastID, lastFilled, err := applyFills(ctx, args.DeckSlug, args.Template, []FillSlideSpec{{
		Position: args.Position,
		Kind:     args.Kind,
		Label:    args.Label,
		Fills:    args.Fills,
		Notes:    args.Notes,
	}})
	if err != nil {
		return FillSlideResult{Deck: batch.Deck, Position: args.Position, SlideCount: batch.SlideCount, Diagnostics: batch.Diagnostics}, err
	}
	return FillSlideResult{
		Deck:        batch.Deck,
		Position:    args.Position,
		SlideID:     lastID,
		Filled:      lastFilled,
		SlideCount:  batch.SlideCount,
		Diagnostics: batch.Diagnostics,
	}, nil
}

func fillSlides(ctx context.Context, args FillSlidesArgs) (FillSlidesResult, error) {
	batch, _, _, err := applyFills(ctx, args.DeckSlug, "", args.Slides)
	return batch, err
}

func applyFills(ctx context.Context, deckSlug, templateName string, specs []FillSlideSpec) (FillSlidesResult, string, []string, error) {
	empty := FillSlidesResult{}
	if len(specs) == 0 {
		return empty, "", nil, fmt.Errorf("slides is required")
	}
	if len(specs) > maxFillSlides {
		return empty, "", nil, fmt.Errorf("at most %d slides per fill_slides call", maxFillSlides)
	}
	seen := make(map[int]bool, len(specs))
	for i, spec := range specs {
		if spec.Position < 0 {
			return empty, "", nil, fmt.Errorf("slides[%d]: position must be zero or greater", i)
		}
		if seen[spec.Position] {
			return empty, "", nil, fmt.Errorf("duplicate position %d in fill_slides", spec.Position)
		}
		seen[spec.Position] = true
		if len(spec.Fills) == 0 {
			return empty, "", nil, fmt.Errorf("slides[%d]: fills is required", i)
		}
	}
	svc, err := personalService(ctx)
	if err != nil {
		return empty, "", nil, err
	}
	deckSlug = resolveWorkingDeckSlug(ctx, svc, deckSlug)
	deck, _, err := svc.Deck(ctx, deckSlug)
	if err != nil {
		return empty, "", nil, err
	}
	tmplName := strings.TrimSpace(templateName)
	if tmplName == "" && deck != nil && deck.Theme != nil {
		tmplName = strings.TrimSpace(deck.Theme[themeKeyTemplateName])
	}
	if tmplName == "" {
		return empty, "", nil, fmt.Errorf("template is required (pass template or create the deck with create_deck)")
	}
	tmpl, ok := svc.resolveTemplate(ctx, tmplName)
	if !ok {
		return empty, "", nil, fmt.Errorf("unknown template %q", tmplName)
	}
	if deck != nil {
		tmpl = overlayDeckTheme(tmpl, deck.Theme)
	}

	type prepared struct {
		spec   FillSlideSpec
		markup string
		filled []string
	}
	items := make([]prepared, 0, len(specs))
	total := 0
	for _, spec := range specs {
		if spec.Position+1 > total {
			total = spec.Position + 1
		}
	}
	skin := SkinFor(tmpl)
	lastPos := 0
	for _, spec := range specs {
		if spec.Position > lastPos {
			lastPos = spec.Position
		}
	}
	for i, spec := range specs {
		spec = applyStampedBookendKind(spec, deck, lastPos)
		arch, err := findTemplateArchetype(tmpl, spec.Kind, spec.Label)
		if err != nil {
			return empty, "", nil, fmt.Errorf("slides[%d]: %w", i, err)
		}
		if isRecipeKind(arch.Kind) {
			chrome := ExtractChrome(tmpl)
			if deck != nil {
				chrome.DeckTitle = strings.TrimSpace(deck.Title)
			}
			chrome.Page = spec.Position + 1
			chrome.Total = total
			arch, err = recipeArchetypeFor(tmpl, arch.Kind, chrome, spec.Fills)
			if err != nil {
				return empty, "", nil, fmt.Errorf("slides[%d]: %w", i, err)
			}
		}
		fills := aliasOfficialBookendFills(arch, spec.Fills)
		titleImage := ""
		if deck != nil && deck.Theme != nil {
			titleImage = deck.Theme[themeKeyTitleImage]
		}
		fills = applyCoverPhotoFill(arch, fills, titleImage)
		if miss := missingTextSlotFills(arch, fills); len(miss) > 0 {
			return empty, "", nil, fmt.Errorf("slides[%d]: missing fills for text slots %s — fill every required slot (or pick a recipe with fewer items)", i, strings.Join(miss, ", "))
		}
		markup, err := fillArchetypeMarkup(arch.Markup, fills)
		if err != nil {
			return empty, "", nil, fmt.Errorf("slides[%d]: %w", i, err)
		}
		if isOfficialBookendKind(arch.Kind) {
			filledPhotos := make(map[string]bool)
			for id, v := range fills {
				if isImageFillSlot(arch, id) && strings.TrimSpace(v) != "" {
					filledPhotos[id] = true
				}
			}
			markup = stripUnselectedHeroPhotos(markup, filledPhotos, mutedColor(tmpl.Tokens))
		}
		if isRecipeKind(arch.Kind) {
			markup = applyAccentSpans(markup, fills, skin.Accent)
		}
		filled := make([]string, 0, len(fills))
		for id := range fills {
			filled = append(filled, id)
		}
		sort.Strings(filled)
		items = append(items, prepared{spec: spec, markup: markup, filled: filled})
	}

	extra := make(map[string]string)
	for _, it := range items {
		for _, ref := range collectAssetRefs(it.markup) {
			if v, ok := tmpl.Assets[ref]; ok {
				extra[ref] = v
			}
		}
	}
	if _, err := svc.mergeDeckAssets(ctx, deckSlug, extra); err != nil {
		return empty, "", nil, err
	}

	var diagnostics []Diagnostic
	var lastSlide *store.SlideContent
	positions := make([]int, 0, len(items))
	for i, it := range items {
		slide, diags, err := svc.WriteSlide(ctx, deckSlug, it.spec.Position, it.markup, it.spec.Notes)
		diagnostics = append(diagnostics, diags...)
		if err != nil {
			return FillSlidesResult{Diagnostics: diagnostics, Positions: positions}, "", nil, fmt.Errorf("slides[%d]: %w", i, err)
		}
		lastSlide = slide
		positions = append(positions, it.spec.Position)
	}
	if err := svc.syncDeckAssetsFromTemplate(ctx, deckSlug, tmpl); err != nil {
		return FillSlidesResult{Diagnostics: diagnostics, Positions: positions}, "", nil, err
	}
	deck, slides, err := svc.Deck(ctx, deckSlug)
	if err != nil {
		return empty, "", nil, fmt.Errorf("reload deck: %w", err)
	}
	slideID := ""
	if lastSlide != nil {
		slideID = lastSlide.ID
	}
	return FillSlidesResult{
		Deck:        deckView(deck),
		SlideCount:  len(slides),
		Positions:   positions,
		Diagnostics: diagnostics,
	}, slideID, items[len(items)-1].filled, nil
}

func getArchetype(ctx context.Context, args GetArchetypeArgs) (GetArchetypeResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return GetArchetypeResult{}, err
	}
	name := strings.TrimSpace(args.Template)
	if name == "" {
		return GetArchetypeResult{}, fmt.Errorf("template is required")
	}
	tmpl, ok := svc.resolveTemplate(ctx, name)
	if !ok {
		return GetArchetypeResult{}, fmt.Errorf("unknown template %q", name)
	}
	arch, err := findTemplateArchetype(tmpl, args.Kind, args.Label)
	if err != nil {
		return GetArchetypeResult{}, err
	}
	return GetArchetypeResult{
		Kind:      arch.Kind,
		Label:     arch.Title,
		Tier:      arch.Tier,
		FillSlots: arch.FillSlots,
		SlotHints: arch.SlotHints,
		Markup:    arch.Markup,
	}, nil
}

func writeSlide(ctx context.Context, args WriteSlideArgs) (WriteSlideResult, error) {
	if args.Position < 0 {
		return WriteSlideResult{}, fmt.Errorf("position must be zero or greater")
	}
	svc, err := personalService(ctx)
	if err != nil {
		return WriteSlideResult{}, err
	}
	slug := resolveWorkingDeckSlug(ctx, svc, args.DeckSlug)
	slide, diagnostics, err := svc.WriteSlide(ctx, slug, args.Position, args.Markup, args.Notes)
	if err != nil {
		return WriteSlideResult{Diagnostics: diagnostics}, err
	}
	deck, slides, err := svc.Deck(ctx, slug)
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
	deck, slides, err := svc.Deck(ctx, resolveWorkingDeckSlug(ctx, svc, args.Slug))
	if err != nil {
		return DeckResult{}, err
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Position < slides[j].Position })
	index := make([]SlideInfo, 0, len(slides))
	for _, s := range slides {
		index = append(index, SlideInfo{ID: s.ID, Position: s.Position, Title: s.Title})
	}
	return DeckResult{Deck: deckViewWithAssets(deck), SlideIndex: index, SlideCount: len(slides)}, nil
}

// ReadSlideArgs defines the read_slide tool input.
type ReadSlideArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Slug of the deck."`
	Position int    `json:"position" jsonschema:"Zero-based slide position to read."`
}

func readSlide(ctx context.Context, args ReadSlideArgs) (WriteSlideResult, error) {
	if args.Position < 0 {
		return WriteSlideResult{}, fmt.Errorf("position must be zero or greater")
	}
	svc, err := personalService(ctx)
	if err != nil {
		return WriteSlideResult{}, err
	}
	deck, slides, err := svc.Deck(ctx, resolveWorkingDeckSlug(ctx, svc, args.DeckSlug))
	if err != nil {
		return WriteSlideResult{}, err
	}
	for _, s := range slides {
		if s.Position == args.Position {
			return WriteSlideResult{Deck: deckView(deck), Slide: s, SlideCount: len(slides)}, nil
		}
	}
	return WriteSlideResult{}, fmt.Errorf("no slide at position %d", args.Position)
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
		Palettes:       paletteInfos(t),
		HasStyleGuide:  t.StyleGuide != nil,
	}
}

func paletteInfos(t themes.Template) []PaletteInfo {
	if len(t.Palettes) == 0 {
		return nil
	}
	out := make([]PaletteInfo, 0, len(t.Palettes))
	for _, p := range t.Palettes {
		out = append(out, PaletteInfo{ID: p.ID, Label: p.Label})
	}
	return out
}

func applyPaletteTokens(tmpl themes.Template, paletteID string, merged map[string]string) error {
	id := strings.TrimSpace(paletteID)
	if id == "" || strings.EqualFold(id, "default") {
		return nil
	}
	pal, ok := tmpl.PaletteByID(id)
	if !ok {
		return fmt.Errorf("unknown palette %q for template %q", id, tmpl.Name)
	}
	for k, v := range pal.Tokens {
		if strings.TrimSpace(v) == "" {
			continue
		}
		merged[k] = v
	}
	merged[themeKeyPalette] = pal.ID
	return nil
}

func requireBookendIntake(tmpl themes.Template, args CreateDeckArgs) error {
	titles := officialBookendKinds(tmpl, "title")
	closings := officialBookendKinds(tmpl, "closing")
	titleKind := strings.TrimSpace(args.TitleKind)
	closingKind := strings.TrimSpace(args.ClosingKind)
	if len(titles) > 1 && titleKind == "" {
		return fmt.Errorf("titleKind is required: template %q has %d cover layouts. Call ask_user with slidesTemplate=%q and slidesKind=title, then pass the option id as titleKind (or \"default\" after they see the picker)", tmpl.Name, len(titles), tmpl.Name)
	}
	resolvedTitle := titleKind
	if strings.EqualFold(resolvedTitle, "default") || resolvedTitle == "" {
		if len(titles) == 1 {
			resolvedTitle = titles[0]
		} else if strings.EqualFold(titleKind, "default") {
			resolvedTitle = defaultOfficialKind(tmpl, titles)
		}
	}
	if resolvedTitle != "" {
		arch, err := findTemplateArchetype(tmpl, resolvedTitle, "")
		if err == nil && firstImageSlotID(arch) != "" && strings.TrimSpace(args.TitleImage) == "" {
			return fmt.Errorf("titleImage is required: cover %q has an image well. Ask yes/no then ask_user slidesImagePicker=true with slidesTemplate=%q, and pass the photo id or \"none\"", resolvedTitle, tmpl.Name)
		}
	}
	if len(closings) > 1 && closingKind == "" {
		return fmt.Errorf("closingKind is required: template %q has %d end-page layouts. Call ask_user with slidesTemplate=%q and slidesKind=closing, then pass the option id as closingKind (or \"default\" after they see the picker)", tmpl.Name, len(closings), tmpl.Name)
	}
	return nil
}

func defaultOfficialKind(tmpl themes.Template, kinds []string) string {
	for _, k := range kinds {
		arch, err := findTemplateArchetype(tmpl, k, "")
		if err != nil {
			continue
		}
		if firstImageSlotID(arch) == "" {
			return k
		}
	}
	if len(kinds) > 0 {
		return kinds[0]
	}
	return ""
}

func applyStampedBookendKind(spec FillSlideSpec, deck *store.DeckManifest, lastPos int) FillSlideSpec {
	if deck == nil || deck.Theme == nil {
		return spec
	}
	if spec.Position == 0 {
		if k := strings.TrimSpace(deck.Theme[themeKeyTitleKind]); k != "" {
			if spec.Kind == "" || isOfficialBookendKind(spec.Kind) || spec.Kind == RecipeCover {
				spec.Kind = k
				spec.Label = ""
			}
		}
	}
	if spec.Position == lastPos && lastPos > 0 {
		if k := strings.TrimSpace(deck.Theme[themeKeyClosingKind]); k != "" {
			if spec.Kind == "" || isOfficialBookendKind(spec.Kind) || spec.Kind == RecipeCloser {
				spec.Kind = k
				spec.Label = ""
			}
		}
	}
	return spec
}

func stampBookendKinds(tmpl themes.Template, args CreateDeckArgs, merged map[string]string) {
	titleKind := strings.TrimSpace(args.TitleKind)
	closingKind := strings.TrimSpace(args.ClosingKind)
	titles := officialBookendKinds(tmpl, "title")
	closings := officialBookendKinds(tmpl, "closing")
	if strings.EqualFold(titleKind, "default") {
		titleKind = defaultOfficialKind(tmpl, titles)
	}
	if strings.EqualFold(closingKind, "default") {
		closingKind = defaultOfficialKind(tmpl, closings)
	}
	if titleKind == "" && len(titles) == 1 {
		titleKind = titles[0]
	}
	if closingKind == "" && len(closings) == 1 {
		closingKind = closings[0]
	}
	if titleKind != "" {
		merged[themeKeyTitleKind] = titleKind
	}
	if closingKind != "" {
		merged[themeKeyClosingKind] = closingKind
	}
	if img := normalizeCoverPhotoRef(args.TitleImage); img != "" {
		merged[themeKeyTitleImage] = img
	}
}

func createDeckCatalogInstructions(tmpl themes.Template, theme map[string]string) string {
	s := "MANDATORY: author the whole deck in ONE fill_slides call (slides: [{position, kind or label, fills}, ...]). " +
		"Do NOT call fill_slide once per slide — that is one LLM round-trip per slide. fill_slide is only for later single-slide edits. " +
		"Do NOT call write_slide and do NOT copy archetype markup. " +
		"Body slides: recipe-* catalog entries (named slots: eyebrow, headline, body_1, item_1_title, …). " +
		"Pick the recipe whose slot count matches the content. A chapter is an eyebrow on a full content slide — do not insert empty section dividers. "
	titles := officialBookendKinds(tmpl, "title")
	closings := officialBookendKinds(tmpl, "closing")
	if len(titles) > 0 {
		s += "Slide 0 MUST be the official title family (" + strings.Join(titles, ", ") + "), not recipe-cover. "
		if k := strings.TrimSpace(theme[themeKeyTitleKind]); k != "" {
			s += "Use kind " + k + ". "
		}
		s += "Fill that variant's catalog fillSlots (often ph-* or the ids in slotHints). headline maps to the title slot, dek to the subtitle/body slot when those recipe names are not on the cover. "
		if k := strings.TrimSpace(theme[themeKeyTitleImage]); k != "" {
			s += "Cover photo is " + k + " in the first ph-pic-* slot. "
		} else {
			s += "Do not keep sample cover photos; omit ph-pic fills unless the user picked a template photo (titleImage). "
		}
	} else {
		s += "Cover = recipe-cover (thesis in the dek). "
	}
	if len(closings) > 0 {
		s += "Last slide MUST be the official closing family (" + strings.Join(closings, ", ") + "), not recipe-closer. "
		if k := strings.TrimSpace(theme[themeKeyClosingKind]); k != "" {
			s += "Use kind " + k + ". "
		}
	} else {
		s += "Closer = recipe-closer. "
	}
	s += "Fill EVERY required text slot listed in this catalog's fillSlots (product closer requires thesis + 3 takeaway chips; product cover has two meta cells). " +
		"Extra fill keys the skin does not emit are ignored. " +
		"Vary at least three recipe-* kinds in a deck longer than 6 slides."
	return s
}

// getTemplateVariantPreviews resolves a template and returns the per-variant
// ast-slide markup plus the shared theme tokens and assets, so a caller (e.g. the
// ask_user variant picker) can render a live thumbnail of each variant. Unlike
// list_templates (deliberately lightweight) this DOES include markup, but it
// still never returns data: image/font bytes — the markup references assets by
// asset-ref which resolve through the existing deck export at render time.
func getTemplateVariantPreviews(ctx context.Context, args TemplateVariantPreviewsArgs) (TemplateVariantPreviewsResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return TemplateVariantPreviewsResult{}, err
	}
	name := strings.TrimSpace(args.Template)
	if name == "" {
		return TemplateVariantPreviewsResult{}, fmt.Errorf("template is required")
	}
	tmpl, ok := svc.resolveTemplate(ctx, name)
	if !ok {
		return TemplateVariantPreviewsResult{}, fmt.Errorf("unknown template %q", name)
	}
	wantKind := strings.TrimSpace(args.Kind)
	if wantKind == "" {
		return TemplateVariantPreviewsResult{}, fmt.Errorf("kind is required (title, section, agenda, closing, content, or pattern)")
	}
	variants := make([]TemplateVariantPreview, 0, len(tmpl.Archetypes))
	for _, arch := range tmpl.Archetypes {
		// Imported templates preserve variant multiplicity by suffixing the
		// role: a template with several covers stores them as title, title-2,
		// title-3, … (see uniqueKind in import_worker.mjs). A caller filtering
		// by slidesKind="title" means the whole ROLE family, so match on the
		// base kind (strip any -N suffix), not an exact string. content also
		// matches pattern-* (sample-derived designed body slides).
		base := stripVariantSuffix(arch.Kind)
		if base != wantKind && !(wantKind == "content" && base == "pattern") {
			continue
		}
		variants = append(variants, TemplateVariantPreview{
			Kind:         arch.Kind,
			Label:        arch.Title,
			Tier:         arch.Tier,
			FillSlots:    arch.FillSlots,
			ThumbnailRef: arch.ThumbnailRef,
		})
	}
	return TemplateVariantPreviewsResult{
		Template: tmpl.Name,
		Theme:    tmpl.Tokens,
		Variants: variants,
	}, nil
}

// templatePick pairs a generated select option with its resolved cover
// thumbnail for the template-choice picker.
type templatePick struct {
	option    AskUserOption
	thumbnail *AskUserThumbnail
}

// templatePickerOptions enumerates every available template (built-in +
// scoped/imported) and returns one option per template — id=template name,
// label/description from the catalog — each carrying a live thumbnail of that
// template's cover slide (its first `title` archetype, else its first
// archetype). The thumbnail is a slides-archetype whose asset-refs resolve on
// the client from the template's own asset map (never data: bytes here). Order
// mirrors list_templates (built-ins first, then scoped) for determinism.
func templatePickerOptions(ctx context.Context) ([]templatePick, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return nil, err
	}
	picks := make([]templatePick, 0)
	seen := make(map[string]bool)
	add := func(t themes.Template) {
		if seen[t.Name] {
			return
		}
		seen[t.Name] = true
		label := strings.TrimSpace(t.Label)
		if label == "" {
			label = t.Name
		}
		pick := templatePick{
			option: AskUserOption{ID: t.Name, Label: label, Description: strings.TrimSpace(t.Description)},
		}
		if cover := coverArchetype(t); cover != nil {
			if thumb := archetypeThumbnail(t.Name, t.Tokens, *cover); thumb != nil {
				thumb.OptionID = t.Name
				pick.thumbnail = thumb
			}
		}
		picks = append(picks, pick)
	}
	for _, t := range themes.ListTemplates() {
		add(t)
	}
	scoped, err := svc.ListTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	for _, t := range scoped {
		add(t)
	}
	return picks, nil
}

// coverArchetype returns the archetype that best represents a template's look:
// the first `title` (cover) role — matching the base kind so suffixed variants
// like title-2 also qualify — falling back to the template's first archetype.
func coverArchetype(t themes.Template) *themes.Archetype {
	for i := range t.Archetypes {
		if stripVariantSuffix(t.Archetypes[i].Kind) == "title" {
			return &t.Archetypes[i]
		}
	}
	if len(t.Archetypes) > 0 {
		return &t.Archetypes[0]
	}
	return nil
}

// archetypeThumbnail prefers a baked PNG (kind=image) so the model never sees
// full ASD markup. Built-ins without ThumbnailRef fall back to a live
// slides-archetype render of the (small) markup.
// Title/closing pickers always live-render chrome with sample photos stripped
// so every cover does not show the same template person/bike.
func archetypeThumbnail(templateName string, tokens map[string]string, arch themes.Archetype) *AskUserThumbnail {
	if isOfficialBookendKind(arch.Kind) {
		markup := layoutPreviewMarkup(arch.Markup, tokens)
		if strings.TrimSpace(markup) == "" {
			return nil
		}
		return &AskUserThumbnail{Kind: "slides-archetype", Markup: markup, Theme: tokens, Template: templateName}
	}
	if ref := strings.TrimSpace(arch.ThumbnailRef); ref != "" {
		return &AskUserThumbnail{Kind: "image", AssetRef: ref, Template: templateName}
	}
	if strings.TrimSpace(arch.Markup) == "" {
		return nil
	}
	return &AskUserThumbnail{Kind: "slides-archetype", Markup: arch.Markup, Theme: tokens, Template: templateName}
}

func shouldOfferDefaultChoice(kind string) bool {
	base := stripVariantSuffix(strings.TrimSpace(kind))
	return base == "title" || base == "closing"
}

// archetypeVisualScore ranks a cover/end variant so photo + color layouts
// appear before empty white title skeletons in the picker.
func archetypeVisualScore(a themes.Archetype) int {
	s := 0
	re := regexp.MustCompile(`<ast-image\b([^>]*)>`)
	for _, m := range re.FindAllStringSubmatch(a.Markup, -1) {
		tag := m[1]
		if !strings.Contains(tag, "asset-ref=") {
			continue
		}
		area := attrIntFrom(tag, "w") * attrIntFrom(tag, "h")
		if area >= 400*300 {
			s += 120
		} else if area >= 250*180 {
			s += 40
		}
	}
	for _, id := range a.FillSlots {
		if strings.HasPrefix(id, "ph-pic-") {
			s += 40
		}
	}
	n := strings.Count(a.Markup, "<ast-shape ")
	if n > 15 {
		n = 15
	}
	s += n * 4
	return s
}

func appendDefaultAskOption(opts []AskUserOption, description string) []AskUserOption {
	for _, o := range opts {
		if o.ID == "default" {
			return opts
		}
	}
	return append(opts, AskUserOption{ID: "default", Label: "Use the default", Description: description})
}

// palettePickerOptions returns one option per template palette, each with a
// live recipe-cover thumbnail recolored to that palette. Imported brand
// templates typically have no palettes — callers get an error rather than a
// made-up colorway list.
func palettePickerOptions(ctx context.Context, templateName string) ([]templatePick, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return nil, err
	}
	tmpl, ok := svc.resolveTemplate(ctx, templateName)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateName)
	}
	if len(tmpl.Palettes) == 0 {
		return nil, fmt.Errorf("template %q has no color palettes", tmpl.Name)
	}
	chrome := ExtractChrome(tmpl)
	picks := make([]templatePick, 0, len(tmpl.Palettes))
	for _, pal := range tmpl.Palettes {
		tokens := cloneStringMap(tmpl.Tokens)
		if tokens == nil {
			tokens = make(map[string]string)
		}
		for k, v := range pal.Tokens {
			if strings.TrimSpace(v) == "" {
				continue
			}
			tokens[k] = v
		}
		clone := tmpl
		clone.Tokens = tokens
		markup, err := RenderRecipe(RecipeCover, SkinFor(clone), clone.StyleGuide, chrome, nil)
		if err != nil {
			return nil, fmt.Errorf("palette %q cover: %w", pal.ID, err)
		}
		thumb := &AskUserThumbnail{
			Kind:     "slides-archetype",
			Markup:   markup,
			Theme:    tokens,
			Template: tmpl.Name,
			OptionID: pal.ID,
		}
		picks = append(picks, templatePick{
			option:    AskUserOption{ID: pal.ID, Label: pal.Label},
			thumbnail: thumb,
		})
	}
	return picks, nil
}

func isRasterDataURI(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "data:image/") && !strings.HasPrefix(v, "data:image/svg+xml")
}

const maxCoverPhotoOptions = 18

type heroPhoto struct {
	Ref         string
	Label       string
	Description string
}

func collectTemplateHeroPhotos(tmpl themes.Template) []heroPhoto {
	seen := map[string]bool{}
	var out []heroPhoto
	add := func(ref, label, desc string, w, h int) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] || len(out) >= maxCoverPhotoOptions {
			return
		}
		if w <= 0 || h <= 0 || w*h < heroPhotoMinArea {
			return
		}
		data, ok := tmpl.Assets[ref]
		if !ok || !isRasterDataURI(data) {
			return
		}
		seen[ref] = true
		if strings.TrimSpace(label) == "" {
			label = fmt.Sprintf("Photo %d", len(out)+1)
		}
		if strings.TrimSpace(desc) == "" {
			desc = "From this template's example slides"
		}
		out = append(out, heroPhoto{Ref: ref, Label: label, Description: desc})
	}
	// Example slides first — these are the authored photos users expect to
	// reuse on a cover, not the single raster left on the layout archetype.
	if tmpl.Model != nil {
		for i, slide := range tmpl.Model.Slides {
			label := strings.TrimSpace(slide.Name)
			if label == "" {
				label = fmt.Sprintf("Example slide %d", i+1)
			}
			if mk := strings.TrimSpace(slide.Background.MediaKey); mk != "" {
				add(mk, label, "Background from "+label, CanvasWidth, CanvasHeight)
			}
			for _, o := range slide.Objects {
				add(o.MediaKey, label, "From "+label, o.W, o.H)
			}
			for _, p := range slide.Placeholders {
				add(p.MediaKey, label, "From "+label, p.W, p.H)
			}
		}
	}
	for _, a := range tmpl.Archetypes {
		if stripVariantSuffix(a.Kind) != "title" && stripVariantSuffix(a.Kind) != "closing" {
			continue
		}
		from := strings.TrimSpace(a.Title)
		if from == "" {
			from = a.Kind
		}
		for _, m := range astImageTagRe.FindAllStringSubmatch(a.Markup, -1) {
			attrs := m[1]
			if attrs == "" && len(m) > 2 {
				attrs = m[2]
			}
			add(attrValue(attrs, "asset-ref"), from, "From "+from, attrIntFrom(attrs, "w"), attrIntFrom(attrs, "h"))
		}
	}
	return out
}

// templateHeroPhotoOptions lists unique large rasters from the template's
// example slides (and leftover title/closing markup) so the user can pick ONE
// photo for the cover well.
func templateHeroPhotoOptions(ctx context.Context, templateName string) ([]templatePick, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return nil, err
	}
	tmpl, ok := svc.resolveTemplate(ctx, templateName)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateName)
	}
	photos := collectTemplateHeroPhotos(tmpl)
	if len(photos) == 0 {
		return nil, fmt.Errorf("template %q has no example photos", tmpl.Name)
	}
	picks := make([]templatePick, 0, len(photos))
	for _, p := range photos {
		picks = append(picks, templatePick{
			option: AskUserOption{ID: p.Ref, Label: p.Label, Description: p.Description},
			thumbnail: &AskUserThumbnail{
				Kind:     "image",
				AssetRef: p.Ref,
				Template: tmpl.Name,
				OptionID: p.Ref,
			},
		})
	}
	return picks, nil
}

// --- ask_user: generic interactive chat question ---
//
// ask_user is a GENERIC, domain-agnostic tool: it lets the assistant ask the
// user a single structured question inline in chat — either yes/no or a
// single-select pick-one, where each select option may carry an optional visual
// thumbnail. It is NOT slides-specific (the Slides variant picker is merely the
// first consumer). The tool does not block the agent loop: it returns a short
// result telling the model the user's next message is the answer, then the
// model's turn ends. The chat runner turns the returned structured payload into
// a [chat_question] card (see maybeEmitChatQuestion in chat_runner.go); the user
// answers by clicking, which sends their choice back as an ordinary user message.

// AskUserThumbnail is an optional visual preview attached to a select option.
// For slides pass kind="slides-archetype" with the archetype Markup (+ optional
// Theme tokens and the Template name so the frontend can resolve asset-refs from
// the template at render time) so the UI renders a live mini-slide; for a plain
// image pass kind="image" with an AssetRef. NEVER embed data: bytes or a resolved
// asset map here — that would bloat the model history and the persisted message
// by megabytes (asset-refs resolve on the client via the slides API instead).
type AskUserThumbnail struct {
	OptionID string            `json:"optionId" jsonschema:"The id of the option this thumbnail belongs to."`
	Kind     string            `json:"kind" jsonschema:"Thumbnail kind: 'slides-archetype' (render markup) or 'image' (asset-ref)."`
	Markup   string            `json:"markup,omitempty" jsonschema:"For slides-archetype: the ast-slide fragment to render as a live preview."`
	AssetRef string            `json:"assetRef,omitempty" jsonschema:"For image: an existing deck asset-ref to display."`
	Theme    map[string]string `json:"theme,omitempty" jsonschema:"Optional theme tokens the markup references (from get_template_variant_previews)."`
	Template string            `json:"template,omitempty" jsonschema:"Template name whose assets resolve the markup's asset-refs at render time (never embed data: bytes)."`
}

// AskUserOption is one selectable answer for a kind="select" question.
type AskUserOption struct {
	ID          string `json:"id" jsonschema:"Stable id for the option (returned as the answer id)."`
	Label       string `json:"label" jsonschema:"Human-readable label shown on the option and sent back as the answer text."`
	Description string `json:"description,omitempty" jsonschema:"Optional one-line description shown under the label."`
}

// AskUserArgs defines the ask_user tool input.
type AskUserArgs struct {
	Kind       string             `json:"kind" jsonschema:"'yesno' for a Yes/No question, or 'select' for a single-choice pick-one."`
	Prompt     string             `json:"prompt" jsonschema:"The question to show the user (one clear sentence)."`
	Options    []AskUserOption    `json:"options,omitempty" jsonschema:"For kind='select': the choices. Omit for 'yesno'. When slidesTemplate is set you may omit options entirely to auto-generate one option per template variant."`
	Thumbnails []AskUserThumbnail `json:"thumbnails,omitempty" jsonschema:"Optional per-option thumbnails (match by optionId) for a visual picker. Usually unnecessary for slides — prefer slidesTemplate below, which attaches live variant previews automatically."`
	// Slides convenience: for a slide-variant picker, set slidesTemplate (and
	// optionally slidesKind) instead of copying markup into thumbnails. ask_user
	// then calls get_template_variant_previews itself and attaches a live
	// slides-archetype thumbnail to each option, matching by label (falling back
	// to id). If options are omitted, one option is generated per variant. This is
	// the reliable way to get a VISUAL slide picker — do not hand-copy markup.
	SlidesTemplate string `json:"slidesTemplate,omitempty" jsonschema:"For a slide-variant picker: the template name (from list_slide_templates). ask_user auto-attaches a live thumbnail per option and, if options are omitted, generates one option per variant."`
	SlidesKind     string `json:"slidesKind,omitempty" jsonschema:"Optional archetype role to filter the variants by (e.g. title, section, agenda, closing, content). Only used with slidesTemplate."`
	// Slides convenience: for the FIRST question — "which template should I use?"
	// — set slidesTemplatePicker=true (with kind='select') and omit options.
	// ask_user then enumerates every available template (built-in + imported),
	// generates one option per template (id=template name, label+description from
	// the catalog), and attaches a live thumbnail of each template's cover so the
	// user picks by seeing the design. Do NOT hand-copy markup or thumbnails.
	SlidesTemplatePicker bool `json:"slidesTemplatePicker,omitempty" jsonschema:"For the template-choice question: set true (with kind='select', no options) to auto-generate one option per available template, each with a live thumbnail of that template's cover slide."`
	// Slides convenience: colorways on a template that defines Palettes (Product
	// Deck). Set true with kind='select' and slidesTemplate; omit options.
	SlidesPalettePicker bool `json:"slidesPalettePicker,omitempty" jsonschema:"For a color-palette question: set true (kind='select') with slidesTemplate. ask_user lists each palette with a live recolored cover thumbnail. Omit options. Do not invent palettes for imported brand templates."`
	// Slides convenience: photos from the template's example title slides, for
	// the cover's single image well. Set true with kind='select' and slidesTemplate.
	SlidesImagePicker bool `json:"slidesImagePicker,omitempty" jsonschema:"For the cover-photo question after a title layout with a ph-pic well: set true (kind='select') with slidesTemplate. ask_user lists each unique template photo. Omit options. Pass the chosen id as titleImage on create_deck."`
}

// AskUserResult is the structured payload the chat runner turns into a
// [chat_question] card. It is also a normal tool result the model reads: the
// Message tells the model to wait for the user's next message (their answer).
type AskUserResult struct {
	Status     string                 `json:"status"`
	QuestionID string                 `json:"questionId"`
	Kind       string                 `json:"kind"`
	Prompt     string                 `json:"prompt"`
	Options    []AskUserOptionPayload `json:"options,omitempty"`
	Message    string                 `json:"message"`
}

// AskUserOptionPayload is one option plus its resolved thumbnail in the result.
type AskUserOptionPayload struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Thumbnail   *AskUserThumbnail `json:"thumbnail,omitempty"`
}

func askUser(ctx context.Context, args AskUserArgs) (AskUserResult, error) {
	kind := strings.TrimSpace(args.Kind)
	if kind != "yesno" && kind != "select" {
		return AskUserResult{}, fmt.Errorf("kind must be 'yesno' or 'select'")
	}
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		return AskUserResult{}, fmt.Errorf("prompt is required")
	}

	// Index explicit thumbnails by option id so each option carries its own
	// preview. Explicit thumbnails always win over auto-resolved slides previews.
	thumbByOption := make(map[string]AskUserThumbnail, len(args.Thumbnails))
	for _, t := range args.Thumbnails {
		if strings.TrimSpace(t.OptionID) != "" {
			thumbByOption[t.OptionID] = t
		}
	}

	inOptions := args.Options

	// Slides convenience (FIRST question): when slidesTemplatePicker is set,
	// enumerate every available template and generate one option per template,
	// each carrying a live thumbnail of that template's cover slide. This turns
	// "which template should I use?" into a VISUAL card instead of a text list.
	if args.SlidesTemplatePicker && kind == "select" {
		picks, err := templatePickerOptions(ctx)
		if err != nil {
			return AskUserResult{}, fmt.Errorf("resolve template picker: %w", err)
		}
		// Auto-generate options from the templates when the model omitted them
		// (or passed fewer than exist), so the picker always covers every
		// template. The model may still curate labels/order by passing options
		// whose ids are template names.
		if len(inOptions) < len(picks) {
			inOptions = inOptions[:0]
			for _, p := range picks {
				inOptions = append(inOptions, AskUserOption{ID: p.option.ID, Label: p.option.Label, Description: p.option.Description})
			}
		}
		// Attach a cover thumbnail per option (unless one was passed explicitly),
		// matched by option id == template name.
		for _, p := range picks {
			if _, ok := thumbByOption[p.option.ID]; ok {
				continue
			}
			if p.thumbnail != nil {
				thumbByOption[p.option.ID] = *p.thumbnail
			}
		}
	}

	// Slides convenience: color palettes on a template (Product Deck). Each
	// option is a live recipe-cover recolored with that palette's tokens.
	if args.SlidesPalettePicker && kind == "select" {
		template := strings.TrimSpace(args.SlidesTemplate)
		if template == "" {
			return AskUserResult{}, fmt.Errorf("slidesPalettePicker requires slidesTemplate")
		}
		picks, err := palettePickerOptions(ctx, template)
		if err != nil {
			return AskUserResult{}, fmt.Errorf("resolve palette picker: %w", err)
		}
		if len(inOptions) < len(picks) {
			inOptions = inOptions[:0]
			for _, p := range picks {
				inOptions = append(inOptions, AskUserOption{ID: p.option.ID, Label: p.option.Label, Description: p.option.Description})
			}
		}
		for _, p := range picks {
			if _, ok := thumbByOption[p.option.ID]; ok {
				continue
			}
			if p.thumbnail != nil {
				thumbByOption[p.option.ID] = *p.thumbnail
			}
		}
		inOptions = appendDefaultAskOption(inOptions, "Keep this template's default colorway")
	} else if args.SlidesImagePicker && kind == "select" {
		template := strings.TrimSpace(args.SlidesTemplate)
		if template == "" {
			return AskUserResult{}, fmt.Errorf("slidesImagePicker requires slidesTemplate")
		}
		picks, err := templateHeroPhotoOptions(ctx, template)
		if err != nil {
			return AskUserResult{}, fmt.Errorf("resolve image picker: %w", err)
		}
		if len(inOptions) < len(picks) {
			inOptions = inOptions[:0]
			for _, p := range picks {
				inOptions = append(inOptions, AskUserOption{ID: p.option.ID, Label: p.option.Label, Description: p.option.Description})
			}
		}
		for _, p := range picks {
			if _, ok := thumbByOption[p.option.ID]; ok {
				continue
			}
			if p.thumbnail != nil {
				thumbByOption[p.option.ID] = *p.thumbnail
			}
		}
		inOptions = appendDefaultAskOption(inOptions, "No photo — leave the cover image well empty")
	} else if template := strings.TrimSpace(args.SlidesTemplate); template != "" && kind == "select" {
		previews, err := getTemplateVariantPreviews(ctx, TemplateVariantPreviewsArgs{
			Template: template,
			Kind:     strings.TrimSpace(args.SlidesKind),
		})
		if err != nil {
			return AskUserResult{}, fmt.Errorf("resolve slide variant previews: %w", err)
		}
		svc, err := personalService(ctx)
		if err != nil {
			return AskUserResult{}, err
		}
		tmpl, ok := svc.resolveTemplate(ctx, template)
		if !ok {
			return AskUserResult{}, fmt.Errorf("unknown template %q", template)
		}
		archByKind := make(map[string]themes.Archetype, len(tmpl.Archetypes))
		for _, a := range tmpl.Archetypes {
			archByKind[a.Kind] = a
		}
		// Title/closing pickers lead with the richest cover (photo + color) so
		// an empty white layout is not the first tile the user clicks.
		if shouldOfferDefaultChoice(args.SlidesKind) {
			sort.SliceStable(previews.Variants, func(i, j int) bool {
				return archetypeVisualScore(archByKind[previews.Variants[i].Kind]) >
					archetypeVisualScore(archByKind[previews.Variants[j].Kind])
			})
		}
		byLabel := make(map[string]TemplateVariantPreview, len(previews.Variants))
		for _, v := range previews.Variants {
			byLabel[strings.ToLower(strings.TrimSpace(v.Label))] = v
		}
		// Auto-generate options from the variants when the model omitted them OR
		// passed fewer than the available variants. When a slidesTemplate is set
		// the intent is a picker over ALL of that role's variants, so a partial
		// (e.g. single) explicit option list would hide the rest — replace it with
		// the full variant set. The model can still curate by passing at least as
		// many options as there are variants.
		if len(inOptions) < len(previews.Variants) {
			inOptions = inOptions[:0]
			seenID := make(map[string]int, len(previews.Variants))
			for i, v := range previews.Variants {
				label := strings.TrimSpace(v.Label)
				if label == "" {
					// Fall back to a role-scoped, human-recognizable label so the
					// picker never shows blank tiles when a variant has no title.
					label = strings.TrimSpace(v.Kind)
					if label == "" {
						label = "variant"
					}
					label = fmt.Sprintf("%s %d", capitalizeFirst(label), i+1)
				}
				// Prefer the catalog kind (title, title-2) as the option id so
				// fill_slides / titleKind can use the ask_user result directly.
				// Fall back to a slug of the label when kinds collide.
				id := strings.TrimSpace(v.Kind)
				if id == "" || seenID[id] > 0 {
					id = slugifyOptionID(label)
				}
				baseID := id
				if n := seenID[baseID]; n > 0 {
					id = fmt.Sprintf("%s-%d", baseID, n+1)
				}
				seenID[baseID]++
				inOptions = append(inOptions, AskUserOption{ID: id, Label: label})
			}
		}
		// Attach a thumbnail per option (unless one was passed explicitly).
		for _, o := range inOptions {
			if _, ok := thumbByOption[o.ID]; ok {
				continue
			}
			v, ok := byLabel[strings.ToLower(strings.TrimSpace(o.Label))]
			if !ok {
				continue
			}
			arch, ok := archByKind[v.Kind]
			if !ok {
				continue
			}
			thumb := archetypeThumbnail(previews.Template, tmpl.Tokens, arch)
			if thumb == nil {
				continue
			}
			thumb.OptionID = o.ID
			thumbByOption[o.ID] = *thumb
		}
		if shouldOfferDefaultChoice(args.SlidesKind) {
			inOptions = appendDefaultAskOption(inOptions, "I'll pick the variant that fits this deck")
		}
	}

	if kind == "select" && len(inOptions) == 0 {
		return AskUserResult{}, fmt.Errorf("kind 'select' requires at least one option (or set slidesTemplate to generate them)")
	}

	options := make([]AskUserOptionPayload, 0, len(inOptions))
	for _, o := range inOptions {
		if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
			return AskUserResult{}, fmt.Errorf("every option requires a non-empty id and label")
		}
		op := AskUserOptionPayload{ID: o.ID, Label: o.Label, Description: o.Description}
		if t, ok := thumbByOption[o.ID]; ok {
			tc := t
			op.Thumbnail = &tc
		}
		options = append(options, op)
	}
	// A deterministic id keyed on the question so the card is stable across
	// reconnects; derived from kind+prompt.
	sum := sha256.Sum256([]byte(kind + "|" + prompt))
	qid := "q-" + fmt.Sprintf("%x", sum)[:12]
	return AskUserResult{
		Status:     "ok",
		QuestionID: qid,
		Kind:       kind,
		Prompt:     prompt,
		Options:    options,
		Message:    "Question shown to the user. Wait for their reply — the user's next message is their answer; do not proceed until they respond.",
	}, nil
}

// stripVariantSuffix removes a trailing "-N" numeric variant suffix from a
// role kind: title-2 -> title, section-3 -> section. Kinds without a numeric
// suffix (or with a non-numeric hyphen segment) are returned unchanged. Import
// suffixes duplicate roles this way (see uniqueKind in import_worker.mjs) so a
// role-family filter must collapse them back to the base kind.
func stripVariantSuffix(kind string) string {
	if i := strings.LastIndexByte(kind, '-'); i > 0 {
		if _, err := strconv.Atoi(kind[i+1:]); err == nil {
			return kind[:i]
		}
	}
	return kind
}

// capitalizeFirst upper-cases the first rune of s (ASCII), leaving the rest
// unchanged. Used to render a role like "title" as "Title" in a fallback label.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// slugifyOptionID derives a stable, human-recognizable option id from a variant
// label (lowercased, non-alphanumerics collapsed to single hyphens). Used when
// ask_user auto-generates options from slide template variants.
func slugifyOptionID(label string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen && b.Len() > 0 {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return "option"
	}
	return id
}

func validateDeck(ctx context.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ValidateDeckResult{}, err
	}
	deck, slides, err := svc.Deck(ctx, resolveWorkingDeckSlug(ctx, svc, args.Slug))
	if err != nil {
		return ValidateDeckResult{}, err
	}
	result := ValidateDeckResult{Deck: deckView(deck), SlideCount: len(slides), Valid: true}
	for _, persisted := range slides {
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

// ReviewDeckArgs defines the review_deck tool input.
type ReviewDeckArgs struct {
	Slug string `json:"slug" jsonschema:"Deck slug to self-review before declaring done."`
}

// ReviewFinding is one heuristic observation about a slide. Severity is
// "warning" (must fix before finishing) or "info" (should consider). It carries
// scene-derived text only — never image/font bytes.
type ReviewFinding struct {
	SlideIndex int    `json:"slideIndex"`
	NodeID     string `json:"nodeId,omitempty"`
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
}

// ReviewDeckResult is the self-evaluation the model reads before declaring a
// deck done: automated heuristic Findings plus a human-style Checklist of
// review points. It DELIBERATELY carries no data: bytes (only the slim DeckView,
// counts, and text findings) so it stays out of model context / off the wire
// (see TestSlidesResponsesOmitHeavyManifestFields).
type ReviewDeckResult struct {
	Deck       *DeckView       `json:"deck"`
	SlideCount int             `json:"slideCount"`
	Findings   []ReviewFinding `json:"findings,omitempty"`
	Checklist  []string        `json:"checklist"`
	Message    string          `json:"message"`
}

// reviewChecklist is the fixed set of human review points the model should
// verify on every deck, even when the automated heuristics find nothing.
var reviewChecklist = []string{
	"Adjacent runs: no date/label collision (e.g. \"1972Founded\"); dates and labels are visually separated.",
	"Contrast & hierarchy: markers, text, and accents have adequate contrast against the surface; the visual hierarchy is clear.",
	"Template chrome: the deck reuses the template's fixed chrome (logo, footer, page furniture) so content slides match the cover/closing.",
	"Precise milestones: timeline/labels use specific dated events, not vague open-ended phrases (\"Today\", \"era\").",
	"Source citation: sources are precise (publisher, page/URL, access date) rather than a generic \"Source: X history\".",
}

// isAlphaNumRune reports whether r is an ASCII alphanumeric (used for the
// run-adjacency collision heuristic: a number/word touching a word/number with
// no whitespace between the two runs renders as e.g. "1972Founded").
func isAlphaNumRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// runsCollide reports whether run text a is directly followed by run text b with
// no separating whitespace where the boundary joins two alphanumeric runes —
// the "1972" + "Founded" -> "1972Founded" defect. Empty runs never collide.
func runsCollide(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	last := []rune(a)
	first := []rune(b)
	lr := last[len(last)-1]
	fr := first[0]
	return isAlphaNumRune(lr) && isAlphaNumRune(fr)
}

// hexLuminance parses a #RRGGBB (or RRGGBB) hex color and returns its relative
// luminance (0..1) per the WCAG formula. ok=false when the value is not a
// 6-digit hex color (so callers skip the contrast check rather than guess).
func hexLuminance(hex string) (float64, bool) {
	s := strings.TrimSpace(hex)
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	channel := func(c float64) float64 {
		c /= 255.0
		if c <= 0.03928 {
			return c / 12.92
		}
		return pow24((c + 0.055) / 1.055)
	}
	r := float64((v >> 16) & 0xff)
	g := float64((v >> 8) & 0xff)
	b := float64(v & 0xff)
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b), true
}

// pow24 computes x^2.4 for x in [0,1]. Isolated so hexLuminance reads cleanly.
func pow24(x float64) float64 { return math.Pow(x, 2.4) }

// contrastRatio returns the WCAG contrast ratio between two relative luminances.
func contrastRatio(l1, l2 float64) float64 {
	hi, lo := l1, l2
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// nodeFillHex returns the explicit fill/color hex authored on a node (node-level
// Fill, then props "fill"/"color"), or "" when none is set. It ignores theme
// tokens (color-token/fill-token) since those resolve to theme-controlled,
// generally accessible colors.
func nodeFillHex(n Node) string {
	if n.Fill != "" {
		return n.Fill
	}
	if n.Props != nil {
		if v, ok := n.Props["fill"].(string); ok && v != "" {
			return v
		}
		if v, ok := n.Props["color"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// nodeText returns a node's full text (joined runs, else Text) for the
// source-citation heuristic.
func nodeText(n Node) string {
	if len(n.Runs) > 0 {
		var b strings.Builder
		for _, r := range n.Runs {
			b.WriteString(r.Text)
		}
		return b.String()
	}
	return n.Text
}

func isFullCanvasNode(n Node) bool {
	return n.Geometry.W >= 1800 && n.Geometry.H >= 1000
}

func isNeutralFill(hex string) bool {
	s := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(hex), "#"))
	return s == "FFFFFF" || s == "FFF" || s == "FFFFFFFF" || s == "000000" || s == "000"
}

var designedGeom = map[string]bool{
	"roundRect": true, "ellipse": true, "chevron": true, "rightArrow": true,
	"leftArrow": true, "triangle": true, "hexagon": true, "trapezoid": true,
	"parallelogram": true, "diamond": true, "star5": true,
}

func slideHasDesignedChrome(nodes []Node) bool {
	for _, n := range nodes {
		if n.ID == "bg" || isFullCanvasNode(n) {
			if len(n.Children) > 0 && slideHasDesignedChrome(n.Children) {
				return true
			}
			continue
		}
		if designedGeom[n.Geom] || n.Path != "" {
			return true
		}
		if fill := nodeFillHex(n); fill != "" && !isNeutralFill(fill) && n.Geometry.W > 80 && n.Geometry.H > 40 {
			return true
		}
		if len(n.Children) > 0 && slideHasDesignedChrome(n.Children) {
			return true
		}
	}
	return false
}

func nodeFontSize(n Node) int {
	if n.Props != nil {
		switch v := n.Props["size"].(type) {
		case int:
			return v
		case float64:
			return int(v)
		case string:
			sz, _ := strconv.Atoi(v)
			return sz
		}
	}
	for _, r := range n.Runs {
		if r.Size > 0 {
			return r.Size
		}
	}
	return 0
}

func slideIsRecipeLayout(nodes []Node) bool {
	found := false
	walkNodes(nodes, func(n Node) {
		if n.ID == "eyebrow" || n.ID == "chrome-footer" || n.ID == "chrome-legal" || n.ID == "headline" {
			found = true
		}
	})
	return found
}

func slideHasEmptyEyebrow(nodes []Node) bool {
	saw := false
	empty := false
	walkNodes(nodes, func(n Node) {
		if n.ID != "eyebrow" {
			return
		}
		saw = true
		if strings.TrimSpace(nodeText(n)) == "" {
			empty = true
		}
	})
	return saw && empty
}

func countNonEmptyText(nodes []Node) int {
	n := 0
	reviewTextNodes(nodes, func(node Node) {
		if strings.TrimSpace(nodeText(node)) == "" {
			return
		}
		if node.ID == "chrome-footer" || node.ID == "chrome-legal" || node.ID == "chrome-page" || node.ID == "chrome-confidential" {
			return
		}
		n++
	})
	return n
}

func slideContentCoverage(nodes []Node) float64 {
	minX, minY := CanvasWidth, CanvasHeight
	maxX, maxY := 0, 0
	any := false
	reviewTextNodes(nodes, func(n Node) {
		if strings.TrimSpace(nodeText(n)) == "" {
			return
		}
		if n.Geometry.W < 40 || n.Geometry.H < 16 {
			return
		}
		any = true
		if n.Geometry.X < minX {
			minX = n.Geometry.X
		}
		if n.Geometry.Y < minY {
			minY = n.Geometry.Y
		}
		if n.Geometry.X+n.Geometry.W > maxX {
			maxX = n.Geometry.X + n.Geometry.W
		}
		if n.Geometry.Y+n.Geometry.H > maxY {
			maxY = n.Geometry.Y + n.Geometry.H
		}
	})
	if !any {
		return 0
	}
	area := (maxX - minX) * (maxY - minY)
	safe := (CanvasWidth - 160) * (CanvasHeight - 120)
	if safe <= 0 {
		return 0
	}
	return float64(area) / float64(safe)
}

var takeawayVerbRe = regexp.MustCompile(`(?i)\b(is|are|was|were|will|can|cannot|should|must|missed|grew|fell|cut|won|lost|made|built|chose|became|do|does|did|not|and then)\b`)

func slideHasNominalTitle(nodes []Node) bool {
	var title Node
	found := false
	reviewTextNodes(nodes, func(n Node) {
		if n.ID != "headline" && n.ID != "headline_2" {
			if found {
				return
			}
			if n.Geometry.Y < 280 && n.Geometry.W >= 600 && nodeFontSize(n) >= 32 {
				title = n
				found = true
			}
			return
		}
		if n.ID == "headline" {
			title = n
			found = true
		}
	})
	if !found {
		return false
	}
	text := strings.TrimSpace(nodeText(title))
	if text == "" {
		return false
	}
	// A filled headline_2 makes a split title, not a nominal label.
	hasLine2 := false
	reviewTextNodes(nodes, func(n Node) {
		if n.ID == "headline_2" && strings.TrimSpace(nodeText(n)) != "" {
			hasLine2 = true
		}
	})
	if hasLine2 {
		return false
	}
	words := strings.Fields(text)
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	if takeawayVerbRe.MatchString(text) || strings.ContainsAny(text, ".,;:—") {
		return false
	}
	return len(words) <= 4
}

func slideHasVisibleTitle(nodes []Node) bool {
	found := false
	reviewTextNodes(nodes, func(n Node) {
		if found || strings.TrimSpace(nodeText(n)) == "" {
			return
		}
		sz := nodeFontSize(n)
		top := n.Geometry.Y < 260 && n.Geometry.W >= 600
		display := sz >= 32 && n.Geometry.W >= 500
		banner := n.Geometry.W >= 1000 && n.Geometry.H >= 56
		if top || display || banner {
			found = true
		}
	})
	return found
}

func slideEmptyCardIDs(nodes []Node) []string {
	type box struct {
		id string
		g  Geometry
	}
	var cards []box
	var texts []Geometry
	var photos []Geometry
	walkNodes(nodes, func(n Node) {
		if n.Type == "image" && n.Geometry.W >= 360 && n.Geometry.H >= 280 {
			photos = append(photos, n.Geometry)
		}
		if n.Type == "text" && strings.TrimSpace(nodeText(n)) != "" {
			texts = append(texts, n.Geometry)
			return
		}
		if n.Type != "shape" || n.ID == "bg" || isFullCanvasNode(n) {
			return
		}
		if n.Geometry.W < 180 || n.Geometry.H < 70 || n.Geometry.W >= 1400 {
			return
		}
		if n.Geometry.H > 400 && n.Geometry.H > 2*n.Geometry.W {
			return
		}
		if n.Props != nil {
			if v, ok := n.Props["decorative"].(string); ok && (v == "true" || v == "1") {
				return
			}
		}
		isCard := n.Geom == "roundRect" || n.Geom == "chevron"
		if !isCard {
			fill := nodeFillHex(n)
			if fill == "" || isNeutralFill(fill) {
				return
			}
			isCard = true
		}
		if isCard {
			cards = append(cards, box{id: n.ID, g: n.Geometry})
		}
	})
	coversPhoto := func(g Geometry) bool {
		area := g.W * g.H
		if area <= 0 {
			return false
		}
		for _, p := range photos {
			ix := minInt(g.X+g.W, p.X+p.W) - maxInt(g.X, p.X)
			iy := minInt(g.Y+g.H, p.Y+p.H) - maxInt(g.Y, p.Y)
			if ix > 0 && iy > 0 && ix*iy*100/area > 35 {
				return true
			}
		}
		return false
	}
	var empty []string
	for _, c := range cards {
		if coversPhoto(c.g) {
			continue
		}
		hit := false
		for _, t := range texts {
			ix := minInt(c.g.X+c.g.W, t.X+t.W) - maxInt(c.g.X, t.X)
			iy := minInt(c.g.Y+c.g.H, t.Y+t.H) - maxInt(c.g.Y, t.Y)
			if ix > 20 && iy > 20 {
				hit = true
				break
			}
		}
		if !hit {
			empty = append(empty, c.id)
		}
	}
	return empty
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func slideIsSparseTitleOnly(nodes []Node) bool {
	if slideHasTemplateAssetRef(nodes) || slideHasDesignedChrome(nodes) {
		return false
	}
	texts := 0
	reviewTextNodes(nodes, func(n Node) {
		if strings.TrimSpace(nodeText(n)) == "" {
			return
		}
		if n.Geometry.H >= 40 || n.Geometry.W >= 400 {
			texts++
		}
	})
	return texts == 1
}

func slideLooksLikeTitleAndBody(nodes []Node) bool {
	texts := 0
	reviewTextNodes(nodes, func(n Node) {
		if n.Geometry.H >= 40 || n.Geometry.W >= 400 {
			texts++
		}
	})
	return texts >= 2
}

func walkNodes(nodes []Node, fn func(Node)) {
	for _, n := range nodes {
		fn(n)
		if len(n.Children) > 0 {
			walkNodes(n.Children, fn)
		}
	}
}

func slideHasTemplateAssetRef(nodes []Node) bool {
	for _, n := range nodes {
		if n.Props != nil {
			if v, ok := n.Props["asset-ref"].(string); ok && strings.TrimSpace(v) != "" {
				return true
			}
			if v, ok := n.Props["assetRef"].(string); ok && strings.TrimSpace(v) != "" {
				return true
			}
		}
		if len(n.Children) > 0 && slideHasTemplateAssetRef(n.Children) {
			return true
		}
	}
	return false
}

// reviewTextNodes walks a node tree, invoking fn for every text node so the
// heuristics can inspect runs/text at any nesting depth (groups included).
func reviewTextNodes(nodes []Node, fn func(Node)) {
	for _, n := range nodes {
		if n.Type == "text" {
			fn(n)
		}
		if len(n.Children) > 0 {
			reviewTextNodes(n.Children, fn)
		}
	}
}

// weakSourcePattern matches a vague, undated "Source: <name> history/overview"
// citation with no URL or year (case-insensitive). A precise citation with a
// URL (contains a dot+slash or "http") or a 4-digit year is not flagged.
func isWeakSourceCitation(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if !strings.HasPrefix(t, "source:") {
		return false
	}
	// Precise if it carries a URL-ish token or a 4-digit year.
	if strings.Contains(t, "http") || strings.Contains(t, ".com") || strings.Contains(t, ".org") || strings.Contains(t, ".html") {
		return false
	}
	for i := 0; i+4 <= len(t); i++ {
		seg := t[i : i+4]
		if seg[0] >= '1' && seg[0] <= '2' && allDigits(seg) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// reviewDeck renders the persisted deck to its parsed SceneGraph and returns a
// structured self-evaluation: automated heuristic findings (adjacent-run
// collision, low-contrast markers, missing template chrome, weak source
// citation) plus a fixed review checklist. It is AGENT-DRIVEN, not a hard
// runtime gate: the model calls it as the final step before finishing and
// revises until there are no warning-level findings. It never returns data:
// bytes — only scene-derived text.
func reviewDeck(ctx context.Context, args ReviewDeckArgs) (ReviewDeckResult, error) {
	svc, err := personalService(ctx)
	if err != nil {
		return ReviewDeckResult{}, err
	}
	slug := resolveWorkingDeckSlug(ctx, svc, args.Slug)
	deck, _, err := svc.Deck(ctx, slug)
	if err != nil {
		return ReviewDeckResult{}, err
	}
	scene, _, err := svc.Scene(ctx, slug)
	if err != nil {
		return ReviewDeckResult{}, err
	}

	fromTemplate := (deck.Theme != nil && strings.TrimSpace(deck.Theme[themeKeyTemplateName]) != "") ||
		deck.TemplateModel != "" || len(deck.Assets) > 0
	// Surface color for the contrast check: theme surface token, else white.
	surfaceHex := "#FFFFFF"
	if deck.Theme != nil {
		if v := strings.TrimSpace(deck.Theme["surface"]); v != "" {
			surfaceHex = v
		}
	}
	surfaceLum, surfaceOK := hexLuminance(surfaceHex)

	var findings []ReviewFinding
	var sparseIdx []int
	for si, slide := range scene.Slides {
		reviewTextNodes(slide.Nodes, func(n Node) {
			// 1) Adjacent-run collision.
			for i := 0; i+1 < len(n.Runs); i++ {
				if runsCollide(n.Runs[i].Text, n.Runs[i+1].Text) {
					findings = append(findings, ReviewFinding{
						SlideIndex: si,
						NodeID:     n.ID,
						Code:       "run_adjacency",
						Severity:   "warning",
						Message: fmt.Sprintf(
							"Adjacent runs %q and %q render with no space between them (e.g. \"1972Founded\"). Add a space inside a run (e.g. %q) or split the date and label into two separate positioned ast-text boxes.",
							n.Runs[i].Text, n.Runs[i+1].Text, n.Runs[i].Text+" "),
					})
				}
			}
			// 4) Weak source citation.
			if isWeakSourceCitation(nodeText(n)) {
				findings = append(findings, ReviewFinding{
					SlideIndex: si,
					NodeID:     n.ID,
					Code:       "weak_source",
					Severity:   "info",
					Message:    "Source citation is generic (no publisher page, URL, or access date). Cite the specific source precisely.",
				})
			}
		})

		// 2) Low-contrast marker (best-effort; only when both colors are hex).
		if surfaceOK {
			walkNodes(slide.Nodes, func(n Node) {
				if n.ID == "bg" || isFullCanvasNode(n) {
					return
				}
				hex := nodeFillHex(n)
				if hex == "" {
					return
				}
				if isNeutralFill(hex) && isNeutralFill(surfaceHex) {
					return
				}
				lum, ok := hexLuminance(hex)
				if !ok {
					return
				}
				if contrastRatio(lum, surfaceLum) < 2.0 {
					findings = append(findings, ReviewFinding{
						SlideIndex: si,
						NodeID:     n.ID,
						Code:       "low_contrast",
						Severity:   "info",
						Message:    fmt.Sprintf("Fill %s has low contrast against the slide surface; the element may read as faint. Use a stronger, consistent color.", hex),
					})
				}
			})
		}

		// 3) Missing template chrome (only meaningful for template-based decks).
		reviewTextNodes(slide.Nodes, func(n Node) {
			if strings.Contains(nodeText(n), "{{TITLE}}") || strings.Contains(nodeText(n), "{{BODY}}") {
				findings = append(findings, ReviewFinding{
					SlideIndex: si,
					NodeID:     n.ID,
					Code:       "unfilled_slot",
					Severity:   "warning",
					Message:    "Unfilled {{TITLE}}/{{BODY}} placeholder remains. Pass a fill for every text slot in fill_slide.",
				})
			}
		})
		// Warn only for flexible title+body walls. Empty dividers and chevron
		// card slides are not missing chrome.
		if fromTemplate && !slideIsRecipeLayout(slide.Nodes) && !slideHasTemplateAssetRef(slide.Nodes) && !slideHasDesignedChrome(slide.Nodes) && slideLooksLikeTitleAndBody(slide.Nodes) {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				Code:       "missing_chrome",
				Severity:   "warning",
				Message:    "This slide has no template media and no designed cards/boxes. Prefer a recipe-* catalog entry via fill_slides so each slide uses a full layout type.",
			})
		}
		if fromTemplate && slideIsSparseTitleOnly(slide.Nodes) {
			sparseIdx = append(sparseIdx, si)
		}
		if fromTemplate && si > 0 && slideContentCoverage(slide.Nodes) < 0.35 && countNonEmptyText(slide.Nodes) <= 2 {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				Code:       "sparse_slide",
				Severity:   "warning",
				Message:    "This slide leaves most of the canvas unused. Use a recipe-* layout (split-narrative, three-up, two-up, …) and fill every required slot so the story uses the page.",
			})
		}
		if fromTemplate && slideIsRecipeLayout(slide.Nodes) && slideHasEmptyEyebrow(slide.Nodes) {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				NodeID:     "eyebrow",
				Code:       "missing_eyebrow",
				Severity:   "warning",
				Message:    "Recipe slides need an eyebrow (chapter/section kicker). Fill the eyebrow slot; do not leave it blank.",
			})
		}
		if fromTemplate && si > 0 && slideHasNominalTitle(slide.Nodes) {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				Code:       "nominal_title",
				Severity:   "info",
				Message:    "Headline reads as a topic label. Prefer a complete-sentence takeaway or a two-line split headline that states the claim.",
			})
		}
		if fromTemplate && !slideHasVisibleTitle(slide.Nodes) {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				Code:       "missing_title",
				Severity:   "warning",
				Message:    "This slide has no real title. Fill the title slot with a 3–8 word topic name — not the first fact, and not a blank.",
			})
		}
		for _, id := range slideEmptyCardIDs(slide.Nodes) {
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				NodeID:     id,
				Code:       "empty_card",
				Severity:   "warning",
				Message:    "A designed card/box is empty. Fill every text slot on this layout, or pick a pattern whose slot count matches the content you have. Do not leave colored components blank.",
			})
		}
		walkNodes(slide.Nodes, func(n Node) {
			if !strings.HasPrefix(n.ID, "ph-pic-") || n.Type == "image" {
				return
			}
			findings = append(findings, ReviewFinding{
				SlideIndex: si,
				NodeID:     n.ID,
				Code:       "unfilled_image_slot",
				Severity:   "warning",
				Message:    "Image slot is still an empty panel. Pass a sha256- asset-ref in fill_slide or pick a title variant that already has a photo.",
			})
		})
	}

	if len(sparseIdx) > 1 {
		findings = append(findings, ReviewFinding{
			SlideIndex: sparseIdx[0],
			Code:       "sparse_section",
			Severity:   "warning",
			Message: fmt.Sprintf(
				"%d slides are title-only dividers (positions %v). Do not insert empty section dividers; put each chapter on a recipe-* content slide with an eyebrow.",
				len(sparseIdx), sparseIdx),
		})
	}

	checklist := make([]string, len(reviewChecklist))
	copy(checklist, reviewChecklist)
	return ReviewDeckResult{
		Deck:       deckView(deck),
		SlideCount: len(scene.Slides),
		Findings:   findings,
		Checklist:  checklist,
		Message:    "Self-review complete. Fix warnings, then re-run review_deck; only declare the deck done when there are no warning-level findings.",
	}, nil
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
	deck, _, err := svc.Deck(ctx, resolveWorkingDeckSlug(ctx, svc, args.DeckSlug))
	if err != nil {
		return ListDeckAssetsResult{}, err
	}
	if deck == nil {
		return ListDeckAssetsResult{}, fmt.Errorf("deck not found")
	}
	catalog := assetCatalog(deck.Assets)
	if deck.Theme != nil {
		if name := strings.TrimSpace(deck.Theme[themeKeyTemplateName]); name != "" {
			if tmpl, ok := svc.resolveTemplate(ctx, name); ok {
				catalog = mergeAssetCatalogs(catalog, assetCatalog(tmpl.Assets))
			}
		}
	}
	return ListDeckAssetsResult{Deck: deckView(deck), Assets: catalog}, nil
}

func mergeAssetCatalogs(a, b []AssetInfo) []AssetInfo {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]AssetInfo, 0, len(a)+len(b))
	for _, info := range a {
		if info.Ref == "" || seen[info.Ref] {
			continue
		}
		seen[info.Ref] = true
		out = append(out, info)
	}
	for _, info := range b {
		if info.Ref == "" || seen[info.Ref] {
			continue
		}
		seen[info.Ref] = true
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// AddDeckImageArgs defines the add_deck_image tool input.
type AddDeckImageArgs struct {
	DeckSlug string `json:"deck_slug" jsonschema:"Persist slug from create_deck, or the hint — in chat the server remaps a hint onto this session's draft."`
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
	slug := resolveWorkingDeckSlug(ctx, svc, args.DeckSlug)
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
		{"create_deck", "Create a new private Astonish Slides deck AFTER ask_user intake (audience, length, who picks the template, title variant, cover photo) unless those answers are already explicit in the user message. Using existing knowledge / skipping search is not a skip. Pass template. titleKind is required when the template has 2+ title* covers; titleImage is required (sha256-… or none) when that cover has a ph-pic well. Optional palette, closingKind. Seeds theme, fonts, and a slim catalog. Author with fill_slides; do not copy markup.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "create_deck", Description: "Create a new private Astonish Slides deck AFTER ask_user intake (audience, length, who picks the template, title variant, cover photo) unless those answers are already explicit in the user message. Using existing knowledge / skipping search is not a skip. Pass template. titleKind is required when the template has 2+ title* covers; titleImage is required (sha256-… or none) when that cover has a ph-pic well. Optional palette, closingKind. Seeds theme, fonts, and a slim catalog. Author with fill_slides; do not copy markup."}, func(ctx tool.Context, args CreateDeckArgs) (DeckResult, error) {
				return createDeck(ctx, args)
			})
		}},
		{"fill_slides", "Write many slides in one call from template catalog entries. Pass slides: [{position, kind or label, fills}]. Prefer this over fill_slide so the whole deck is authored in one LLM turn. Slide 0 is the official title family when the catalog lists title*; last slide is official closing* when listed; body is recipe-*.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "fill_slides", Description: "Write many slides in one call from template catalog entries. Pass slides: [{position, kind or label, fills}]. Prefer this over fill_slide so the whole deck is authored in one LLM turn. Slide 0 is the official title family when the catalog lists title*; last slide is official closing* when listed; body is recipe-*."}, func(ctx tool.Context, args FillSlidesArgs) (FillSlidesResult, error) {
				return fillSlides(ctx, args)
			})
		}},
		{"fill_slide", "Write or replace ONE slide from a template catalog entry. Use fill_slides to author the whole deck; use this only for a later single-slide edit.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "fill_slide", Description: "Write or replace ONE slide from a template catalog entry. Use fill_slides to author the whole deck; use this only for a later single-slide edit."}, func(ctx tool.Context, args FillSlideArgs) (FillSlideResult, error) {
				return fillSlide(ctx, args)
			})
		}},
		{"get_archetype", "Fetch a single template archetype's markup by kind or label. Escape hatch only — prefer fill_slide, which does not require copying markup.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_archetype", Description: "Fetch a single template archetype's markup by kind or label. Escape hatch only — prefer fill_slide, which does not require copying markup."}, func(ctx tool.Context, args GetArchetypeArgs) (GetArchetypeResult, error) {
				return getArchetype(ctx, args)
			})
		}},
		{"write_slide", "Write one slide as a complete ast-slide fragment. Only for blank-canvas decks (no template). When create_deck returned a catalog, use fill_slides instead.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "write_slide", Description: "Write one slide as a complete ast-slide fragment. Only for blank-canvas decks (no template). When create_deck returned a catalog, use fill_slides instead."}, func(ctx tool.Context, args WriteSlideArgs) (WriteSlideResult, error) {
				return writeSlide(ctx, args)
			})
		}},
		{"get_deck", "Read a private slide deck: identity, theme, asset catalog, and a slim slide index (id/position/title). Does not return slide markup — use read_slide for one slide.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_deck", Description: "Read a private slide deck: identity, theme, asset catalog, and a slim slide index (id/position/title). Does not return slide markup — use read_slide for one slide."}, func(ctx tool.Context, args GetDeckArgs) (DeckResult, error) {
				return getDeck(ctx, args)
			})
		}},
		{"read_slide", "Read one slide's canonical ASD markup and notes by position.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "read_slide", Description: "Read one slide's canonical ASD markup and notes by position."}, func(ctx tool.Context, args ReadSlideArgs) (WriteSlideResult, error) {
				return readSlide(ctx, args)
			})
		}},
		{"list_decks", "List private Astonish Slides decks available to the current user.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_decks", Description: "List private Astonish Slides decks available to the current user."}, func(ctx tool.Context, args ListDecksArgs) (ListDecksResult, error) {
				return listDecks(ctx, args)
			})
		}},
		{"list_slide_templates", "List available slide templates (built-in + imported) as a lightweight catalog: name, label, description, scope, palettes (id/label when the template has colorways), and archetype variants {kind,label,tier,fillSlots} — no markup, tokens, or assets. Use palettes and title*/closing* counts to decide which ask_user questions to ask. Do not confuse with email MCP list_templates.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "list_slide_templates", Description: "List available slide templates (built-in + imported) as a lightweight catalog: name, label, description, scope, palettes (id/label when the template has colorways), and archetype variants {kind,label,tier,fillSlots} — no markup, tokens, or assets. Use palettes and title*/closing* counts to decide which ask_user questions to ask. Do not confuse with email MCP list_templates."}, func(ctx tool.Context, args ListTemplatesArgs) (ListTemplatesResult, error) {
				return listTemplates(ctx, args)
			})
		}},
		{"get_template_variant_previews", "Get lightweight per-variant previews for a template role (kind is required). Returns {kind,label,tier,fillSlots,thumbnailRef} — never full ASD markup or image bytes. Prefer ask_user with slidesTemplate for a visual picker.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_template_variant_previews", Description: "Get lightweight per-variant previews for a template role (kind is required). Returns {kind,label,tier,fillSlots,thumbnailRef} — never full ASD markup or image bytes. Prefer ask_user with slidesTemplate for a visual picker."}, func(ctx tool.Context, args TemplateVariantPreviewsArgs) (TemplateVariantPreviewsResult, error) {
				return getTemplateVariantPreviews(ctx, args)
			})
		}},
		{"validate_deck", "Validate every persisted slide in a private deck and return structured ASD diagnostics.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "validate_deck", Description: "Validate every persisted slide in a private deck and return structured ASD diagnostics."}, func(ctx tool.Context, args ValidateDeckArgs) (ValidateDeckResult, error) {
				return validateDeck(ctx, args)
			})
		}},
		{"review_deck", "Self-review a finished deck as the FINAL step before you tell the user it is ready. Renders the persisted scene and returns heuristic findings (run_adjacency = collided date/label text like \"1972Founded\"; low_contrast markers; missing_chrome = a template slide that dropped the logo/footer; weak_source = a vague citation) plus a review checklist. Fix EVERY warning-level finding, then call review_deck again; only declare the deck done when there are no warnings. This catches semantic/visual defects that validate_deck (structural only) does not. Returns text findings only, never image data.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "review_deck", Description: "Self-review a finished deck as the FINAL step before you tell the user it is ready. Renders the persisted scene and returns heuristic findings (run_adjacency = collided date/label text like \"1972Founded\"; low_contrast markers; missing_chrome = a template slide that dropped the logo/footer; weak_source = a vague citation) plus a review checklist. Fix EVERY warning-level finding, then call review_deck again; only declare the deck done when there are no warnings. This catches semantic/visual defects that validate_deck (structural only) does not. Returns text findings only, never image data."}, func(ctx tool.Context, args ReviewDeckArgs) (ReviewDeckResult, error) {
				return reviewDeck(ctx, args)
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
		{"ask_user", "Ask the user ONE structured question inline in chat and WAIT for their reply. kind='yesno' shows Yes/No; kind='select' shows a pick-one list. Intake: audience, length, who picks the template. If they want to choose, slidesTemplatePicker=true (kind='select', no options) shows a live cover thumbnail per template. After a template is known, slidesTemplate+slidesKind=title|closing shows official cover/end LAYOUTS (sample photos stripped). If the chosen title has a ph-pic well, yes/no then slidesImagePicker=true with slidesTemplate lists template photos. slidesPalettePicker=true with slidesTemplate shows Product Deck colorways. Do NOT hand-copy markup. After calling it, end your turn.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "ask_user", Description: "Ask the user ONE structured question inline in chat and WAIT for their reply. kind='yesno' shows Yes/No; kind='select' shows a pick-one list. Intake: audience, length, who picks the template. If they want to choose, slidesTemplatePicker=true (kind='select', no options) shows a live cover thumbnail per template. After a template is known, slidesTemplate+slidesKind=title|closing shows official cover/end LAYOUTS (sample photos stripped). If the chosen title has a ph-pic well, yes/no then slidesImagePicker=true with slidesTemplate lists template photos. slidesPalettePicker=true with slidesTemplate shows Product Deck colorways. Do NOT hand-copy markup. After calling it, end your turn."}, func(ctx tool.Context, args AskUserArgs) (AskUserResult, error) {
				return askUser(ctx, args)
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
