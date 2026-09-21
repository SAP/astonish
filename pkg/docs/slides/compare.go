package slides

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// AcceptanceThreshold is the minimum FidelityScore for a template import to
// be considered validated. 0.75 = 75% of structural, style, and content checks pass.
const AcceptanceThreshold = 0.75

// CompareReport is the output of Compare: a structured fidelity report with
// per-slide findings and a weighted aggregate score.
type CompareReport struct {
	FidelityScore  float64        `json:"fidelityScore"`  // 0.0–1.0
	Passed         bool           `json:"passed"`         // FidelityScore >= AcceptanceThreshold
	SlideFindings  []SlideFinding `json:"slideFindings"`
	StructureScore float64        `json:"structureScore"` // 0.0–1.0 sub-score
	StyleScore     float64        `json:"styleScore"`     // 0.0–1.0 sub-score
	ContentScore   float64        `json:"contentScore"`   // 0.0–1.0 sub-score

	// SlideFingerprints is populated during Compare and holds the source IR
	// visual fingerprint for each slide. It is used by llmRepairPass to ground
	// the repair prompt with concrete per-slide visual structure.
	SlideFingerprints []SlideFingerprint `json:"slideFingerprints,omitempty"`
}

// SlideFinding holds gap items for one source slide (0-based index).
type SlideFinding struct {
	SlideIndex int       `json:"slideIndex"` // 0-based
	Gaps       []GapItem `json:"gaps,omitempty"`
}

// GapItem describes a single detected gap between source and reconstruction.
type GapItem struct {
	Kind          GapKind  `json:"kind"`
	Severity      Severity `json:"severity"`
	Description   string   `json:"description"`
	ArchetypeKind string   `json:"archetypeKind,omitempty"`
	Expected      string   `json:"expected,omitempty"`
	Actual        string   `json:"actual,omitempty"`
}

// GapKind is an enumeration of gap categories.
type GapKind string

const (
	GapSlideCountMismatch  GapKind = "slide_count_mismatch"
	GapMissingContent      GapKind = "missing_content"
	GapWrongBackground     GapKind = "wrong_background"
	GapWrongTextColor      GapKind = "wrong_text_color"
	GapFontMismatch        GapKind = "font_mismatch"
	GapMissingAsset        GapKind = "missing_asset"
	GapNoMatchingArchetype GapKind = "no_matching_archetype"
	// Structural visual gaps — require deep IR fingerprint comparison.
	GapMissingShapeFamily GapKind = "missing_shape_family" // expected roundRect/stripe/ellipse not in reconstruction
	GapMissingFillColor   GapKind = "missing_fill_color"   // an accent fill color from IR not found in ASD output
	GapWrongShapeCount    GapKind = "wrong_shape_count"     // significant shape count deviation per geom family
)

// Severity classifies the impact of a gap finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityMajor    Severity = "major"
	SeverityMinor    Severity = "minor"
)

// -------------------------------------------------------------------------
// Visual fingerprint — the per-slide structural summary from source IR
// -------------------------------------------------------------------------

// ShapeFamily is a single geom family extracted from the source IR or the
// reconstructed ASD nodes. It captures the structural "DNA" of the slide.
type ShapeFamily struct {
	Geom     string   `json:"geom"`            // rect | roundRect | ellipse | line | image | path
	Count    int      `json:"count"`           // how many unique fill colors (legacy — approximate)
	RawCount int      `json:"rawCount"`        // exact object count for this geom family
	Fills    []string `json:"fills,omitempty"` // fill colors (deduped, up to 6)
}

// SlideFingerprint is the extracted visual fingerprint for one slide.
// It summarises the source IR's chrome objects so the LLM repair prompt and
// deep comparison can reference concrete visual structure.
type SlideFingerprint struct {
	SlideIndex       int           `json:"slideIndex"`
	LayoutName       string        `json:"layoutName,omitempty"`
	Background       string        `json:"background,omitempty"`
	ShapeFamilies    []ShapeFamily `json:"shapeFamilies,omitempty"`
	AccentColors     []string      `json:"accentColors,omitempty"` // all non-white/non-black fills
	HasRoundRects    bool          `json:"hasRoundRects"`           // any roundRect shapes
	HasStripes       bool          `json:"hasStripes"`              // narrow tall/wide stripes (left-bar accent)
	HasTimeline      bool          `json:"hasTimeline"`             // horizontal line + small dots
	HasEllipses      bool          `json:"hasEllipses"`             // circle markers
	ChromeShapeCount int           `json:"chromeShapeCount"`        // total non-text, non-image chrome shapes
	RoundRectCount   int           `json:"roundRectCount"`          // exact count of roundRect objects
}

// extractSourceFingerprint builds a SlideFingerprint from an IRLayout's chrome objects.
func extractSourceFingerprint(idx int, layout themes.IRLayout) SlideFingerprint {
	fp := SlideFingerprint{
		SlideIndex: idx,
		LayoutName: layout.Name,
		Background: layout.Background.Color,
	}

	geomCounts := map[string][]string{} // geom → fill colors
	geomRawCounts := map[string]int{}   // geom → exact object count
	accentSet := map[string]bool{}

	for _, obj := range layout.Objects {
		geom := normalizeGeom(obj)
		fill := ""
		// For accent-color extraction: only use fill colors from structural
		// chrome shapes (rect, roundRect, ellipse, path). Line connector objects
		// and text objects carry per-slide data colors that differ across slides
		// and must NOT be treated as template brand accent colors.
		isStructuralChrome := obj.Kind != "line" && obj.Kind != "text"
		if isStructuralChrome {
			if obj.Fill != nil && obj.Fill.Kind == "solid" && obj.Fill.Color != "" {
				fill = strings.ToLower(obj.Fill.Color)
			} else if obj.Fill != nil && obj.Fill.Kind == "gradient" && obj.Fill.Gradient != nil && len(obj.Fill.Gradient.Stops) > 0 {
				fill = strings.ToLower(obj.Fill.Gradient.Stops[0].Color)
			}
			// Also treat stroke color as an accent candidate for shapes with
			// colored borders (e.g. bordered cards, framed images).
			if fill == "" && obj.Line != nil && obj.Line.Color != "" {
				fill = strings.ToLower(obj.Line.Color)
			}
		} else {
			// For geomCounts tracking (structural comparison), still use fill/line
			// even for line and text objects — just don't count them as accent colors.
			if obj.Fill != nil && obj.Fill.Kind == "solid" && obj.Fill.Color != "" {
				fill = strings.ToLower(obj.Fill.Color)
			} else if obj.Fill != nil && obj.Fill.Kind == "gradient" && obj.Fill.Gradient != nil && len(obj.Fill.Gradient.Stops) > 0 {
				fill = strings.ToLower(obj.Fill.Gradient.Stops[0].Color)
			}
		}
		if fill != "" {
			geomCounts[geom] = appendUnique(geomCounts[geom], fill)
			if isStructuralChrome && isAccentColor(fill) {
				accentSet[fill] = true
			}
		} else {
			if _, ok := geomCounts[geom]; !ok {
				geomCounts[geom] = nil
			}
		}
		geomRawCounts[geom]++

		// Detect structural patterns.
		switch {
		case geom == "roundRect":
			fp.HasRoundRects = true
			fp.RoundRectCount++
		case geom == "ellipse":
			fp.HasEllipses = true
		case isStripeShape(obj):
			fp.HasStripes = true
		case isTimelineMarker(obj, layout.Objects):
			fp.HasTimeline = true
		}

		if obj.Kind != "text" && obj.Kind != "image" {
			fp.ChromeShapeCount++
		}
	}

	// Build shape families sorted by raw count descending.
	for geom, fills := range geomCounts {
		fp.ShapeFamilies = append(fp.ShapeFamilies, ShapeFamily{
			Geom:     geom,
			Count:    len(fills),
			RawCount: geomRawCounts[geom],
			Fills:    fills,
		})
	}
	sort.Slice(fp.ShapeFamilies, func(i, j int) bool {
		return fp.ShapeFamilies[i].Count > fp.ShapeFamilies[j].Count
	})

	for c := range accentSet {
		fp.AccentColors = append(fp.AccentColors, c)
	}
	sort.Strings(fp.AccentColors)

	return fp
}

// extractRecoFingerprint builds a SlideFingerprint from a reconstructed Slide's nodes.
func extractRecoFingerprint(idx int, slide Slide) SlideFingerprint {
	fp := SlideFingerprint{SlideIndex: idx}
	geomCounts := map[string][]string{}
	geomRawCounts := map[string]int{}
	accentSet := map[string]bool{}

	for _, n := range slide.Nodes {
		if n.Type != "shape" {
			continue
		}
		geom := n.Geom
		if geom == "" {
			geom = "rect"
		}
		fill := strings.ToLower(n.Fill)
		// Fall back to line/stroke color for shapes with colored borders
		// (e.g. bordered cards). Pure connector lines (geom="line") carry
		// data-visualization colors that differ per slide — exclude them.
		if fill == "" && n.Line != "" && geom != "line" {
			fill = strings.ToLower(n.Line)
		}

		// Skip pure background rects (full-canvas decorative bg).
		dec, _ := n.Props["decorative"].(string)
		if dec == "true" && n.Geometry.X == 0 && n.Geometry.Y == 0 &&
			n.Geometry.W >= CanvasWidth-20 && n.Geometry.H >= CanvasHeight-20 {
			fp.Background = fill
			continue
		}

		if fill != "" {
			geomCounts[geom] = appendUnique(geomCounts[geom], fill)
			// Collect accent colors from structural shapes (non-line geom).
			if geom != "line" && isAccentColor(fill) {
				accentSet[fill] = true
			}
		} else {
			if _, ok := geomCounts[geom]; !ok {
				geomCounts[geom] = nil
			}
		}
		geomRawCounts[geom]++

		switch geom {
		case "roundRect":
			fp.HasRoundRects = true
			fp.RoundRectCount++
		case "ellipse":
			fp.HasEllipses = true
		}

		if isNodeStripe(n) {
			fp.HasStripes = true
		}

		fp.ChromeShapeCount++
	}

	for geom, fills := range geomCounts {
		fp.ShapeFamilies = append(fp.ShapeFamilies, ShapeFamily{
			Geom:     geom,
			Count:    len(fills),
			RawCount: geomRawCounts[geom],
			Fills:    fills,
		})
	}
	sort.Slice(fp.ShapeFamilies, func(i, j int) bool {
		return fp.ShapeFamilies[i].Count > fp.ShapeFamilies[j].Count
	})

	for c := range accentSet {
		fp.AccentColors = append(fp.AccentColors, c)
	}
	sort.Strings(fp.AccentColors)

	return fp
}

// -------------------------------------------------------------------------
// Compare
// -------------------------------------------------------------------------

// Compare compares an imported TemplateModel IR (source) against a
// reconstructed SceneGraph (output of ReconstructScene) and produces a
// structured CompareReport.
//
// FidelityScore = 0.50*structureScore + 0.30*styleScore + 0.20*contentScore
// Passed        = FidelityScore >= AcceptanceThreshold
func Compare(source *themes.TemplateModel, reconstruction *SceneGraph) CompareReport {
	if source == nil {
		source = &themes.TemplateModel{}
	}
	if reconstruction == nil {
		reconstruction = &SceneGraph{}
	}

	sourceCount := len(source.Slides)
	recoCount := len(reconstruction.Slides)
	pairCount := sourceCount
	if recoCount < pairCount {
		pairCount = recoCount
	}

	var allFindings []SlideFinding
	for i := 0; i < sourceCount; i++ {
		allFindings = append(allFindings, SlideFinding{SlideIndex: i})
	}

	// Build per-slide fingerprints for source IR.
	var fingerprints []SlideFingerprint
	for i, srcSlide := range source.Slides {
		fingerprints = append(fingerprints, extractSourceFingerprint(i, srcSlide))
	}

	// -------------------------------------------------------------------------
	// Structural sub-score (weight 0.50)
	// -------------------------------------------------------------------------
	structureScore := computeStructureScore(source, reconstruction, allFindings, fingerprints)

	// -------------------------------------------------------------------------
	// Style sub-score (weight 0.30)
	// -------------------------------------------------------------------------
	styleScore := computeStyleScore(source, reconstruction, allFindings, sourceCount, fingerprints)

	// -------------------------------------------------------------------------
	// Content sub-score (weight 0.20)
	// -------------------------------------------------------------------------
	contentScore := computeContentScore(source, reconstruction, allFindings)

	// Prune empty findings.
	var slideFindings []SlideFinding
	for _, sf := range allFindings {
		if len(sf.Gaps) > 0 {
			slideFindings = append(slideFindings, sf)
		}
	}

	// Slide-count mismatch is a cross-cutting gap.
	if sourceCount != recoCount {
		gap := GapItem{
			Kind:        GapSlideCountMismatch,
			Severity:    SeverityCritical,
			Description: fmt.Sprintf("source has %d slides but reconstruction has %d", sourceCount, recoCount),
			Expected:    strconv.Itoa(sourceCount),
			Actual:      strconv.Itoa(recoCount),
		}
		if len(slideFindings) > 0 && slideFindings[0].SlideIndex == 0 {
			slideFindings[0].Gaps = append([]GapItem{gap}, slideFindings[0].Gaps...)
		} else {
			slideFindings = append([]SlideFinding{{SlideIndex: 0, Gaps: []GapItem{gap}}}, slideFindings...)
		}
	}

	_ = pairCount

	fidelity := 0.50*structureScore + 0.30*styleScore + 0.20*contentScore

	return CompareReport{
		FidelityScore:     fidelity,
		Passed:            fidelity >= AcceptanceThreshold,
		SlideFindings:     slideFindings,
		StructureScore:    structureScore,
		StyleScore:        styleScore,
		ContentScore:      contentScore,
		SlideFingerprints: fingerprints,
	}
}

// AcceptanceBar returns true when the report's FidelityScore meets or exceeds
// AcceptanceThreshold.
func AcceptanceBar(report CompareReport) bool {
	return report.FidelityScore >= AcceptanceThreshold
}

// -------------------------------------------------------------------------
// Internal sub-score helpers
// -------------------------------------------------------------------------

func computeStructureScore(source *themes.TemplateModel, reco *SceneGraph, findings []SlideFinding, fingerprints []SlideFingerprint) float64 {
	sourceCount := len(source.Slides)
	recoCount := len(reco.Slides)

	score := 1.0

	// Count mismatch penalty.
	if sourceCount != recoCount {
		maxCount := sourceCount
		if recoCount > maxCount {
			maxCount = recoCount
		}
		if maxCount > 0 {
			diff := sourceCount - recoCount
			if diff < 0 {
				diff = -diff
			}
			score = 1.0 - float64(diff)/float64(maxCount)
		} else {
			score = 0.0
		}
	}

	pairCount := sourceCount
	if recoCount < pairCount {
		pairCount = recoCount
	}

	penalties := 0.0
	checks := 0

	for i := 0; i < pairCount; i++ {
		recoSlide := reco.Slides[i]

		// Empty-slide penalty: -0.15 per empty reconstruction slide.
		if slideIsEmpty(recoSlide) {
			penalties += 0.15
			checks++
			if i < len(findings) {
				findings[i].Gaps = append(findings[i].Gaps, GapItem{
					Kind:        GapNoMatchingArchetype,
					Severity:    SeverityMajor,
					Description: "reconstruction slide has no nodes (empty markup)",
				})
			}
			continue
		}

		// Deep structural check: compare source IR fingerprint vs reconstruction.
		if i < len(fingerprints) {
			srcFP := fingerprints[i]
			recoFP := extractRecoFingerprint(i, recoSlide)
			// Bookend slides (title/closing/section/agenda) are matched to minimal-chrome
			// archetypes that don't reproduce the original's decorative chrome. Skip
			// shape-family checks for these slides to avoid false critical gaps.
			isBookend := false
			if i < len(source.Slides) {
				k := themes.InferLayoutKind(source.Slides[i])
				isBookend = k == "title" || k == "section" || k == "closing" || k == "agenda"
			}
			p, c := compareFingerprints(srcFP, recoFP, i, findings, isBookend)
			penalties += p
			checks += c
		}
	}

	if checks > 0 {
		score = score - penalties/float64(checks)
	}
	if score < 0 {
		score = 0
	}
	return score
}

// compareFingerprints compares a source IR fingerprint against a reconstruction
// fingerprint, appending gap items to findings[slideIndex]. Returns (totalPenalty, checks).
//
// isBookend: when true (title/closing/section/agenda slides), skip shape-family
// gap checks because bookend slides are matched to minimal-chrome archetypes
// and their decorative shapes are not expected to be reproduced.
func compareFingerprints(src, reco SlideFingerprint, slideIndex int, findings []SlideFinding, isBookend bool) (penalty float64, checks int) {
	// For bookend slides (title/closing/section/agenda), the source has branded
	// decorative chrome (product screenshots, small UI elements) that the
	// corresponding minimal archetype doesn't reproduce. Skip structural shape
	// checks to avoid false critical/major gaps.
	if isBookend {
		return 0, 0
	}

	// 1. roundRect presence: the most visually distinctive shape family.
	if src.HasRoundRects {
		checks++
		if !reco.HasRoundRects {
			penalty += 1.0
			if slideIndex < len(findings) {
				findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
					Kind:        GapMissingShapeFamily,
					Severity:    SeverityCritical,
					Description: fmt.Sprintf("slide %d: source IR has roundRect card shapes but reconstruction has none", slideIndex+1),
					Expected:    "roundRect shapes",
					Actual:      "none found",
				})
			}
		}
	}

	// 2. Ellipse/circle markers (timeline dots, milestone circles).
	if src.HasEllipses {
		checks++
		if !reco.HasEllipses {
			penalty += 0.5
			if slideIndex < len(findings) {
				findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
					Kind:        GapMissingShapeFamily,
					Severity:    SeverityMajor,
					Description: fmt.Sprintf("slide %d: source IR has ellipse/circle markers but reconstruction has none", slideIndex+1),
					Expected:    "ellipse shapes",
					Actual:      "none found",
				})
			}
		}
	}

	// 3. Stripe presence (narrow accent bars along card edges).
	if src.HasStripes {
		checks++
		if !reco.HasStripes {
			penalty += 0.5
			if slideIndex < len(findings) {
				findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
					Kind:        GapMissingShapeFamily,
					Severity:    SeverityMajor,
					Description: fmt.Sprintf("slide %d: source IR has narrow stripe/accent bar shapes but reconstruction has none", slideIndex+1),
					Expected:    "stripe/accent bar shapes",
					Actual:      "none found",
				})
			}
		}
	}

	// 4. Chrome shape count: large deviations indicate dropped chrome.
	if src.ChromeShapeCount > 3 {
		checks++
		ratio := float64(reco.ChromeShapeCount) / float64(src.ChromeShapeCount)
		if ratio < 0.5 {
			penalty += 0.8
			if slideIndex < len(findings) {
				findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
					Kind:        GapWrongShapeCount,
					Severity:    SeverityMajor,
					Description: fmt.Sprintf("slide %d: reconstruction has %d chrome shapes vs %d in source (%.0f%% coverage)", slideIndex+1, reco.ChromeShapeCount, src.ChromeShapeCount, ratio*100),
					Expected:    strconv.Itoa(src.ChromeShapeCount),
					Actual:      strconv.Itoa(reco.ChromeShapeCount),
				})
			}
		} else if ratio < 0.7 {
			penalty += 0.4
			if slideIndex < len(findings) {
				findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
					Kind:        GapWrongShapeCount,
					Severity:    SeverityMinor,
					Description: fmt.Sprintf("slide %d: reconstruction has %d chrome shapes vs %d in source (%.0f%% coverage)", slideIndex+1, reco.ChromeShapeCount, src.ChromeShapeCount, ratio*100),
					Expected:    strconv.Itoa(src.ChromeShapeCount),
					Actual:      strconv.Itoa(reco.ChromeShapeCount),
				})
			}
		}
	}

	// 5. Accent fill colors from source IR: check each one appears in reconstruction.
	for _, accentColor := range src.AccentColors {
		checks++
		if !slideContainsColor(Slide{Nodes: nodesFlatFromFingerprint(reco)}, accentColor) {
			// Also do a looser hex comparison.
			if !recoContainsFill(reco, accentColor) {
				penalty += 0.3
				if slideIndex < len(findings) {
					findings[slideIndex].Gaps = append(findings[slideIndex].Gaps, GapItem{
						Kind:        GapMissingFillColor,
						Severity:    SeverityMajor,
						Description: fmt.Sprintf("slide %d: source accent color %s not found in reconstruction", slideIndex+1, accentColor),
						Expected:    accentColor,
						Actual:      "absent",
					})
				}
			}
		}
	}

	return penalty, checks
}

// recoContainsFill checks if the reconstructed fingerprint contains a given fill color.
func recoContainsFill(reco SlideFingerprint, hexColor string) bool {
	needle := strings.ToLower(strings.TrimPrefix(hexColor, "#"))
	for _, sf := range reco.ShapeFamilies {
		for _, f := range sf.Fills {
			f = strings.ToLower(strings.TrimPrefix(f, "#"))
			if f == needle {
				return true
			}
			// Allow ±10 per channel delta for color approximations (gradient→solid).
			if len(needle) == 6 && len(f) == 6 {
				r1, g1, b1, e1 := parseHexColor("#" + needle)
				r2, g2, b2, e2 := parseHexColor("#" + f)
				if e1 == nil && e2 == nil {
					if math.Abs(float64(r1)-float64(r2)) <= 10 &&
						math.Abs(float64(g1)-float64(g2)) <= 10 &&
						math.Abs(float64(b1)-float64(b2)) <= 10 {
						return true
					}
				}
			}
		}
	}
	return false
}

// nodesFlatFromFingerprint builds a minimal []Node slice so slideContainsColor
// can check shape fill colors stored in a reconstruction fingerprint.
func nodesFlatFromFingerprint(fp SlideFingerprint) []Node {
	var nodes []Node
	for _, sf := range fp.ShapeFamilies {
		for _, fill := range sf.Fills {
			nodes = append(nodes, Node{
				Type: "shape",
				Fill: fill,
			})
		}
	}
	return nodes
}

func computeStyleScore(source *themes.TemplateModel, reco *SceneGraph, findings []SlideFinding, slideCount int, fingerprints []SlideFingerprint) float64 {
	if slideCount == 0 {
		return 1.0
	}

	totalPenalty := 0.0
	pairCount := len(source.Slides)
	if len(reco.Slides) < pairCount {
		pairCount = len(reco.Slides)
	}

	for i := 0; i < pairCount; i++ {
		srcSlide := source.Slides[i]
		recoSlide := reco.Slides[i]

		// Bookend slides (title/closing/section/agenda) are matched to minimal-chrome
		// archetypes intentionally — skip style checks that would produce false gaps.
		isBookend := themes.InferLayoutKind(srcSlide) != "content"

		// Background color check.
		if srcSlide.Background.Color != "" {
			recoColor := findBackgroundColor(recoSlide)
			if recoColor != "" {
				delta := colorDelta(srcSlide.Background.Color, recoColor)
				if delta > 20 {
					totalPenalty += 0.15
					if i < len(findings) {
						findings[i].Gaps = append(findings[i].Gaps, GapItem{
							Kind:        GapWrongBackground,
							Severity:    SeverityMajor,
							Description: fmt.Sprintf("background color mismatch (delta %.0f > 20)", delta),
							Expected:    srcSlide.Background.Color,
							Actual:      recoColor,
						})
					}
				}
			}
		}

		// Primary text color check: first IRPlaceholder with a non-empty Style.Color.
		// Skip for bookend slides — their archetypes may intentionally use the
		// template's primary text color rather than the placeholder override.
		if !isBookend {
			srcTextColor := firstPlaceholderTextColor(srcSlide)
			if srcTextColor != "" {
				if !slideContainsColor(recoSlide, srcTextColor) {
					totalPenalty += 0.10
					if i < len(findings) {
						findings[i].Gaps = append(findings[i].Gaps, GapItem{
							Kind:        GapWrongTextColor,
							Severity:    SeverityMajor,
							Description: "primary text color from source not found in reconstruction slide",
							Expected:    srcTextColor,
						})
					}
				}
			}
		}

		// Check that accent colors from the fingerprint survive into the reco style.
		if i < len(fingerprints) {
			_ = fingerprints[i] // already checked in structure; no double-penalty here
		}
	}

	style := 1.0 - totalPenalty/float64(slideCount)
	if style < 0 {
		style = 0
	}
	return style
}

func computeContentScore(source *themes.TemplateModel, reco *SceneGraph, findings []SlideFinding) float64 {
	total := 0
	matched := 0

	pairCount := len(source.Slides)
	if len(reco.Slides) < pairCount {
		pairCount = len(reco.Slides)
	}

	for i := 0; i < pairCount; i++ {
		srcSlide := source.Slides[i]
		recoSlide := reco.Slides[i]

		// Bookend slides (title/closing/section/agenda) use minimal-chrome
		// archetypes that intentionally don't reproduce every chrome text object
		// (person names, watermarks, decorative slogans). Skip content checks for
		// these slides to avoid false-positive missing-content gaps.
		if themes.InferLayoutKind(srcSlide) != "content" {
			continue
		}

		recoText := strings.ToLower(collectSlideText(recoSlide))

		for _, obj := range srcSlide.Objects {
			if obj.Kind != "text" {
				continue
			}
			// Skip footer/edge text objects (same filter as projectText).
			// These are bylines, slide numbers, and repeated branding marks at
			// canvas edges that archetypes don't reproduce as slots.
			if obj.Y > 900 || obj.X > 1800 {
				continue
			}
			text := strings.TrimSpace(obj.Text)
			if len(text) <= 3 {
				continue
			}
			total++
			if strings.Contains(recoText, strings.ToLower(text)) {
				matched++
			} else {
				if i < len(findings) {
					findings[i].Gaps = append(findings[i].Gaps, GapItem{
						Kind:        GapMissingContent,
						Severity:    SeverityMajor,
						Description: fmt.Sprintf("source text %q not found in reconstruction", truncate(text, 60)),
						Expected:    text,
					})
				}
			}
		}
	}

	if total == 0 {
		return 1.0
	}
	return float64(matched) / float64(total)
}

// -------------------------------------------------------------------------
// Fingerprint shape-detection helpers
// -------------------------------------------------------------------------

// normalizeGeom returns the canonical geom family for an IRChrome object.
func normalizeGeom(obj themes.IRChrome) string {
	switch obj.Kind {
	case "image":
		return "image"
	case "line":
		return "line"
	case "ellipse":
		return "ellipse"
	case "path":
		return "path"
	case "text":
		return "text"
	}
	if obj.Kind == "rect" || obj.Kind == "" {
		if obj.RectRadius > 0 {
			return "roundRect"
		}
		return "rect"
	}
	return obj.Kind
}

// isStripeShape returns true when an IRChrome object looks like a narrow accent
// bar (left stripe, top stripe). These are common in card-based templates.
func isStripeShape(obj themes.IRChrome) bool {
	if obj.Kind == "image" || obj.Kind == "text" || obj.Kind == "line" {
		return false
	}
	w := obj.W
	h := obj.H
	if w <= 0 || h <= 0 {
		return false
	}
	// A stripe is a visible narrow band. Exclude single-pixel dividers
	// (h=1 horizontal lines) — they render as invisible and are not accent stripes.
	// left/right stripe: w < 30px and h > 80px
	// top/bottom stripe: h >= 3px and h < 30px and w > 200px
	isLeftStripe := w <= 28 && h >= 80
	isTopStripe := h >= 3 && h <= 28 && w >= 200
	return isLeftStripe || isTopStripe
}

// isNodeStripe returns true when an ASD Node looks like a stripe shape.
// A stripe is a narrow decorative band (left bar or top bar), not a 1px line.
func isNodeStripe(n Node) bool {
	if n.Type != "shape" {
		return false
	}
	w := n.Geometry.W
	h := n.Geometry.H
	if w <= 0 || h <= 0 {
		return false
	}
	// Exclude single-pixel-height shapes (horizontal dividers) — they look
	// like thin lines, not visible accent stripes. A real top-stripe needs
	// at least 3px height so it renders as a visible band.
	isLeftStripe := w <= 28 && h >= 80
	isTopStripe := h >= 3 && h <= 28 && w >= 200
	return isLeftStripe || isTopStripe
}

// isTimelineMarker returns true when an IRChrome looks like a small connector
// dot that forms part of a timeline/roadmap rail (tiny ellipse near a line).
func isTimelineMarker(obj themes.IRChrome, siblings []themes.IRChrome) bool {
	if obj.Kind != "ellipse" {
		return false
	}
	maxDim := obj.W
	if obj.H > maxDim {
		maxDim = obj.H
	}
	if maxDim > 56 || maxDim < 8 {
		return false
	}
	// Check if there is a horizontal line among siblings.
	for _, s := range siblings {
		if s.Kind == "line" || (s.H <= 10 && s.W > 400) {
			return true
		}
	}
	return false
}

// isAccentColor returns true when the color is not white, near-white, black,
// near-black, or a common grey, so it is likely a template accent color.
func isAccentColor(hexColor string) bool {
	r, g, b, err := parseHexColor(hexColor)
	if err != nil {
		return false
	}
	// Luminance (approximate).
	lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	if lum > 230 { // near-white
		return false
	}
	if lum < 20 { // near-black
		return false
	}
	// Low saturation → grey family.
	maxC := float64(r)
	if float64(g) > maxC {
		maxC = float64(g)
	}
	if float64(b) > maxC {
		maxC = float64(b)
	}
	minC := float64(r)
	if float64(g) < minC {
		minC = float64(g)
	}
	if float64(b) < minC {
		minC = float64(b)
	}
	saturation := 0.0
	if maxC > 0 {
		saturation = (maxC - minC) / maxC
	}
	return saturation > 0.15
}

// appendUnique appends s to slice only if it isn't already there (case-insensitive,
// and caps the list at 6 entries to avoid bloat).
func appendUnique(slice []string, s string) []string {
	if len(slice) >= 6 {
		return slice
	}
	s = strings.ToLower(s)
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// -------------------------------------------------------------------------
// Node-tree utilities
// -------------------------------------------------------------------------

// slideIsEmpty returns true when a slide has no nodes.
func slideIsEmpty(s Slide) bool {
	return len(s.Nodes) == 0
}

// findBackgroundColor returns the fill color of the first full-canvas
// decorative background shape in the slide.
func findBackgroundColor(s Slide) string {
	for _, n := range s.Nodes {
		if n.Type != "shape" {
			continue
		}
		alt, _ := n.Props["alt"].(string)
		dec, _ := n.Props["decorative"].(string)
		if alt == "" || dec == "true" {
			if n.Fill != "" {
				return n.Fill
			}
		}
	}
	return ""
}

// firstPlaceholderTextColor returns the first non-empty Style.Color from the
// source slide's content placeholders (excluding footers, slide numbers, and
// edge-area placeholders that the reconstruction does not reproduce).
func firstPlaceholderTextColor(layout themes.IRLayout) string {
	for _, ph := range layout.Placeholders {
		// Skip footer/edge placeholders (same filter as projectText).
		if ph.Y > 900 || ph.X > 1800 {
			continue
		}
		if ph.Style.Color != "" {
			return ph.Style.Color
		}
	}
	return ""
}

// slideContainsColor checks whether the given hex color string appears in any
// node's color-related attributes in the slide (fill, line/stroke, Props["color"], or run colors).
func slideContainsColor(s Slide, hexColor string) bool {
	needle := strings.ToLower(hexColor)
	for _, n := range s.Nodes {
		if strings.ToLower(n.Fill) == needle {
			return true
		}
		if strings.ToLower(n.Line) == needle {
			return true
		}
		if c, _ := n.Props["color"].(string); strings.ToLower(c) == needle {
			return true
		}
		for _, run := range n.Runs {
			if strings.ToLower(run.Color) == needle {
				return true
			}
		}
	}
	return false
}

// collectSlideText concatenates all text content from a reconstructed slide's
// node tree.
func collectSlideText(s Slide) string {
	var b strings.Builder
	for _, n := range s.Nodes {
		collectNodeText(n, &b)
	}
	return b.String()
}

func collectNodeText(n Node, b *strings.Builder) {
	if n.Text != "" {
		b.WriteString(" ")
		b.WriteString(n.Text)
	}
	for _, run := range n.Runs {
		if run.Text != "" {
			b.WriteString(" ")
			b.WriteString(run.Text)
		}
	}
	for _, child := range n.Children {
		collectNodeText(child, b)
	}
}

// -------------------------------------------------------------------------
// Color math
// -------------------------------------------------------------------------

// colorDelta computes the average per-channel delta (ΔR+ΔG+ΔB)/3 between two
// #RRGGBB hex color strings. Returns 255 on parse error.
func colorDelta(a, b string) float64 {
	ra, ga, ba, errA := parseHexColor(a)
	rb, gb, bb, errB := parseHexColor(b)
	if errA != nil || errB != nil {
		return 255
	}
	dr := math.Abs(float64(ra) - float64(rb))
	dg := math.Abs(float64(ga) - float64(gb))
	db := math.Abs(float64(ba) - float64(bb))
	return (dr + dg + db) / 3.0
}

// parseHexColor parses a #RRGGBB or RRGGBB hex string into R, G, B components.
func parseHexColor(s string) (r, g, b uint8, err error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %q", s)
	}
	val, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %q: %w", s, err)
	}
	r = uint8(val >> 16)
	g = uint8((val >> 8) & 0xFF)
	b = uint8(val & 0xFF)
	return r, g, b, nil
}

// truncate shortens a string to at most max runes, appending "…" if clipped.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
