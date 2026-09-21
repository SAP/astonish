package slides

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// ReconstructScene generates a reconstruction deck scene from an imported
// template IR. For each source slide in model.Slides it selects the
// best-matching archetype, projects the source text/asset content into the
// archetype's fill slots, and emits a valid ASD v2 SceneGraph.
//
// The reconstruction must never contain raw OOXML XML. It is assembled
// purely from IR fields (themes.IRLayout, IRChrome, IRPlaceholder) combined
// with the imported archetypes and assets.
//
// Unsupported constructs (tables, charts, SmartArt) are recorded as warning
// strings on the returned slice; they are never silently dropped.
func ReconstructScene(model *themes.TemplateModel, archetypes []themes.Archetype, assets map[string]string) (*SceneGraph, []string, error) {
	scene, markups, warnings, err := reconstructCore(model, archetypes, assets)
	_ = markups
	return scene, warnings, err
}

// ReconstructMarkups is like ReconstructScene but also returns the per-slide
// ASD markup strings (after text/image projection, before node parsing).
// The i-th string corresponds to scene.Slides[i]. Used by the diagnostics tool
// to render the filled slide in the live-preview panel without re-running
// projection.
func ReconstructMarkups(model *themes.TemplateModel, archetypes []themes.Archetype, assets map[string]string) (*SceneGraph, []string, []string, error) {
	return reconstructCore(model, archetypes, assets)
}

// reconstructCore is the shared implementation of ReconstructScene and
// ReconstructMarkups.
func reconstructCore(model *themes.TemplateModel, archetypes []themes.Archetype, assets map[string]string) (*SceneGraph, []string, []string, error) {
	if model == nil {
		return nil, nil, nil, fmt.Errorf("reconstruct: model must not be nil")
	}

	var warnings []string

	// Propagate unsupported-construct warnings from the model.
	for _, w := range model.Warnings {
		msg := w.Message
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "table") || strings.Contains(lower, "chart") || strings.Contains(lower, "smartart") {
			warnings = append(warnings, msg)
		}
	}

	scene := &SceneGraph{
		SchemaVersion: SchemaV2,
		Theme:         model.Theme,
		Assets:        assets,
	}

	// Determine overall title from the first slide's title placeholder.
	if len(model.Slides) > 0 {
		scene.Title = firstTitleText(model.Slides[0])
	}
	if scene.Title == "" {
		scene.Title = "Reconstruction"
	}

	// Pre-compute archetype fingerprints once (parsing markup is not free).
	archFPs := buildArchetypeFingerprints(archetypes)

	markups := make([]string, 0, len(model.Slides))

	for i, srcSlide := range model.Slides {
		arch := selectArchetype(archetypes, archFPs, srcSlide, i)
		markup := arch.Markup

		// Project text content from source placeholders and text chrome objects.
		markup, warns := projectText(markup, arch.FillSlots, srcSlide.Placeholders, srcSlide.Objects)
		warnings = append(warnings, warns...)

		// Project image content from source chrome objects.
		markup, warns = projectImages(markup, srcSlide.Objects, assets)
		warnings = append(warnings, warns...)

		// Validate the projected markup; fall back to raw archetype markup on failure.
		slide, _, parseErr := ParseSlide(markup)
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("slide %d: projected markup invalid (%v); using raw archetype markup", i+1, parseErr))
			markup = arch.Markup
			slide, _, _ = ParseSlide(markup)
		}

		if slide.ID == "" {
			slide.ID = fmt.Sprintf("slide-%d", i+1)
		}
		if slide.Title == "" {
			slide.Title = firstTitleText(srcSlide)
		}

		markups = append(markups, markup)
		scene.Slides = append(scene.Slides, slide)
	}

	return scene, markups, warnings, nil
}

// archetypeFingerprint holds pre-parsed structural data for one archetype so
// selectArchetype can score without re-parsing markup on every call.
// sourceSlideIndex mirrors Archetype.SourceSlideIndex for fast Phase-0 lookup.
type archetypeFingerprint struct {
	SlideFingerprint
	// hasTextSlots is true when the archetype markup contains ≥1 fill slot.
	hasTextSlots bool
	// slotCount is the number of declared fill slots (FillSlots length).
	slotCount int
	// roundRectCount is the number of roundRect shapes in the archetype markup.
	// Used for count-based scoring (not just presence).
	roundRectCount int
	// sourceSlideIndex mirrors Archetype.SourceSlideIndex for O(1) Phase-0 lookup.
	// -1 means this archetype was not built from a specific source slide.
	sourceSlideIndex int
}

// buildArchetypeFingerprints parses each archetype's markup once and extracts
// its structural fingerprint.
func buildArchetypeFingerprints(archetypes []themes.Archetype) []archetypeFingerprint {
	fps := make([]archetypeFingerprint, len(archetypes))
	for i, a := range archetypes {
		fp := archetypeFingerprint{
			hasTextSlots:     len(a.FillSlots) > 0,
			slotCount:        len(a.FillSlots),
			sourceSlideIndex: a.SourceSlideIndex,
		}
		if a.Markup != "" {
			slide, _, err := ParseSlide(a.Markup)
			if err == nil {
				fp.SlideFingerprint = extractRecoFingerprint(i, slide)
				// RoundRectCount is now populated by extractRecoFingerprint.
				fp.roundRectCount = fp.SlideFingerprint.RoundRectCount
			}
		}
		fps[i] = fp
	}
	return fps
}

// selectArchetype picks the best-matching archetype for a source slide using a
// three-phase scoring strategy:
//
//  0. Direct index match: if any archetype has SourceSlideIndex == srcSlideIdx,
//     return it immediately. This guarantees slide N always gets the archetype
//     built FROM slide N (true per-slide fidelity). This phase fires for all
//     slides — including covers and closing slides — as long as the per-slide
//     pattern loop in import_worker.mjs generated an archetype for them.
//
//  1. Bookend detection (title/section/closing/agenda): if InferLayoutKind
//     returns one of these special kinds, score by kind name match first —
//     only look at shape families as a tiebreaker. This fires only when no
//     Phase-0 archetype exists (e.g. blank slides with no chrome objects).
//
//  2. Content slide structural matching: score by comparing the source IR
//     fingerprint against each archetype's parsed-markup fingerprint.
//     Features weighted:
//     - roundRect presence match   +4.0  (most visually distinctive)
//     - ellipse presence match     +1.5
//     - stripe presence match      +1.5
//     - chrome shape count ratio   +1.0 * clamped ratio
//     - slot count similarity      +3.0
//     - accent color overlap       +2.0
//     - kind name tiebreaker       +0.5
func selectArchetype(archetypes []themes.Archetype, archFPs []archetypeFingerprint, src themes.IRLayout, srcSlideIdx int) themes.Archetype {
	if len(archetypes) == 0 {
		return themes.Archetype{}
	}

	// Phase 0: direct source-slide-index match.
	// The import_worker assigns SourceSlideIndex (1-based, so slide 0 → value 1)
	// to every per-slide pattern archetype it generates. If we find an archetype
	// built from this exact source slide, use it immediately — no scoring needed.
	// SourceSlideIndex == 0 means "unset" (chrome-kind archetypes from layouts).
	want1Based := srcSlideIdx + 1
	for i, a := range archetypes {
		if archFPs[i].sourceSlideIndex == want1Based {
			return a
		}
	}

	kind := themes.InferLayoutKind(src)
	nameLower := strings.ToLower(src.Name)

	// Phase 1: bookend slides get matched by kind name first.
	isBookend := kind == "title" || kind == "section" || kind == "closing" || kind == "agenda"
	if isBookend {
		for _, a := range archetypes {
			if strings.ToLower(a.Kind) == kind {
				return a
			}
		}
		// Fallback: partial kind name match in archetype title.
		for _, a := range archetypes {
			if strings.Contains(strings.ToLower(a.Title), kind) {
				return a
			}
		}
	}

	// Phase 2: structural fingerprint matching for content slides.
	srcFP := extractSourceFingerprint(0, src)
	srcPlaceholderCount := len(src.Placeholders)

	// Count text chrome objects — these become fill-slot demands (projectText
	// maps them to ph-N numbered slots). An archetype with more slots is better
	// when the source has many text items.
	srcTextChromeCount := 0
	for _, o := range src.Objects {
		if o.Kind == "text" && len(o.Text) > 3 {
			srcTextChromeCount++
		}
	}

	bestScore := -1.0
	best := archetypes[0]

	for i, a := range archetypes {
		score := scoreArchetypeByFingerprint(srcFP, srcPlaceholderCount, srcTextChromeCount, archFPs[i], a, kind, nameLower)
		if score > bestScore {
			bestScore = score
			best = a
		}
	}
	return best
}

// scoreArchetypeByFingerprint scores an archetype against a source slide's
// structural fingerprint. Higher is better; the maximum possible score is ~12.5.
//
// Key insight: archetypes with <2 fill slots are "chrome-only" slides (blank,
// section dividers, single-title). For content slides with many shapes, such
// archetypes should score very low — they can never render the card content.
func scoreArchetypeByFingerprint(srcFP SlideFingerprint, srcPHCount, srcTextChromeCount int, archFP archetypeFingerprint, a themes.Archetype, kind, slideName string) float64 {
	score := 0.0

	// Effective slot demand: the larger of formal placeholders and text chrome
	// objects. Text chrome objects are mapped to ph-N slots by projectText, so
	// an archetype with more slots absorbs more source content.
	effectiveSlotDemand := srcPHCount
	if srcTextChromeCount > effectiveSlotDemand {
		effectiveSlotDemand = srcTextChromeCount
	}

	// --- hard penalty for useless archetypes on content slides ---
	// Archetypes with <2 fill slots cannot render card content. Penalise heavily
	// when the source slide has substantial chrome (it's a content slide).
	if archFP.slotCount < 2 && srcFP.ChromeShapeCount > 3 {
		score -= 5.0
	}

	// --- hard penalty for bookend archetypes selected for content slides ---
	// Phase-2 scoring runs only for content slides (bookends are handled in Phase 1).
	// Bookend archetypes (title/section/closing/agenda) must not win in Phase 2.
	archKindBase := a.Kind
	if i := strings.LastIndexByte(a.Kind, '-'); i > 0 {
		if _, err := strconv.Atoi(a.Kind[i+1:]); err == nil {
			archKindBase = a.Kind[:i]
		}
	}
	if archKindBase == "title" || archKindBase == "section" || archKindBase == "closing" || archKindBase == "agenda" {
		score -= 10.0
	}

	// --- roundRect count similarity (weight 4.0, highest weight) ---
	// Use the exact RoundRectCount from the fingerprint (not fill-color count).
	// A 3-card archetype must not win over a 6-card one.
	srcRRCount := srcFP.RoundRectCount
	archRRCount := archFP.roundRectCount
	if srcRRCount > 0 || archRRCount > 0 {
		maxRR := srcRRCount
		if archRRCount > maxRR {
			maxRR = archRRCount
		}
		if maxRR > 0 {
			diff := srcRRCount - archRRCount
			if diff < 0 {
				diff = -diff
			}
			score += 4.0 * (1.0 - float64(diff)/float64(maxRR))
		}
	} else {
		// Neither source nor archetype has roundRects — they agree.
		score += 4.0
	}

	// --- ellipse match (weight 1.5) ---
	if srcFP.HasEllipses == archFP.HasEllipses {
		score += 1.5
	}

	// --- stripe match (weight 1.5) ---
	if srcFP.HasStripes == archFP.HasStripes {
		score += 1.5
	}

	// --- total chrome shape count similarity (weight 1.0) ---
	// Archetypes include text-slot shapes (ast-text boxes) as part of their shape
	// count, so they naturally have MORE shapes than the source IR chrome count.
	// Only penalise an archetype that has FEWER shapes than the source (it's
	// missing decorative chrome). Extra shapes are fine — they're fill slots.
	if srcFP.ChromeShapeCount > 0 && archFP.ChromeShapeCount > 0 {
		ratio := float64(archFP.ChromeShapeCount) / float64(srcFP.ChromeShapeCount)
		if ratio < 1.0 {
			// Archetype has fewer shapes: penalise proportionally
			score += 1.0 * ratio
		} else {
			// Archetype has same or more shapes: full score (no penalty)
			score += 1.0
		}
	}

	// --- fill slot count similarity (weight 3.0, raised from 2.0) ---
	// The most important factor: an archetype with too few slots cannot render
	// the source slide's content at all. Uses effective slot demand (max of
	// formal placeholders and text chrome count) so archetypes with more slots
	// win when the source has many text objects.
	// Excess archetype slots are only mildly penalised — empty slots are fine.
	if effectiveSlotDemand > 0 || archFP.slotCount > 0 {
		maxSlots := effectiveSlotDemand
		if archFP.slotCount > maxSlots {
			maxSlots = archFP.slotCount
		}
		if maxSlots > 0 {
			diff := effectiveSlotDemand - archFP.slotCount
			if diff < 0 {
				// Archetype has MORE slots than needed: mild penalty (0.3×)
				diff = -diff * 3 / 10
			}
			score += 3.0 * (1.0 - float64(diff)/float64(maxSlots))
		}
	}

	// --- accent color overlap (weight 2.0) ---
	// Archetypes built from slides with matching accent colors (brand status
	// colors, fill scheme) score higher. This helps pick the right archetype
	// variant when multiple archetypes have identical roundRect counts but
	// different color palettes (e.g. red/purple/blue vs orange/green).
	if len(srcFP.AccentColors) > 0 && len(archFP.AccentColors) > 0 {
		overlapCount := 0
		for _, c := range srcFP.AccentColors {
			for _, ac := range archFP.AccentColors {
				if strings.EqualFold(c, ac) {
					overlapCount++
					break
				}
			}
		}
		// Overlap fraction: 0 = no match, 1 = perfect match.
		maxColors := len(srcFP.AccentColors)
		if len(archFP.AccentColors) > maxColors {
			maxColors = len(archFP.AccentColors)
		}
		if maxColors > 0 {
			score += 2.0 * float64(overlapCount) / float64(maxColors)
		}
	}

	// --- kind name tiebreaker (weight 0.5) ---
	archKindLower := strings.ToLower(a.Kind)
	archTitleLower := strings.ToLower(a.Title)
	if archKindLower == kind {
		score += 0.5
	} else if strings.Contains(archTitleLower, kind) {
		score += 0.3
	} else if strings.Contains(slideName, archKindLower) {
		score += 0.2
	}

	return score
}

// projectText substitutes source text into matching fill slots in the markup
// string. It handles both typed placeholder slots (title/body) and numbered
// card slots (ph-2, ph-3, …) populated from text chrome objects.
//
// Strategy:
//  1. From placeholders: map title-type → ph-title-like slot, body-type → ph-2.
//  2. From text chrome objects (IRChrome.Kind == "text"): parse the archetype
//     markup to find each slot's (x, y, w, h) position, then match each source
//     text object to the nearest slot by centroid distance. This positional
//     matching ensures "One Alerting Platform" at source (104,447) goes to the
//     archetype slot whose box is centred near (104,447), not to a header strip
//     at y=40 that happens to be first in the slot list.
//     Slots at canvas edges (x > canvasW-60 or y > canvasH-60) are never filled
//     — they are footer/date/slide-number chrome, not content regions.
func projectText(markup string, fillSlots []string, placeholders []themes.IRPlaceholder, objects []themes.IRChrome) (string, []string) {
	var warnings []string

	// Track which slots have been filled.
	filled := map[string]bool{}

	// --- Step 1: placeholders (title + body families) ---
	// Only inject real content (not template tokens like {{BODY}} / {{TITLE}},
	// not footer/slide-number placeholders at canvas edges).
	for _, ph := range placeholders {
		if ph.Type == "" {
			continue
		}
		// Skip footer/slide-number placeholders (typically at y > 900 or x > 1800).
		if ph.Y > 900 || ph.X > 1800 {
			continue
		}
		slotID := matchSlot(fillSlots, ph.Type)
		if slotID == "" {
			if ph.Prompt != "" || len(strings.TrimSpace(ph.Name)) > 0 {
				warnings = append(warnings, fmt.Sprintf("no fill slot for placeholder %q (type %q)", ph.Name, ph.Type))
			}
			continue
		}
		text := ph.Prompt
		if text == "" {
			text = ph.Name
		}

		// Always inject the placeholder's explicit text color so the reconstruction
		// slide reflects the source layout's actual typography (e.g. #6A7D90 body
		// text). Do this regardless of whether we have real text content — the color
		// is a template design attribute, not per-content data.
		if ph.Style.Color != "" && ph.Style.Color != "#000000" && ph.Style.Color != "#FFFFFF" {
			markup = injectSlotColor(markup, slotID, ph.Style.Color)
		}

		// Skip template tokens ({{BODY}}, {{TITLE}}, etc.) — these are prompts
		// for the AI, not real source content to inject.
		if strings.HasPrefix(text, "{{") && strings.HasSuffix(text, "}}") {
			continue
		}
		if text != "" {
			markup = injectText(markup, slotID, text)
			filled[slotID] = true
		}
	}

	// --- Step 2: text chrome objects → numbered card slots (positional matching) ---
	// Collect non-empty text objects from chrome.
	type textObj struct {
		text string
		x, y int
		w, h int
	}
	var texts []textObj
	for _, obj := range objects {
		if obj.Kind != "text" {
			continue
		}
		t := strings.TrimSpace(obj.Text)
		if t == "" {
			continue
		}
		// Skip footer/edge text objects — these are chrome (date, copyright, slide
		// number) that should not be mapped to content slots.
		if obj.Y > 960 || obj.X > 1800 {
			continue
		}
		texts = append(texts, textObj{text: t, x: obj.X, y: obj.Y, w: obj.W, h: obj.H})
	}

	// Build open slots list: unfilled numbered slots that are NOT at canvas edges.
	// Parse each slot's position from the markup so we can do positional matching.
	//
	// Supplemental scan: the archetype markup may contain ph-N ast-text elements
	// that are not in the declared FillSlots list (e.g. when the import_worker
	// generated more text regions than it declared as fill slots). These are still
	// valid fill targets — scan the markup for all id="ph-N" patterns and add any
	// undeclared ones to the candidate pool so overflow text is not silently dropped.
	type slotBox struct {
		id   string
		x, y int
		w, h int
	}
	// Collect all slot IDs: declared FillSlots first, then undeclared ph-N from markup.
	slotIDSet := make(map[string]bool, len(fillSlots))
	allSlotIDs := make([]string, 0, len(fillSlots)+4)
	for _, id := range fillSlots {
		slotIDSet[id] = true
		allSlotIDs = append(allSlotIDs, id)
	}
	// Scan markup for undeclared ph-N elements (e.g. ph-10, ph-11 beyond FillSlots).
	for i := 1; i <= 50; i++ {
		id := fmt.Sprintf("ph-%d", i)
		if !slotIDSet[id] && strings.Contains(markup, `id="`+id+`"`) {
			allSlotIDs = append(allSlotIDs, id)
			slotIDSet[id] = true
		}
	}

	var openSlots []slotBox
	for _, id := range allSlotIDs {
		if filled[id] || !isNumberedSlot(id) {
			continue
		}
		x, y, w, h := parseSlotGeometry(markup, id)
		// Skip footer/edge slots — slide-number, date, copyright chrome at canvas edges.
		// Canvas is 1920×1080; slots with x > 1860 or y > 1020 are edge chrome.
		if x > 1860 || y > 1020 {
			continue
		}
		openSlots = append(openSlots, slotBox{id: id, x: x, y: y, w: w, h: h})
	}

	if len(openSlots) == 0 || len(texts) == 0 {
		return markup, warnings
	}

	// Positional matching: for each source text object, find the nearest
	// archetype slot by centroid distance. Each slot can only receive one text.
	// Distance is the Euclidean distance between source text centroid and slot centroid.
	// We use a greedy nearest-neighbour assignment: sort source texts by Y then X
	// (reading order), then for each assign to the nearest available slot.
	//
	// Y-band preference: a slot within the same horizontal band (|srcY - slotY| < 200)
	// is preferred over any cross-band match. This prevents a text at y=657 from
	// stealing a slot at y=847 when a better in-band slot exists.
	//
	// Sort texts by Y then X for reading order.
	for i := 1; i < len(texts); i++ {
		for j := i; j > 0; j-- {
			a, b := texts[j-1], texts[j]
			if a.y > b.y || (a.y == b.y && a.x > b.x) {
				texts[j-1], texts[j] = texts[j], texts[j-1]
			}
		}
	}

	// Build a usage map to prevent a slot from being used twice.
	usedSlots := map[string]bool{}

	// yBandThreshold is the maximum vertical distance (in pixels) between a
	// source text and a slot centroid for the match to be considered "in-band".
	// In-band matches are always preferred over cross-band matches, regardless
	// of Euclidean distance.
	const yBandThreshold = 200.0

	for _, txt := range texts {
		// Source text centroid.
		srcCX := float64(txt.x) + float64(txt.w)/2.0
		srcCY := float64(txt.y) + float64(txt.h)/2.0

		// Two-phase nearest-neighbour:
		// Phase A: find nearest in-band slot (|srcCY - slotCY| < yBandThreshold).
		// Phase B: if no in-band slot, find nearest cross-band slot.
		bestDist := -1.0
		bestIdx := -1
		hasInBand := false

		for i, s := range openSlots {
			if usedSlots[s.id] {
				continue
			}
			// Slot centroid.
			sCX := float64(s.x) + float64(s.w)/2.0
			sCY := float64(s.y) + float64(s.h)/2.0
			dy := srcCY - sCY
			if dy < 0 {
				dy = -dy
			}
			inBand := dy < yBandThreshold
			dx := srcCX - sCX
			dist := dx*dx + (srcCY-sCY)*(srcCY-sCY) // squared distance

			// Prefer in-band over cross-band: if we already have an in-band
			// candidate, skip cross-band slots entirely.
			if hasInBand && !inBand {
				continue
			}
			if inBand && !hasInBand {
				// First in-band candidate — clear any previous cross-band best.
				bestDist = dist
				bestIdx = i
				hasInBand = true
				continue
			}
			if bestDist < 0 || dist < bestDist {
				bestDist = dist
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break // no more slots
		}
		slot := openSlots[bestIdx]
		// Only fill if the source text is reasonably close to the slot
		// (within ~2× the slot diagonal). This prevents wildly off-canvas
		// texts from filling slots they have no relationship to.
		slotDiag := float64(slot.w)*float64(slot.w) + float64(slot.h)*float64(slot.h)
		maxDistSq := 4.0 * slotDiag
		if slotDiag > 0 && bestDist > maxDistSq {
			continue // too far — skip this text object
		}
		markup = injectText(markup, slot.id, txt.text)
		filled[slot.id] = true
		usedSlots[slot.id] = true
	}

	return markup, warnings
}

// parseSlotGeometry extracts the x, y, w, h attributes of the ast-text element
// with the given slot id from the markup string. Returns zeros if not found.
func parseSlotGeometry(markup, slotID string) (x, y, w, h int) {
	marker := `id="` + slotID + `"`
	idx := strings.Index(markup, marker)
	if idx < 0 {
		return
	}
	// Find the start of this tag.
	start := strings.LastIndex(markup[:idx], "<")
	if start < 0 {
		return
	}
	end := strings.Index(markup[start:], ">")
	if end < 0 {
		return
	}
	tag := markup[start : start+end+1]
	x = attrInt(tag, "x")
	y = attrInt(tag, "y")
	w = attrInt(tag, "w")
	h = attrInt(tag, "h")
	return
}

// attrInt extracts an integer attribute value from a tag string.
// Returns 0 if the attribute is not found or not parseable.
func attrInt(tag, attr string) int {
	key := attr + `="`
	idx := strings.Index(tag, key)
	if idx < 0 {
		return 0
	}
	valStart := idx + len(key)
	valEnd := strings.Index(tag[valStart:], `"`)
	if valEnd < 0 {
		return 0
	}
	v, err := strconv.Atoi(tag[valStart : valStart+valEnd])
	if err != nil {
		return 0
	}
	return v
}

// isNumberedSlot returns true when the slot id looks like a numbered content
// slot (ph-1, ph-2, ph-title, ph-card-3, ph-body, etc.) — i.e. anything that
// is not specifically a picture slot.
func isNumberedSlot(id string) bool {
	idLower := strings.ToLower(id)
	// Exclude picture/image slots — those are filled by projectImages.
	if strings.Contains(idLower, "pic") || strings.Contains(idLower, "img") || strings.Contains(idLower, "image") {
		return false
	}
	return true
}

// matchSlot returns the fill slot id that best matches the given placeholder type.
func matchSlot(fillSlots []string, phType string) string {
	phType = strings.ToLower(phType)
	// Title family: title, ctrTitle.
	isTitle := phType == "title" || phType == "ctrtitle"
	// Body family: body, subTitle.
	isBody := phType == "body" || phType == "subtitle"

	for _, id := range fillSlots {
		idLower := strings.ToLower(id)
		if isTitle && strings.Contains(idLower, "title") {
			return id
		}
		if isBody && (strings.Contains(idLower, "body") || idLower == "ph-2") {
			return id
		}
	}
	return ""
}

// injectSlotColor updates the color attribute of an ast-text element with the
// given slot id. This ensures the reconstruction slide reflects the source
// placeholder's explicit text color.
func injectSlotColor(markup, slotID, color string) string {
	if color == "" {
		return markup
	}
	// Normalise to lowercase #rrggbb (the color may come as #RRGGBB from the IR).
	c := strings.TrimPrefix(color, "#")
	if len(c) == 6 {
		color = "#" + strings.ToLower(c)
	}
	marker := `id="` + slotID + `"`
	idx := strings.Index(markup, marker)
	if idx < 0 {
		return markup
	}
	// Find the start of this element (the opening "<").
	elemStart := strings.LastIndex(markup[:idx], "<")
	if elemStart < 0 {
		return markup
	}
	// Find the end of the opening tag.
	tagEnd := strings.Index(markup[elemStart:], ">")
	if tagEnd < 0 {
		return markup
	}
	tagEnd += elemStart
	openTag := markup[elemStart : tagEnd+1]

	// Replace the color="..." attribute if present, else add it.
	colorAttr := `color="`
	ci := strings.Index(openTag, colorAttr)
	if ci >= 0 {
		// Replace existing color value.
		valStart := ci + len(colorAttr)
		valEnd := strings.Index(openTag[valStart:], `"`)
		if valEnd < 0 {
			return markup
		}
		valEnd += valStart
		newTag := openTag[:valStart] + color + openTag[valEnd:]
		return markup[:elemStart] + newTag + markup[tagEnd+1:]
	}
	// No color attribute: insert it before the closing ">".
	closeGT := strings.Index(openTag, ">")
	if closeGT < 0 {
		return markup
	}
	var insertPos int
	if openTag[closeGT-1] == '/' {
		insertPos = elemStart + closeGT - 1
	} else {
		insertPos = elemStart + closeGT
	}
	newMarkup := markup[:insertPos] + ` color="` + color + `"` + markup[insertPos:]
	return newMarkup
}

// injectText replaces the text content between `id="<slotID>"` ast-text tags.
// Text is wrapped in <ast-run> so the slides runtime renders it correctly.
// Multi-line text (newlines) is split into multiple <ast-run> elements separated
// by line breaks, which the runtime renders correctly with white-space:pre-wrap.
func injectText(markup, slotID, text string) string {
	// Find the opening ast-text tag containing id="<slotID>".
	marker := `id="` + slotID + `"`
	idx := strings.Index(markup, marker)
	if idx < 0 {
		return markup
	}
	// Find the closing ">" of the opening tag.
	tagEnd := strings.Index(markup[idx:], ">")
	if tagEnd < 0 {
		return markup
	}
	tagEnd += idx + 1 // absolute position just after ">"

	// Find the closing </ast-text>.
	closeTag := "</ast-text>"
	closeIdx := strings.Index(markup[tagEnd:], closeTag)
	if closeIdx < 0 {
		return markup
	}
	closeIdx += tagEnd

	// Wrap text in <ast-run> so the slides runtime displays it.
	// Escape HTML special chars so & < > don't break the markup.
	// Multi-line text: each line gets its own <ast-run> separated by \n
	// which pre-wrap renders correctly. Alternatively use one run with \n.
	escaped := htmlEscape(text)
	content := `<ast-run>` + escaped + `</ast-run>`

	return markup[:tagEnd] + content + markup[closeIdx:]
}

// htmlEscape escapes the minimal HTML special characters for safe inline embedding.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// projectImages substitutes asset refs from image chrome objects into the
// nearest ph-pic-N fill slot in the markup.
//
// If the image's media key is already referenced as an asset-ref attribute in the
// archetype markup (i.e. it is already embedded as a fixed decorative image), the
// object is silently skipped — no warning, no duplicate injection. This prevents
// false-positive "no ph-pic-N slot" warnings for the common case where the
// import_worker baked the image directly into the archetype markup.
func projectImages(markup string, objects []themes.IRChrome, assets map[string]string) (string, []string) {
	var warnings []string
	for _, obj := range objects {
		if obj.Kind != "image" || obj.MediaKey == "" {
			continue
		}
		assetRef, ok := assets[obj.MediaKey]
		if !ok {
			assetRef = obj.MediaKey // use key as-is if not in map
		}
		// If the archetype markup already contains this asset-ref (the image is
		// already a fixed decorative element in the slide chrome), skip silently.
		if strings.Contains(markup, `asset-ref="`+obj.MediaKey+`"`) ||
			strings.Contains(markup, `asset-ref="`+assetRef+`"`) {
			continue
		}
		// Find the nearest ph-pic-N fill slot.
		idx := strings.Index(markup, `id="ph-pic-`)
		if idx < 0 {
			warnings = append(warnings, fmt.Sprintf("no ph-pic-N slot for image chrome %q", obj.MediaKey))
			continue
		}
		// Replace the src attribute value if present, else inject a slot substitution.
		markup = injectImageSlot(markup, idx, assetRef)
	}
	return markup, warnings
}

// injectImageSlot replaces (or appends) the src attribute on the ast-image element at position idx.
func injectImageSlot(markup string, idx int, assetRef string) string {
	// Find the end of the opening tag from idx.
	tagEnd := strings.Index(markup[idx:], ">")
	if tagEnd < 0 {
		return markup
	}
	tagEnd += idx
	openingTag := markup[idx:tagEnd]

	// If src attribute exists, replace it.
	if srcIdx := strings.Index(openingTag, ` src="`); srcIdx >= 0 {
		start := idx + srcIdx + len(` src="`)
		end := strings.Index(markup[start:], `"`)
		if end >= 0 {
			return markup[:start] + assetRef + markup[start+end:]
		}
	}
	// Otherwise insert src before the closing ">".
	return markup[:tagEnd] + ` src="` + assetRef + `"` + markup[tagEnd:]
}

// firstTitleText returns the first title-typed placeholder's prompt or name from
// a source layout (used to set the SceneGraph title and individual slide titles).
func firstTitleText(layout themes.IRLayout) string {
	for _, ph := range layout.Placeholders {
		pt := strings.ToLower(ph.Type)
		if pt == "title" || pt == "ctrtitle" {
			if ph.Prompt != "" {
				return ph.Prompt
			}
			if ph.Name != "" {
				return ph.Name
			}
		}
	}
	return ""
}
