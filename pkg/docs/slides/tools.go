package slides

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
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

// TemplateVariantPreviewsArgs defines the get_template_variant_previews input.
type TemplateVariantPreviewsArgs struct {
	Template string `json:"template" jsonschema:"Template name (from list_templates) to fetch variant previews for."`
	Kind     string `json:"kind,omitempty" jsonschema:"Optional archetype role to filter by (e.g. title, section, agenda, closing, content). Omit for all roles."`
}

// TemplateVariantPreview is one archetype variant plus the render inputs a UI
// needs to show a live thumbnail: the ast-slide Markup and the template Theme
// tokens + Assets it references. This carries ASD text and asset-refs only — it
// never embeds data: image/font bytes (those resolve through the deck asset
// plumbing at render time), so it is safe to return from a tool.
type TemplateVariantPreview struct {
	Kind      string   `json:"kind"`
	Label     string   `json:"label,omitempty"`
	Tier      string   `json:"tier,omitempty"`
	FillSlots []string `json:"fillSlots,omitempty"`
	Markup    string   `json:"markup"`
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
	variants := make([]TemplateVariantPreview, 0, len(tmpl.Archetypes))
	for _, arch := range tmpl.Archetypes {
		// Imported templates preserve variant multiplicity by suffixing the
		// role: a template with several covers stores them as title, title-2,
		// title-3, … (see uniqueKind in import_worker.mjs). A caller filtering
		// by slidesKind="title" means the whole ROLE family, so match on the
		// base kind (strip any -N suffix), not an exact string.
		if wantKind != "" && stripVariantSuffix(arch.Kind) != wantKind {
			continue
		}
		variants = append(variants, TemplateVariantPreview{
			Kind:      arch.Kind,
			Label:     arch.Title,
			Tier:      arch.Tier,
			FillSlots: arch.FillSlots,
			Markup:    arch.Markup,
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
		if cover := coverArchetype(t); cover != nil && strings.TrimSpace(cover.Markup) != "" {
			pick.thumbnail = &AskUserThumbnail{
				OptionID: t.Name,
				Kind:     "slides-archetype",
				Markup:   cover.Markup,
				Theme:    t.Tokens,
				Template: t.Name,
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
	SlidesTemplate string `json:"slidesTemplate,omitempty" jsonschema:"For a slide-variant picker: the template name (from list_templates). ask_user auto-attaches a live thumbnail per option and, if options are omitted, generates one option per variant."`
	SlidesKind     string `json:"slidesKind,omitempty" jsonschema:"Optional archetype role to filter the variants by (e.g. title, section, agenda, closing, content). Only used with slidesTemplate."`
	// Slides convenience: for the FIRST question — "which template should I use?"
	// — set slidesTemplatePicker=true (with kind='select') and omit options.
	// ask_user then enumerates every available template (built-in + imported),
	// generates one option per template (id=template name, label+description from
	// the catalog), and attaches a live thumbnail of each template's cover so the
	// user picks by seeing the design. Do NOT hand-copy markup or thumbnails.
	SlidesTemplatePicker bool `json:"slidesTemplatePicker,omitempty" jsonschema:"For the template-choice question: set true (with kind='select', no options) to auto-generate one option per available template, each with a live thumbnail of that template's cover slide."`
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

	// Slides convenience: when slidesTemplate is set, resolve the template's
	// per-variant preview markup and (a) generate one option per variant if the
	// model omitted options, and (b) auto-attach a live slides-archetype
	// thumbnail to every option, matched by label (case-insensitive) then id.
	// This is what makes the picker VISUAL without the model copying markup.
	if template := strings.TrimSpace(args.SlidesTemplate); template != "" && kind == "select" {
		previews, err := getTemplateVariantPreviews(ctx, TemplateVariantPreviewsArgs{
			Template: template,
			Kind:     strings.TrimSpace(args.SlidesKind),
		})
		if err != nil {
			return AskUserResult{}, fmt.Errorf("resolve slide variant previews: %w", err)
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
				// IDs MUST be unique: the frontend keys option tiles by id, so two
				// variants that slugify to the same id (e.g. duplicate/empty labels)
				// would otherwise collapse into a single rendered tile. Disambiguate
				// collisions with a numeric suffix.
				id := slugifyOptionID(label)
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
			if !ok || strings.TrimSpace(v.Markup) == "" {
				continue
			}
			thumbByOption[o.ID] = AskUserThumbnail{
				OptionID: o.ID,
				Kind:     "slides-archetype",
				Markup:   v.Markup,
				Theme:    previews.Theme,
				Template: previews.Template,
			}
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
		{"get_template_variant_previews", "Get the per-variant preview markup for a template so you can show the user a VISUAL picker of variants (e.g. via ask_user thumbnails). Returns each archetype variant's {kind,label,tier,fillSlots,markup} plus the shared theme + assets. Optionally filter by kind (title|section|agenda|closing|content). Returns ASD markup and asset-refs only — never image/font bytes.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "get_template_variant_previews", Description: "Get the per-variant preview markup for a template so you can show the user a VISUAL picker of variants (e.g. via ask_user thumbnails). Returns each archetype variant's {kind,label,tier,fillSlots,markup} plus the shared theme + assets. Optionally filter by kind (title|section|agenda|closing|content). Returns ASD markup and asset-refs only — never image/font bytes."}, func(ctx tool.Context, args TemplateVariantPreviewsArgs) (TemplateVariantPreviewsResult, error) {
				return getTemplateVariantPreviews(ctx, args)
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
		{"ask_user", "Ask the user ONE structured question inline in chat and WAIT for their reply. kind='yesno' shows Yes/No buttons; kind='select' shows a pick-one list. For the TEMPLATE-CHOICE question ('which template should I use?'), set slidesTemplatePicker=true (kind='select', no options) — ask_user lists every available template with a LIVE THUMBNAIL of each template's cover. For a SLIDES VARIANT PICKER within a chosen template, set slidesTemplate (the template name) and optionally slidesKind (title|section|agenda|closing|content) — ask_user then shows a LIVE THUMBNAIL of each variant and can auto-generate the options; do NOT hand-copy markup. GENERIC: use it any time you need the user to choose. After calling it, end your turn — the user's next message is their answer.", func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{Name: "ask_user", Description: "Ask the user ONE structured question inline in chat and WAIT for their reply. kind='yesno' shows Yes/No buttons; kind='select' shows a pick-one list. For the TEMPLATE-CHOICE question ('which template should I use?'), set slidesTemplatePicker=true (kind='select', no options) — ask_user lists every available template with a LIVE THUMBNAIL of each template's cover. For a SLIDES VARIANT PICKER within a chosen template, set slidesTemplate (the template name) and optionally slidesKind (title|section|agenda|closing|content) — ask_user then shows a LIVE THUMBNAIL of each variant and can auto-generate the options; do NOT hand-copy markup. GENERIC: use it any time you need the user to choose. After calling it, end your turn — the user's next message is their answer."}, func(ctx tool.Context, args AskUserArgs) (AskUserResult, error) {
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
