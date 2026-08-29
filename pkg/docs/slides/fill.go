package slides

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// themeKeyTemplateName is stamped onto a deck's theme when create_deck seeds
// from a template so fill_slides can resolve archetypes without a schema change.
// It is a meta key, never a CSS variable.
const (
	themeKeyTemplateName = "template-name"
	themeKeyTitleKind    = "template-title-kind"
	themeKeyClosingKind  = "template-closing-kind"
	themeKeyPalette      = "template-palette"
	themeKeyTitleImage   = "template-title-image"
)

// heroPhotoMinArea is the floor at which an ast-image is a sample hero photo
// (cover people/bikes), not a logo. Matches the importer's global-hero floor.
const heroPhotoMinArea = 400 * 300

func isThemeMetaKey(key string) bool {
	switch key {
	case embeddedFontsThemeKey, themeKeyTemplateName, themeKeyTitleKind, themeKeyClosingKind, themeKeyPalette, themeKeyTitleImage:
		return true
	default:
		return false
	}
}

// overlayDeckTheme copies non-meta color/type tokens from the session deck onto
// the template so recipe skins follow create_deck palette/theme overlays.
func overlayDeckTheme(tmpl themes.Template, deckTheme map[string]string) themes.Template {
	if len(deckTheme) == 0 {
		return tmpl
	}
	merged := cloneStringMap(tmpl.Tokens)
	if merged == nil {
		merged = make(map[string]string)
	}
	for k, v := range deckTheme {
		if isThemeMetaKey(k) || strings.TrimSpace(v) == "" {
			continue
		}
		merged[k] = v
	}
	tmpl.Tokens = merged
	return tmpl
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// agentCatalogBookends are the imported title/closing family shown to the model.
// pattern-*, section, agenda, and extra content samples stay on disk and remain
// fetchable via get_archetype but are not in the create_deck catalog.
func agentCatalogBookends(tmpl themes.Template) []themes.Archetype {
	out := make([]themes.Archetype, 0)
	for _, a := range tmpl.Archetypes {
		if !isAgentCatalogBookend(tmpl, a) {
			continue
		}
		out = append(out, restoreOfficialPictureWells(tmpl, a))
	}
	return out
}

// restoreOfficialPictureWells re-inserts a split-cover picture well that import
// omitted (empty full-bleed cyan-slab guard). GCO "White cover with blue pattern"
// keeps a right-hand ~half-page well at the template muted color; without it the
// filled slide is a blank white page while the picker thumbnail still shows blue.
func restoreOfficialPictureWells(tmpl themes.Template, arch themes.Archetype) themes.Archetype {
	base := stripVariantSuffix(arch.Kind)
	if base != "title" && base != "closing" {
		return arch
	}
	if tmpl.Model == nil {
		return arch
	}
	var layout *themes.IRLayout
	for i := range tmpl.Model.Layouts {
		if tmpl.Model.Layouts[i].Name == arch.Title {
			layout = &tmpl.Model.Layouts[i]
			break
		}
	}
	if layout == nil {
		return arch
	}
	next := 1
	for strings.Contains(arch.Markup, fmt.Sprintf(`id="ph-pic-%d"`, next)) {
		next++
	}
	muted := mutedColor(tmpl.Tokens)
	for _, ph := range layout.Placeholders {
		if ph.Type != "image" {
			continue
		}
		if ph.W < 200 || ph.H < 200 {
			continue
		}
		if ph.W >= CanvasWidth*9/10 && ph.H >= CanvasHeight*9/10 {
			continue
		}
		id := fmt.Sprintf("ph-pic-%d", next)
		next++
		fill := muted
		if c := strings.TrimSpace(ph.Fill); strings.HasPrefix(c, "#") {
			fill = c
		}
		well := fmt.Sprintf(`<ast-shape id="%s" kind="rect" x="%d" y="%d" w="%d" h="%d" geom="rect" fill="%s" alt="" decorative="true"></ast-shape>`,
			id, ph.X, ph.Y, ph.W, ph.H, fill)
		arch.Markup = injectAfterBackground(arch.Markup, well)
		seen := false
		for _, s := range arch.FillSlots {
			if s == id {
				seen = true
				break
			}
		}
		if !seen {
			arch.FillSlots = append(arch.FillSlots, id)
			arch.SlotHints = append(arch.SlotHints, themes.SlotHint{ID: id, Role: "image", Hint: "template picture well"})
		}
	}
	return arch
}

func mutedColor(tokens map[string]string) string {
	if v := strings.TrimSpace(tokens["muted"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(tokens["accent2"]); v != "" {
		return v
	}
	return "#89D1FF"
}

func isOfficialBookendKind(kind string) bool {
	base := stripVariantSuffix(kind)
	return base == "title" || base == "closing"
}

func firstImageSlotID(arch themes.Archetype) string {
	for _, id := range arch.FillSlots {
		if isImageFillSlot(arch, id) {
			return id
		}
	}
	return ""
}

func normalizeCoverPhotoRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.EqualFold(ref, "none") || strings.EqualFold(ref, "default") {
		return ""
	}
	return ref
}

// applyCoverPhotoFill puts the user-chosen template photo into the cover's
// single image well when the model omitted ph-pic fills.
func applyCoverPhotoFill(arch themes.Archetype, fills map[string]string, titleImage string) map[string]string {
	if stripVariantSuffix(arch.Kind) != "title" && arch.Kind != RecipeCover {
		return fills
	}
	slot := firstImageSlotID(arch)
	if slot == "" && arch.Kind == RecipeCover {
		slot = "ph-pic-1"
	}
	if slot == "" {
		return fills
	}
	if strings.TrimSpace(fills[slot]) != "" {
		return fills
	}
	ref := normalizeCoverPhotoRef(titleImage)
	if ref == "" {
		return fills
	}
	out := cloneStringMap(fills)
	if out == nil {
		out = map[string]string{}
	}
	out[slot] = ref
	return out
}

// withRecipeCoverImageFill injects the stamped cover logo/photo into recipe
// fills before RenderRecipe, so an optional ph-pic-1 well is actually emitted.
func withRecipeCoverImageFill(kind string, fills map[string]string, titleImage string) map[string]string {
	if kind != RecipeCover {
		return fills
	}
	return applyCoverPhotoFill(themes.Archetype{Kind: RecipeCover}, fills, titleImage)
}

func injectAfterBackground(markup, well string) string {
	if well == "" || markup == "" {
		return markup
	}
	const closeShape = "</ast-shape>"
	if i := strings.Index(markup, closeShape); i > 0 && strings.Contains(markup[:i], `id="bg"`) {
		at := i + len(closeShape)
		return markup[:at] + well + markup[at:]
	}
	if i := strings.Index(markup, ">"); i >= 0 {
		return markup[:i+1] + well + markup[i+1:]
	}
	return markup + well
}

func isAgentCatalogBookend(tmpl themes.Template, a themes.Archetype) bool {
	base := stripVariantSuffix(a.Kind)
	if base != "title" && base != "closing" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(a.Tier), "fixed") || tmpl.Model != nil {
		return true
	}
	// Built-in generic title/section/content skeletons (archetypesFor) are not
	// official brand bookends — the model should use recipe-cover / recipe-closer.
	if _, builtin := themes.LookupTemplate(tmpl.Name); builtin && strings.TrimSpace(tmpl.Scope) != "scope" {
		return false
	}
	return true
}

func officialBookendKinds(tmpl themes.Template, role string) []string {
	role = strings.TrimSpace(role)
	var out []string
	for _, a := range agentCatalogBookends(tmpl) {
		if stripVariantSuffix(a.Kind) == role {
			out = append(out, a.Kind)
		}
	}
	return out
}

// ArchetypeCatalogEntry is the slim per-layout/pattern description returned by
// create_deck. Markup is deliberately omitted — fill_slides applies fills
// server-side.
type ArchetypeCatalogEntry struct {
	Kind         string            `json:"kind"`
	Label        string            `json:"label,omitempty"`
	Tier         string            `json:"tier,omitempty"`
	FillSlots    []string          `json:"fillSlots,omitempty"`
	SlotHints    []themes.SlotHint `json:"slotHints,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	ThumbnailRef string            `json:"thumbnailRef,omitempty"`
}

func catalogFrom(archetypes []themes.Archetype) []ArchetypeCatalogEntry {
	out := make([]ArchetypeCatalogEntry, 0, len(archetypes))
	for _, a := range archetypes {
		label := strings.TrimSpace(a.Title)
		summary := label
		if summary == "" {
			summary = a.Kind
		}
		cardTotal := 0
		for _, h := range a.SlotHints {
			if m := cardOfRe.FindStringSubmatch(h.Hint); len(m) == 2 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > cardTotal {
					cardTotal = n
				}
			}
		}
		if cardTotal >= 2 {
			summary = fmt.Sprintf("%s — %d cards; title plus a sentence in every card", label, cardTotal)
		} else if n := strings.Count(a.Markup, `geom="roundRect"`); n >= 2 {
			summary = fmt.Sprintf("%s — %d rounded cards; fill every card", summary, n)
		}
		out = append(out, ArchetypeCatalogEntry{
			Kind:         a.Kind,
			Label:        label,
			Tier:         a.Tier,
			FillSlots:    a.FillSlots,
			SlotHints:    a.SlotHints,
			Summary:      summary,
			ThumbnailRef: a.ThumbnailRef,
		})
	}
	return out
}

func findTemplateArchetype(tmpl themes.Template, kind, label string) (themes.Archetype, error) {
	kind = strings.TrimSpace(kind)
	label = strings.TrimSpace(label)
	if kind == "" && label == "" {
		return themes.Archetype{}, fmt.Errorf("kind or label is required")
	}
	if kind != "" {
		if isRecipeKind(kind) {
			return recipeArchetypeFor(tmpl, kind, ExtractChrome(tmpl), nil)
		}
		for _, a := range tmpl.Archetypes {
			if a.Kind == kind {
				return restoreOfficialPictureWells(tmpl, a), nil
			}
		}
		base := stripVariantSuffix(kind)
		for _, a := range tmpl.Archetypes {
			if stripVariantSuffix(a.Kind) == base {
				return restoreOfficialPictureWells(tmpl, a), nil
			}
		}
		return themes.Archetype{}, fmt.Errorf("no archetype with kind %q", kind)
	}
	want := strings.ToLower(label)
	var found *themes.Archetype
	for i := range tmpl.Archetypes {
		got := strings.ToLower(strings.TrimSpace(tmpl.Archetypes[i].Title))
		if got == want {
			return restoreOfficialPictureWells(tmpl, tmpl.Archetypes[i]), nil
		}
		if found == nil && strings.Contains(got, want) {
			found = &tmpl.Archetypes[i]
		}
	}
	if found != nil {
		return restoreOfficialPictureWells(tmpl, *found), nil
	}
	for _, m := range allRecipeMeta() {
		if strings.ToLower(m.Title) == want {
			return recipeArchetypeFor(tmpl, m.Kind, ExtractChrome(tmpl), nil)
		}
	}
	return themes.Archetype{}, fmt.Errorf("no archetype with label %q", label)
}

var assetRefRe = regexp.MustCompile(`^(sha256-[0-9a-fA-F]+|thumb/.+)$`)

func looksLikeAssetRef(s string) bool {
	s = strings.TrimSpace(s)
	return assetRefRe.MatchString(s)
}

// fillArchetypeMarkup copies archetype markup and substitutes fillSlots.
// Text slots (ast-text) get their inner content replaced with a single ast-run.
// Image slots (ast-image or ast-shape id=ph-pic-*) whose value looks like an
// asset-ref become an ast-image at the same geometry. Unmentioned slots and
// fill keys that are not in the markup are left as-is / ignored; leftover
// {{TITLE}}/{{BODY}} is an error.
func fillArchetypeMarkup(markup string, fills map[string]string) (string, error) {
	out := markup
	for id, raw := range fills {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if isRecipeControlFill(id) {
			continue
		}
		if !markupHasID(out, id) {
			// Image fills that target a missing ph-pic-* slot must fail loudly
			// so the model doesn't hallucinate that an image was placed.
			if strings.HasPrefix(id, "ph-pic-") && looksLikeAssetRef(value) {
				return "", fmt.Errorf("image slot %q does not exist in this layout — preserve the layout and use add_slide_image after this slide is written", id)
			}
			// Other extra keys (optional slots the skin does not emit, typos)
			// are ignored so a product cover fill of meta_4 is not a hard error.
			continue
		}
		next, err := replaceSlot(out, id, value)
		if err != nil {
			return "", err
		}
		out = next
	}
	if strings.Contains(out, "{{TITLE}}") || strings.Contains(out, "{{BODY}}") {
		return "", fmt.Errorf("unfilled template placeholders remain ({{TITLE}}/{{BODY}}); pass a fill for every text slot")
	}
	return out, nil
}

// aliasOfficialBookendFills maps recipe-ish names (headline/dek) onto an
// imported title/closing's real slots (ph-* or {{TITLE}}/{{BODY}}). Recipe
// fills are left unchanged.
func aliasOfficialBookendFills(arch themes.Archetype, fills map[string]string) map[string]string {
	if isRecipeKind(arch.Kind) || len(fills) == 0 {
		return fills
	}
	base := stripVariantSuffix(arch.Kind)
	if base != "title" && base != "closing" {
		return fills
	}
	out := cloneStringMap(fills)
	if out == nil {
		out = make(map[string]string)
	}
	titleID := firstNonEmpty(
		slotIDContaining(arch.Markup, "{{TITLE}}"),
		firstSlotWithRole(arch, "title", "heading"),
		nthFillSlot(arch, 0),
	)
	bodyID := firstNonEmpty(
		slotIDContaining(arch.Markup, "{{BODY}}"),
		firstSlotWithRole(arch, "body", "subtitle", "caption"),
		nthFillSlot(arch, 1),
	)
	aliasInto(out, titleID, "headline", "title", "TITLE")
	aliasInto(out, bodyID, "dek", "subtitle", "body", "BODY")
	return out
}

func aliasInto(fills map[string]string, dest string, aliases ...string) {
	dest = strings.TrimSpace(dest)
	if dest == "" || strings.TrimSpace(fills[dest]) != "" {
		return
	}
	for _, a := range aliases {
		if v := strings.TrimSpace(fills[a]); v != "" {
			fills[dest] = v
			return
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nthFillSlot(arch themes.Archetype, i int) string {
	if i < 0 || i >= len(arch.FillSlots) {
		return ""
	}
	return arch.FillSlots[i]
}

func firstSlotWithRole(arch themes.Archetype, roles ...string) string {
	want := make(map[string]bool, len(roles))
	for _, r := range roles {
		want[strings.ToLower(strings.TrimSpace(r))] = true
	}
	for _, h := range arch.SlotHints {
		if want[strings.ToLower(strings.TrimSpace(h.Role))] {
			return h.ID
		}
	}
	return ""
}

var astTextOpenRe = regexp.MustCompile(`<ast-text\b([^>]*)>`)

func slotIDContaining(markup, needle string) string {
	if needle == "" || markup == "" {
		return ""
	}
	locs := astTextOpenRe.FindAllStringSubmatchIndex(markup, -1)
	for _, idx := range locs {
		attrs := markup[idx[2]:idx[3]]
		id := attrValue(attrs, "id")
		if id == "" {
			continue
		}
		rest := markup[idx[1]:]
		end := strings.Index(rest, "</ast-text>")
		if end < 0 {
			continue
		}
		if strings.Contains(rest[:end], needle) {
			return id
		}
	}
	return ""
}

func attrValue(attrs, name string) string {
	start, end, ok := attrValueRange(attrs, name)
	if !ok {
		return ""
	}
	return attrs[start:end]
}

func attrValueRange(attrs, name string) (int, int, bool) {
	prefix := name + `="`
	searchFrom := 0
	for searchFrom < len(attrs) {
		rel := strings.Index(attrs[searchFrom:], prefix)
		if rel < 0 {
			return 0, 0, false
		}
		idx := searchFrom + rel
		if idx == 0 || attrs[idx-1] == '<' || isAttrSpace(attrs[idx-1]) {
			start := idx + len(prefix)
			if relEnd := strings.IndexByte(attrs[start:], '"'); relEnd >= 0 {
				return start, start + relEnd, true
			}
			return 0, 0, false
		}
		searchFrom = idx + len(prefix)
	}
	return 0, 0, false
}

func isAttrSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func isRecipeControlFill(id string) bool {
	switch id {
	case "emphasis", "headline_accent", "dek_accent", "thesis_accent":
		return true
	default:
		return false
	}
}

func recipeArchetypeFor(tmpl themes.Template, kind string, chrome Chrome, fills map[string]string) (themes.Archetype, error) {
	m, ok := recipeByKind(kind)
	if !ok {
		return themes.Archetype{}, fmt.Errorf("unknown recipe kind %q", kind)
	}
	markup, err := RenderRecipe(m.Kind, SkinFor(tmpl), tmpl.StyleGuide, chrome, fills)
	if err != nil {
		return themes.Archetype{}, err
	}
	hints := recipeSlotsInMarkup(m.Slots, markup)
	if m.Kind == RecipeCover && SkinFor(tmpl).ID == SkinProduct {
		for _, s := range m.Slots {
			if s.ID == "ph-pic-1" {
				hints = ensureSlotHint(hints, s)
				break
			}
		}
	}
	return themes.Archetype{
		Kind:      m.Kind,
		Title:     m.Title,
		Markup:    markup,
		Tier:      "flexible",
		FillSlots: requiredFillSlots(hints),
		SlotHints: hints,
	}, nil
}

func ensureSlotHint(hints []themes.SlotHint, extra themes.SlotHint) []themes.SlotHint {
	for _, h := range hints {
		if h.ID == extra.ID {
			return hints
		}
	}
	return append(hints, extra)
}

// recipeSlotsInMarkup keeps slot hints that the rendered markup actually has,
// plus control fills (emphasis / *_accent) which never appear as DOM ids.
// Product cover has no meta_4; product closer has no thesis/item cards.
func recipeSlotsInMarkup(slots []themes.SlotHint, markup string) []themes.SlotHint {
	out := make([]themes.SlotHint, 0, len(slots))
	for _, s := range slots {
		if isRecipeControlFill(s.ID) {
			out = append(out, s)
			continue
		}
		if markupHasID(markup, s.ID) {
			out = append(out, s)
		}
	}
	return out
}

func markupHasID(markup, id string) bool {
	_, _, _, _, _, _, ok := findElement(markup, id)
	return ok
}

func slotHintRole(arch themes.Archetype, id string) string {
	for _, h := range arch.SlotHints {
		if h.ID == id {
			return strings.ToLower(strings.TrimSpace(h.Role))
		}
	}
	return ""
}

func isImageFillSlot(arch themes.Archetype, id string) bool {
	if strings.HasPrefix(id, "ph-pic-") {
		return true
	}
	return slotHintRole(arch, id) == "image"
}

// missingTextSlotFills lists text fillSlots that have no non-empty fill.
// Image slots are optional. Built-in archetypes with no FillSlots skip the check.
func missingTextSlotFills(arch themes.Archetype, fills map[string]string) []string {
	if len(arch.FillSlots) == 0 {
		return nil
	}
	var missing []string
	for _, id := range arch.FillSlots {
		if isImageFillSlot(arch, id) {
			continue
		}
		if slotHintRole(arch, id) == "optional" {
			continue
		}
		if strings.TrimSpace(fills[id]) == "" {
			missing = append(missing, id)
		}
	}
	return missing
}

func replaceSlot(markup, id, value string) (string, error) {
	start, tag, attrs, innerStart, innerEnd, closeEnd, ok := findElement(markup, id)
	if !ok {
		return "", fmt.Errorf("fill slot %q not found in archetype markup", id)
	}
	if tag == "ast-text" {
		if looksLikePlaceholderFill(value) {
			return "", fmt.Errorf("slot %q fill looks like leftover template dummy text %q; write real content that fits the box", id, value)
		}
		open := markup[start:innerStart]
		if shrunk, ok := shrinkOpenTagToFit(open, value); ok {
			open = shrunk
		}
		inner := "<ast-run>" + html.EscapeString(value) + "</ast-run>"
		return markup[:start] + open + inner + markup[innerEnd:], nil
	}
	if (tag == "ast-image" || tag == "ast-shape") && (strings.HasPrefix(id, "ph-pic-") || looksLikeAssetRef(value)) {
		if !looksLikeAssetRef(value) && !strings.HasPrefix(value, "sha256-") {
			return "", fmt.Errorf("image slot %q requires an asset-ref (sha256-…), got %q", id, value)
		}
		geo := geometryAttrs(attrs)
		fit := attrValue(attrs, "fit")
		if fit == "" {
			fit = "cover"
			if attrIntFrom(attrs, "w")*attrIntFrom(attrs, "h") < heroPhotoMinArea {
				fit = "contain"
			}
		}
		repl := `<ast-image id="` + html.EscapeString(id) + `" ` + geo + ` asset-ref="` + html.EscapeString(value) + `" fit="` + html.EscapeString(fit) + `"></ast-image>`
		return markup[:start] + repl + markup[closeEnd:], nil
	}
	return "", fmt.Errorf("slot %q is a <%s>; pass text for ast-text slots or an asset-ref for image slots", id, tag)
}

var cardOfRe = regexp.MustCompile(`(?i)card \d+ of (\d+)`)

var placeholderFillRe = regexp.MustCompile(`(?i)<[a-z][\w-]*>|yyyy[-_/]?mm|click to (add|edit)|^\(?r\)?\s*footnote$|^(draft|confidential|sample|dummy data|for discussion|final slide|backup|update data)$|goes here|\bhere\b.+\bhere\b|^(your |presenter |speaker |author )?name\b|^[a-z][a-z0-9 /&-]{1,48}:$|\bmonth\s*0+\b|^add .{0,40}(logo|photo|image|picture)\b`)

func looksLikePlaceholderFill(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && placeholderFillRe.MatchString(t)
}

func attrIntFrom(tag, name string) int {
	n, err := strconv.Atoi(attrValue(tag, name))
	if err != nil {
		return 0
	}
	return n
}

func textFitsBox(w, h, size int, text string) bool {
	if size <= 0 || w <= 0 || h <= 0 {
		return true
	}
	charW := float64(size) * 0.52
	if charW < 1 {
		charW = 1
	}
	cols := int(float64(w) / charW)
	if cols < 4 {
		cols = 4
	}
	lines, n := 1, 0
	for _, r := range text {
		if r == '\n' {
			lines++
			n = 0
			continue
		}
		n++
		if n > cols {
			lines++
			n = 1
		}
	}
	return float64(lines)*float64(size)*1.25 <= float64(h)+float64(size)*0.2
}

func shrinkOpenTagToFit(open, text string) (string, bool) {
	w := attrIntFrom(open, "w")
	h := attrIntFrom(open, "h")
	size := attrIntFrom(open, "size")
	if w <= 0 || h <= 0 || size <= 0 || textFitsBox(w, h, size, text) {
		return open, false
	}
	orig := size
	for size > 16 && !textFitsBox(w, h, size, text) {
		size -= 2
	}
	if size == orig {
		return open, false
	}
	return setIntAttr(open, "size", size), true
}

func geometryAttrs(attrs string) string {
	var parts []string
	for _, name := range []string{"x", "y", "w", "h", "rot", "flip-h", "flip-v"} {
		if value := attrValue(attrs, name); value != "" {
			parts = append(parts, name+`="`+value+`"`)
		}
	}
	return strings.Join(parts, " ")
}

var astImageTagRe = regexp.MustCompile(`<ast-image\b([^>]*)></ast-image>|<ast-image\b([^/][^>]*)/>`)

func imageTagAttrs(match []int, markup string) (attrs string) {
	// Submatch groups: (1) paired tag attrs, (2) self-closing attrs.
	if len(match) >= 4 && match[2] >= 0 {
		return markup[match[2]:match[3]]
	}
	if len(match) >= 6 && match[4] >= 0 {
		return markup[match[4]:match[5]]
	}
	return ""
}

// stripUnselectedHeroPhotos removes sample cover photos the user did not pick.
// Unfilled ph-pic-* slots become muted wells so split covers keep their
// geometry; large decorative ast-image heroes are dropped. Logos stay.
func stripUnselectedHeroPhotos(markup string, filled map[string]bool, muted string) string {
	if markup == "" {
		return markup
	}
	if muted == "" {
		muted = "#89D1FF"
	}
	matches := astImageTagRe.FindAllStringSubmatchIndex(markup, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		loc := matches[i]
		attrs := imageTagAttrs(loc, markup)
		id := attrValue(attrs, "id")
		if filled[id] {
			continue
		}
		area := attrIntFrom(attrs, "w") * attrIntFrom(attrs, "h")
		isPicSlot := strings.HasPrefix(id, "ph-pic-")
		isLogo := strings.Contains(strings.ToLower(id), "logo") || (area > 0 && area < heroPhotoMinArea)
		if isPicSlot {
			well := fmt.Sprintf(`<ast-shape id="%s" kind="rect" %s geom="rect" fill="%s" alt="" decorative="true"></ast-shape>`,
				html.EscapeString(id), geometryAttrs(attrs), muted)
			markup = markup[:loc[0]] + well + markup[loc[1]:]
			continue
		}
		if isLogo {
			continue
		}
		if area < heroPhotoMinArea {
			continue
		}
		w, h := attrIntFrom(attrs, "w"), attrIntFrom(attrs, "h")
		if w >= CanvasWidth*9/10 && h >= CanvasHeight*9/10 {
			markup = markup[:loc[0]] + markup[loc[1]:]
			continue
		}
		well := fmt.Sprintf(`<ast-shape id="%s" kind="rect" %s geom="rect" fill="%s" alt="" decorative="true"></ast-shape>`,
			html.EscapeString(id), geometryAttrs(attrs), muted)
		markup = markup[:loc[0]] + well + markup[loc[1]:]
	}
	return markup
}

// layoutPreviewMarkup is the title/closing picker preview: chrome and empty
// picture wells, never the template's sample people/bike photos.
func layoutPreviewMarkup(markup string, tokens map[string]string) string {
	return stripUnselectedHeroPhotos(markup, nil, mutedColor(tokens))
}

var assetRefAttrRe = regexp.MustCompile(`\basset-ref="([^"]+)"`)

// collectAssetRefs returns unique ast-image asset-ref values in markup.
func collectAssetRefs(markup string) []string {
	ms := assetRefAttrRe.FindAllStringSubmatch(markup, -1)
	if len(ms) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ms))
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		ref := strings.TrimSpace(m[1])
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// seedLightweightAssets copies only embedded fonts from a template asset map.
// Session decks must not inherit every sample-slide photo (tens of MB); fill
// copies the few refs the authored markup actually uses.
func seedLightweightAssets(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, v := range src {
		if strings.HasPrefix(k, "font:") && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func keepAssetKey(k string) bool {
	return strings.HasPrefix(k, "font:") || strings.HasPrefix(k, "thumb/") || strings.HasPrefix(k, "slidethumb/")
}

// findElement locates the element whose id attribute equals id. ASD markup is
// a constrained XML fragment (no prefixed ids, no CDATA), so a scan is enough.
func findElement(markup, id string) (start int, tag, attrs string, innerStart, innerEnd, closeEnd int, ok bool) {
	idx := idAttrIndex(markup, id)
	if idx < 0 {
		return 0, "", "", 0, 0, 0, false
	}
	lt := strings.LastIndex(markup[:idx], "<")
	if lt < 0 {
		return 0, "", "", 0, 0, 0, false
	}
	gt := strings.Index(markup[idx:], ">")
	if gt < 0 {
		return 0, "", "", 0, 0, 0, false
	}
	gt += idx
	openTag := markup[lt+1 : gt]
	selfClose := strings.HasSuffix(openTag, "/")
	fields := strings.Fields(strings.TrimSuffix(openTag, "/"))
	if len(fields) == 0 {
		return 0, "", "", 0, 0, 0, false
	}
	tag = fields[0]
	attrs = strings.TrimSpace(openTag[len(tag):])
	if selfClose {
		return lt, tag, attrs, gt + 1, gt + 1, gt + 1, true
	}
	close := "</" + tag + ">"
	end := strings.Index(markup[gt+1:], close)
	if end < 0 {
		return 0, "", "", 0, 0, 0, false
	}
	innerStart = gt + 1
	innerEnd = gt + 1 + end
	closeEnd = innerEnd + len(close)
	return lt, tag, attrs, innerStart, innerEnd, closeEnd, true
}

// idAttrIndex finds `id="foo"` as a complete attribute value so `headline`
// does not match `headline_2`.
func idAttrIndex(markup, id string) int {
	needle := `id="` + id + `"`
	start := 0
	for {
		idx := strings.Index(markup[start:], needle)
		if idx < 0 {
			return -1
		}
		idx += start
		end := idx + len(needle)
		if end == len(markup) || !isXMLNameChar(markup[end]) {
			return idx
		}
		start = end
	}
}

func isXMLNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// setElementXY rewrites the x/y attributes of the element with the given id.
func setElementXY(markup, id string, x, y int) (string, error) {
	return rewriteElementGeometry(markup, id, map[string]int{"x": x, "y": y})
}

// setElementGeometry rewrites all geometry attributes of the named element.
func setElementGeometry(markup, id string, x, y, w, h int) (string, error) {
	return rewriteElementGeometry(markup, id, map[string]int{"x": x, "y": y, "w": w, "h": h})
}

func rewriteElementGeometry(markup, id string, values map[string]int) (string, error) {
	start, tag, attrs, innerStart, innerEnd, closeEnd, ok := findElement(markup, id)
	if !ok {
		return "", fmt.Errorf("element %q not found", id)
	}
	attrs = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(attrs), "/"))
	for _, key := range []string{"x", "y", "w", "h"} {
		if value, ok := values[key]; ok {
			attrs = setIntAttr(attrs, key, value)
		}
	}
	selfClose := closeEnd == innerStart
	open := "<" + tag
	if strings.TrimSpace(attrs) != "" {
		open += " " + strings.TrimSpace(attrs)
	}
	if selfClose {
		open += "/>"
		return markup[:start] + open + markup[closeEnd:], nil
	}
	open += ">"
	return markup[:start] + open + markup[innerStart:innerEnd] + "</" + tag + ">" + markup[closeEnd:], nil
}

func setElementText(markup, id, text string) (string, error) {
	start, tag, attrs, innerStart, _, closeEnd, ok := findElement(markup, id)
	if !ok {
		return "", fmt.Errorf("element %q not found", id)
	}
	if tag != "ast-text" {
		return "", fmt.Errorf("cannot set text on <%s> %q", tag, id)
	}
	if closeEnd == innerStart {
		return "", fmt.Errorf("cannot set text on self-closing element %q", id)
	}
	attrs = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(attrs), "/"))
	open := "<" + tag
	if strings.TrimSpace(attrs) != "" {
		open += " " + strings.TrimSpace(attrs)
	}
	open += ">"
	return markup[:start] + open + html.EscapeString(text) + "</" + tag + ">" + markup[closeEnd:], nil
}

func removeElement(markup, id string) (string, error) {
	start, _, _, _, _, closeEnd, ok := findElement(markup, id)
	if !ok {
		return "", fmt.Errorf("element %q not found", id)
	}
	return markup[:start] + markup[closeEnd:], nil
}

func setIntAttr(attrs, key string, n int) string {
	if start, end, ok := attrValueRange(attrs, key); ok {
		return attrs[:start] + strconv.Itoa(n) + attrs[end:]
	}
	attrs = strings.TrimSpace(attrs)
	if attrs == "" {
		return fmt.Sprintf(`%s="%d"`, key, n)
	}
	return attrs + " " + fmt.Sprintf(`%s="%d"`, key, n)
}
