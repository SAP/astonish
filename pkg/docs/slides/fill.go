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
const themeKeyTemplateName = "template-name"

func isThemeMetaKey(key string) bool {
	return key == embeddedFontsThemeKey || key == themeKeyTemplateName
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
				return a, nil
			}
		}
		base := stripVariantSuffix(kind)
		for _, a := range tmpl.Archetypes {
			if stripVariantSuffix(a.Kind) == base {
				return a, nil
			}
		}
		return themes.Archetype{}, fmt.Errorf("no archetype with kind %q", kind)
	}
	want := strings.ToLower(label)
	var found *themes.Archetype
	for i := range tmpl.Archetypes {
		got := strings.ToLower(strings.TrimSpace(tmpl.Archetypes[i].Title))
		if got == want {
			return tmpl.Archetypes[i], nil
		}
		if found == nil && strings.Contains(got, want) {
			found = &tmpl.Archetypes[i]
		}
	}
	if found != nil {
		return *found, nil
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
// asset-ref become an ast-image at the same geometry. Unmentioned slots are
// left as-is; leftover {{TITLE}}/{{BODY}} is an error.
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

func recipeArchetypeFor(tmpl themes.Template, kind string, chrome Chrome, fills map[string]string) (themes.Archetype, error) {
	m, ok := recipeByKind(kind)
	if !ok {
		return themes.Archetype{}, fmt.Errorf("unknown recipe kind %q", kind)
	}
	markup, err := RenderRecipe(m.Kind, tmpl.StyleGuide, tmpl.Tokens, chrome, fills)
	if err != nil {
		return themes.Archetype{}, err
	}
	return themes.Archetype{
		Kind:      m.Kind,
		Title:     m.Title,
		Markup:    markup,
		Tier:      "flexible",
		FillSlots: requiredFillSlots(m.Slots),
		SlotHints: m.Slots,
	}, nil
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
		if !looksLikeAssetRef(value) {
			return "", fmt.Errorf("image slot %q requires an asset-ref (sha256-…), got %q", id, value)
		}
		geo := geometryAttrs(attrs)
		repl := `<ast-image id="` + html.EscapeString(id) + `" ` + geo + ` asset-ref="` + html.EscapeString(value) + `" fit="cover"></ast-image>`
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
	re := regexp.MustCompile(`\b` + name + `="(-?\d+)"`)
	m := re.FindStringSubmatch(tag)
	if len(m) < 2 {
		return 0
	}
	n := 0
	for _, c := range m[1] {
		if c == '-' {
			continue
		}
		n = n*10 + int(c-'0')
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
	re := regexp.MustCompile(`\bsize="\d+"`)
	return re.ReplaceAllString(open, fmt.Sprintf(`size="%d"`, size)), true
}

func geometryAttrs(attrs string) string {
	var parts []string
	for _, name := range []string{"x", "y", "w", "h", "rot", "flip-h", "flip-v"} {
		re := regexp.MustCompile(`\b` + name + `="([^"]*)"`)
		if m := re.FindStringSubmatch(attrs); len(m) == 2 {
			parts = append(parts, name+`="`+m[1]+`"`)
		}
	}
	return strings.Join(parts, " ")
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
	needle := `id="` + id + `"`
	idx := strings.Index(markup, needle)
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
