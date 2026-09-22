// slides-diag: PPTX import fidelity diagnostics tool.
//
// The reference thumbnail is the original .pptx rendered by pptx-glimpse,
// not a re-render of the import IR. The generated column is the reconstructed
// archetype, screenshotted with headless Chrome. The pixel diff is reference vs
// filled reconstruction.
//
// Usage:
//
//	go run ./cmd/slides-diag -pptx /path/to/template.pptx [-out report.html]
//
// -browser is accepted and ignored. Reference thumbnails no longer need Chrome;
// Chrome is still used for the generated column.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	gohtml "html"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/pdfgen"
)

func main() {
	pptxPath := flag.String("pptx", "", "Path to the .pptx file to analyse (required)")
	outPath := flag.String("out", "", "Output HTML path (default: <name>-diag.html next to the pptx)")
	_ = flag.Bool("browser", false, "Ignored. Reference thumbnails come from pptx-glimpse; Chrome is still used for the generated column.")
	flag.Parse()

	if *pptxPath == "" {
		fmt.Fprintln(os.Stderr, "slides-diag: -pptx is required")
		flag.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(*pptxPath)
	if err != nil {
		fatalf("read pptx: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	wd, scriptPath := resolveWorkerPaths()

	fmt.Fprintf(os.Stderr, "▶  Importing %s …\n", filepath.Base(*pptxPath))
	t0 := time.Now()
	runner := pptxworker.ImportRunner{WorkingDir: wd, ScriptPath: scriptPath}
	resp, err := runner.Run(context.Background(), pptxworker.ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		fatalf("import worker: %v", err)
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		fatalf("decode template: %v", err)
	}
	fmt.Fprintf(os.Stderr, "✓  %d archetypes in %s\n", len(tmpl.Archetypes), time.Since(t0).Round(time.Millisecond))

	if tmpl.Model == nil {
		fatalf("no IR model in import result")
	}

	fmt.Fprintln(os.Stderr, "▶  Reconstructing …")
	scene, recoMarkups, warnings, err := slides.ReconstructMarkups(tmpl.Model, tmpl.Archetypes, tmpl.Assets)
	if err != nil {
		fatalf("reconstruct: %v", err)
	}
	fmt.Fprintf(os.Stderr, "✓  %d slides, %d warnings\n", len(scene.Slides), len(warnings))

	fmt.Fprintln(os.Stderr, "▶  Comparing (structural) …")
	report := slides.Compare(tmpl.Model, scene)
	fmt.Fprintf(os.Stderr, "✓  Fidelity %.1f%%  structure=%.1f%%  style=%.1f%%  content=%.1f%%  passed=%v\n",
		report.FidelityScore*100, report.StructureScore*100,
		report.StyleScore*100, report.ContentScore*100, report.Passed)
	if gaps := slides.AuditImportComponents(tmpl.Model, tmpl.Archetypes); len(gaps) > 0 {
		fmt.Fprintf(os.Stderr, "⚠  %d component gaps (deck-independent; fix the importer, not this template)\n", len(gaps))
		shown := 0
		for _, g := range gaps {
			fmt.Fprintf(os.Stderr, "  %s\n", g)
			if shown++; shown >= 12 {
				fmt.Fprintf(os.Stderr, "  … %d more\n", len(gaps)-shown)
				break
			}
		}
	}

	// Print archetype summary
	fmt.Fprintf(os.Stderr, "\n── Archetypes (%d) ─────────────────────────────────\n", len(tmpl.Archetypes))
	for _, a := range tmpl.Archetypes {
		mark := "  "
		if len(a.FillSlots) >= 2 {
			mark = "✓ "
		}
		fmt.Fprintf(os.Stderr, "  %s%-22s %d slots  %s\n", mark, a.Kind, len(a.FillSlots), clip(a.Title, 40))
	}

	// Print top gaps
	fmt.Fprintln(os.Stderr)
	n := 0
	for _, sf := range report.SlideFindings {
		for _, g := range sf.Gaps {
			fmt.Fprintf(os.Stderr, "  slide%02d %-10s %s\n", sf.SlideIndex+1, g.Severity, clip(g.Description, 90))
			if n++; n >= 15 {
				fmt.Fprintln(os.Stderr, "  … (more in report)")
				goto doneGaps
			}
		}
	}
doneGaps:

	// Load slides runtime
	runtimeJS, rtErr := loadRuntime(wd)
	if rtErr != nil {
		fmt.Fprintf(os.Stderr, "⚠  %v — slide previews will be text-only\n", rtErr)
	}

	fmt.Fprintln(os.Stderr, "▶  Rendering reference thumbnails (pptx-glimpse) …")
	refPNG, refWarns, refErr := renderReferenceThumbs(b64, wd, 480)
	if refErr != nil {
		fmt.Fprintf(os.Stderr, "⚠  reference thumbnails failed: %v\n", refErr)
	} else {
		fmt.Fprintf(os.Stderr, "✓  %d reference thumbnails\n", len(refPNG))
		warnings = append(warnings, refWarns...)
	}

	var pngComparisons []slideComparison
	if runtimeJS != "" && len(refPNG) > 0 {
		fmt.Fprintln(os.Stderr, "▶  Rendering generated slides via headless Chrome …")
		mgr := browser.NewManager(browser.BrowserConfig{
			Headless:  true,
			NoSandbox: os.Getuid() == 0,
		})
		defer mgr.Cleanup()
		pngComparisons, err = renderSlideComparisons(tmpl, scene, []byte(runtimeJS), mgr, refPNG)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  generated PNG rendering failed: %v\n", err)
			pngComparisons = nil
		}
	}
	if len(pngComparisons) == 0 && len(refPNG) > 0 {
		pngComparisons = referenceOnlyComparisons(tmpl, refPNG)
	}

	out := *outPath
	if out == "" {
		base := strings.TrimSuffix(filepath.Base(*pptxPath), filepath.Ext(*pptxPath))
		out = filepath.Join(filepath.Dir(*pptxPath), base+"-diag.html")
	}

	page := buildReport(tmpl, scene, recoMarkups, report, warnings, runtimeJS, pngComparisons)
	if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
		fatalf("write: %v", err)
	}
	fmt.Fprintf(os.Stderr, "\n📄  %s\n", out)
	if len(pngComparisons) > 0 {
		fmt.Fprintln(os.Stderr, "    4-column comparison: SOURCE | ARCHETYPE (blank) | FILLED | DIFF (SOURCE vs ARCH)")
	} else {
		fmt.Fprintln(os.Stderr, "    Open in browser — SOURCE | GENERATED | GAPS for every slide.")
		fmt.Fprintln(os.Stderr, "    Tip: add -browser flag for pixel-accurate PNG comparison (requires headless Chrome)")
	}
}

// ─── Slide PNG comparison ──────────────────────────────────────────────────

// slideComparison holds the reference PPTX thumbnail and the generated render.
//
//	SourcePNG: original .pptx slide rendered by pptx-glimpse (ground truth)
//	FilledPNG: reconstructed archetype screenshotted by headless Chrome
//	DiffPNG:   pixel diff of reference vs filled (red = visual gap)
//	Similarity: foreground-weighted pixel similarity of reference vs filled
//	ArchKind:  which archetype was selected
type slideComparison struct {
	SlideIndex int
	SourcePNG  string
	FilledPNG  string
	DiffPNG    string
	Similarity float64
	ArchKind   string
	ArchTitle  string
	RefWarning string
}

// renderReferenceThumbs rasterizes the original PPTX with pptx-glimpse.
// The map key is the 0-based slide index. PNG bytes are raw, not data-URIs.
func renderReferenceThumbs(pptxBase64, workingDir string, width int) (map[int][]byte, []string, error) {
	script := filepath.Join(filepath.Dir(workingDir), "pkg", "docs", "slides", "pptxworker", "render_thumbs.mjs")
	if _, err := os.Stat(script); err != nil {
		if root := os.Getenv("ASTONISH_ROOT"); root != "" {
			script = filepath.Join(root, "pkg", "docs", "slides", "pptxworker", "render_thumbs.mjs")
		}
	}
	resp, err := (pptxworker.ThumbRunner{
		WorkingDir: workingDir,
		ScriptPath: script,
		Timeout:    3 * time.Minute,
	}).Run(context.Background(), pptxworker.ThumbRequest{
		PPTXBase64: pptxBase64,
		Width:      width,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make(map[int][]byte, len(resp.Slides))
	for _, s := range resp.Slides {
		if s.PNGBase64 == "" {
			continue
		}
		raw, decErr := s.DecodePNG()
		if decErr != nil {
			resp.Warnings = append(resp.Warnings, decErr.Error())
			continue
		}
		idx := s.SlideNumber - 1
		if idx < 0 {
			continue
		}
		out[idx] = raw
	}
	return out, resp.Warnings, nil
}

func referenceOnlyComparisons(tmpl themes.Template, refPNG map[int][]byte) []slideComparison {
	archFPs := buildArchetypeFPs(tmpl.Archetypes)
	var out []slideComparison
	for i, src := range tmpl.Model.Slides {
		raw, ok := refPNG[i]
		if !ok || len(raw) == 0 {
			continue
		}
		arch := selectBestArchetype(tmpl.Archetypes, archFPs, src, i)
		out = append(out, slideComparison{
			SlideIndex: i,
			SourcePNG:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw),
			ArchKind:   arch.Kind,
			ArchTitle:  arch.Title,
		})
	}
	return out
}

// renderSlideComparisons screenshots the filled reconstruction and diffs it
// against the pptx-glimpse reference thumbnail for the same slide.
func renderSlideComparisons(tmpl themes.Template, scene *slides.SceneGraph, runtimeJS []byte, bp pdfgen.BrowserProvider, refPNG map[int][]byte) ([]slideComparison, error) {
	const scale = 0.25 // 1920x1080 → 480x270, matching the reference width

	archFPs := buildArchetypeFPs(tmpl.Archetypes)
	var results []slideComparison

	for i, srcSlide := range tmpl.Model.Slides {
		refRaw, hasRef := refPNG[i]
		if !hasRef || len(refRaw) == 0 {
			fmt.Fprintf(os.Stderr, "  slide %d: no reference thumbnail\n", i+1)
			continue
		}
		arch := selectBestArchetype(tmpl.Archetypes, archFPs, srcSlide, i)

		var filledPNG []byte
		if i < len(scene.Slides) {
			filledScene := slides.SceneGraph{
				SchemaVersion: slides.SchemaV2,
				Title:         "filled",
				Theme:         tmpl.Tokens,
				Assets:        tmpl.Assets,
				Slides:        []slides.Slide{scene.Slides[i]},
			}
			filledHTML, exportErr := (slides.HTMLExporter{RuntimeJS: runtimeJS}).Export(filledScene)
			if exportErr != nil {
				fmt.Fprintf(os.Stderr, "  slide %d: generated HTML export error: %v\n", i+1, exportErr)
			} else {
				png, pngErr := pdfgen.RenderHTMLToPNGChrome(string(filledHTML.Bytes), bp, pdfgen.ScreenshotOptions{
					Width:               slides.CanvasWidth,
					Height:              slides.CanvasHeight,
					Scale:               scale,
					ReadinessExpression: slides.SlidesReadinessExpression,
					Timeout:             60 * time.Second,
				})
				if pngErr != nil {
					fmt.Fprintf(os.Stderr, "  slide %d: generated PNG render error: %v\n", i+1, pngErr)
				} else {
					filledPNG = png
				}
			}
		}

		cmp := slideComparison{
			SlideIndex: i,
			SourcePNG:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(refRaw),
			ArchKind:   arch.Kind,
			ArchTitle:  arch.Title,
		}
		if len(filledPNG) > 0 {
			cmp.FilledPNG = "data:image/png;base64," + base64.StdEncoding.EncodeToString(filledPNG)
			cmp.Similarity = pixelSimilarity(refRaw, filledPNG)
			if diff := generateDiffPNG(refRaw, filledPNG, 15); len(diff) > 0 {
				cmp.DiffPNG = "data:image/png;base64," + base64.StdEncoding.EncodeToString(diff)
			}
			fmt.Fprintf(os.Stderr, "  slide %d [%s]: pixel-similarity=%.1f%%\n", i+1, arch.Kind, cmp.Similarity*100)
		} else {
			fmt.Fprintf(os.Stderr, "  slide %d [%s]: reference only\n", i+1, arch.Kind)
		}
		results = append(results, cmp)
	}
	return results, nil
}

// buildArchetypeFPs builds a lookup of archetype fingerprints for selectBestArchetype.
func buildArchetypeFPs(archetypes []themes.Archetype) []archetypeFingerprint {
	fps := make([]archetypeFingerprint, len(archetypes))
	for i, a := range archetypes {
		fp := archetypeFingerprint{
			slotCount:        len(a.FillSlots),
			sourceSlideIndex: a.SourceSlideIndex, // 1-based; 0 = unset
		}
		if a.Markup != "" {
			slide, _, err := slides.ParseSlide(a.Markup)
			if err == nil {
				rr := 0
				shapes := 0
				for _, n := range slide.Nodes {
					if n.Type != "shape" {
						continue
					}
					shapes++
					g := n.Geom
					if g == "" {
						g = "rect"
					}
					if g == "roundRect" {
						rr++
						fp.hasRR = true
					}
					if g == "ellipse" {
						fp.hasEl = true
					}
					if (n.Geometry.W <= 28 && n.Geometry.H >= 80) ||
						(n.Geometry.H <= 28 && n.Geometry.W >= 200) {
						fp.hasStr = true
					}
				}
				fp.shapeN = shapes
				fp.rrN = rr
			}
		}
		fps[i] = fp
	}
	return fps
}

// archetypeFingerprint is a compact representation for selectBestArchetype.
type archetypeFingerprint struct {
	hasRR     bool
	hasEl     bool
	hasStr    bool
	shapeN    int
	slotCount int
	rrN       int
	// sourceSlideIndex mirrors Archetype.SourceSlideIndex for Phase-0 matching.
	// 1-based (0 = unset / chrome-kind archetype).
	sourceSlideIndex int
}

// selectBestArchetype mirrors the selectArchetype logic from reconstruct.go.
// Uses InferLayoutKind for bookend detection (same as production code) so the
// diagnostic shows the same archetype selection as ReconstructScene.
func selectBestArchetype(archetypes []themes.Archetype, fps []archetypeFingerprint, src themes.IRLayout, srcSlideIdx ...int) themes.Archetype {
	if len(archetypes) == 0 {
		return themes.Archetype{}
	}

	// Phase 0: direct source-slide-index match (mirrors reconstruct.go Phase 0).
	// The import_worker assigns SourceSlideIndex (1-based) to every per-slide
	// pattern archetype. If we find an archetype built from this exact source
	// slide, use it immediately.
	if len(srcSlideIdx) > 0 {
		want1Based := srcSlideIdx[0] + 1
		for i, a := range archetypes {
			if fps[i].sourceSlideIndex == want1Based {
				return a
			}
		}
	}

	// Phase 1: bookend detection — use same logic as production selectArchetype.
	kind := themes.InferLayoutKind(src)
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

	// Phase 2: shape-fingerprint scoring for content slides.
	srcRR, srcShapes, srcTextChrome := 0, 0, 0
	for _, o := range src.Objects {
		if o.Kind == "image" {
			continue
		}
		if o.Kind == "text" {
			if len(o.Text) > 3 {
				srcTextChrome++
			}
			continue
		}
		srcShapes++
		if o.RectRadius > 0 {
			srcRR++
		}
	}
	srcPH := len(src.Placeholders)

	// Effective slot demand: use larger of formal placeholders and text chrome count.
	effectiveSlotDemand := srcPH
	if srcTextChrome > effectiveSlotDemand {
		effectiveSlotDemand = srcTextChrome
	}

	bestScore := -999.0
	best := archetypes[0]

	for i, a := range archetypes {
		fp := fps[i]
		score := 0.0

		// Hard penalty for 0-1 slot archetypes on content slides
		if fp.slotCount < 2 && srcShapes > 3 {
			score -= 5.0
		}

		// Hard penalty for bookend archetypes in Phase 2 (content slide selection).
		archKindBase := a.Kind
		if idx := strings.LastIndexByte(a.Kind, '-'); idx > 0 {
			if _, err := strconv.Atoi(a.Kind[idx+1:]); err == nil {
				archKindBase = a.Kind[:idx]
			}
		}
		if archKindBase == "title" || archKindBase == "section" || archKindBase == "closing" || archKindBase == "agenda" {
			score -= 10.0
		}

		// RoundRect count similarity (highest weight)
		if srcRR > 0 || fp.rrN > 0 {
			maxRR := srcRR
			if fp.rrN > maxRR {
				maxRR = fp.rrN
			}
			diff := srcRR - fp.rrN
			if diff < 0 {
				diff = -diff
			}
			score += 4.0 * (1.0 - float64(diff)/float64(maxRR))
		} else {
			score += 4.0
		}

		// Chrome shape count similarity
		if srcShapes > 0 && fp.shapeN > 0 {
			ratio := float64(fp.shapeN) / float64(srcShapes)
			dev := math.Abs(ratio - 1.0)
			if dev > 1.0 {
				dev = 1.0
			}
			score += 2.0 * (1.0 - dev)
		}

		// Slot count similarity using effective slot demand
		if effectiveSlotDemand > 0 || fp.slotCount > 0 {
			maxS := effectiveSlotDemand
			if fp.slotCount > maxS {
				maxS = fp.slotCount
			}
			if maxS > 0 {
				diff := effectiveSlotDemand - fp.slotCount
				if diff < 0 {
					// Archetype has more slots than needed: mild penalty
					diff = -diff / 2
				}
				score += 2.0 * (1.0 - float64(diff)/float64(maxS))
			}
		}

		if score > bestScore {
			bestScore = score
			best = a
		}
	}
	return best
}

// pixelSimilarity computes a foreground-weighted structural similarity between
// two PNG images. Returns 1.0 for identical, 0.0 for completely different.
//
// Problem with naive luminance MAD: slides have large white backgrounds (~70%
// of pixels). Two completely different slides that share the same white
// background score ~95% similar — meaningless. A blank slide vs any slide with
// white background is "similar".
//
// Fix: foreground-weighted diff. Each pixel pair is weighted by how much
// "foreground" content it carries — near-white pixels get weight ≈ 0 (they're
// background), strongly-colored pixels get weight = 1. The similarity is the
// weighted average of per-pixel agreement.
//
// This means: two slides with different card layouts but same background still
// score low (the card pixels ARE the foreground and they differ). Two slides
// with matching card positions score high.
func pixelSimilarity(a, b []byte) float64 {
	imgA, errA := png.Decode(bytes.NewReader(a))
	imgB, errB := png.Decode(bytes.NewReader(b))
	if errA != nil || errB != nil {
		return 0
	}
	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()

	w := boundsA.Max.X
	h := boundsA.Max.Y
	if boundsB.Max.X < w {
		w = boundsB.Max.X
	}
	if boundsB.Max.Y < h {
		h = boundsB.Max.Y
	}
	if w == 0 || h == 0 {
		return 0
	}

	totalWeight := 0.0
	weightedMatch := 0.0

	// Sample every 3rd pixel for performance.
	for y := boundsA.Min.Y; y < boundsA.Min.Y+h; y += 3 {
		for x := boundsA.Min.X; x < boundsA.Min.X+w; x += 3 {
			ra, ga, ba, _ := imgA.At(x, y).RGBA()
			rb, gb, bb, _ := imgB.At(x, y).RGBA()

			// Convert 0..65535 → 0..1
			rA, gA, bA := float64(ra)/65535.0, float64(ga)/65535.0, float64(ba)/65535.0
			rB, gB, bB := float64(rb)/65535.0, float64(gb)/65535.0, float64(bb)/65535.0

			lumA := 0.299*rA + 0.587*gA + 0.114*bA
			lumB := 0.299*rB + 0.587*gB + 0.114*bB

			// Foreground weight: how far from white is each pixel.
			// Near-white (lum > 0.92) → weight ≈ 0.
			// Dark or colorful → weight up to 1.
			fgA := math.Max(0, 1.0-lumA/0.92)
			fgB := math.Max(0, 1.0-lumB/0.92)
			// Use the MAX of both weights: a pixel that is foreground in
			// EITHER image must be counted (a missing shape = a dark area in
			// source but white in generated → high fgA, low fgB → still counts).
			weight := math.Max(fgA, fgB)
			// Minimum weight so background regions still contribute a little
			// (prevents division by zero and anchors the score).
			weight = math.Max(weight, 0.05)

			// Per-pixel agreement: how similar are the two pixels?
			// Use luminance difference as the signal.
			lumDiff := math.Abs(lumA - lumB)
			agree := 1.0 - math.Min(1.0, lumDiff/0.5) // full disagreement at Δlum > 0.5

			totalWeight += weight
			weightedMatch += weight * agree
		}
	}
	if totalWeight == 0 {
		return 1
	}
	return weightedMatch / totalWeight
}

// generateDiffPNG creates a visual diff PNG: green for matching pixels, red for
// differing pixels (>threshold luminance difference). Returns empty slice on error.
func generateDiffPNG(a, b []byte, threshold float64) []byte {
	imgA, errA := png.Decode(bytes.NewReader(a))
	imgB, errB := png.Decode(bytes.NewReader(b))
	if errA != nil || errB != nil {
		return nil
	}
	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()

	w := boundsA.Max.X
	h := boundsA.Max.Y
	if boundsB.Max.X < w {
		w = boundsB.Max.X
	}
	if boundsB.Max.Y < h {
		h = boundsB.Max.Y
	}

	diffImg := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ra, ga, ba, _ := imgA.At(x, y).RGBA()
			rb, gb, bb, _ := imgB.At(x, y).RGBA()
			lumA := (0.299*float64(ra) + 0.587*float64(ga) + 0.114*float64(ba)) / 256.0
			lumB := (0.299*float64(rb) + 0.587*float64(gb) + 0.114*float64(bb)) / 256.0
			diff := math.Abs(lumA - lumB)
			if diff > threshold {
				// Show diff magnitude as red intensity
				intensity := uint8(math.Min(255, diff*4))
				diffImg.Set(x, y, color.RGBA{R: intensity, G: 0, B: 0, A: 255})
			} else {
				// Dim the matching areas
				avg := uint8((lumA + lumB) / 2 * 0.3)
				diffImg.Set(x, y, color.RGBA{R: 0, G: avg, B: 0, A: 255})
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, diffImg); err != nil {
		return nil
	}
	return buf.Bytes()
}

// ─── HTML report ──────────────────────────────────────────────────────────────

func buildReport(tmpl themes.Template, scene *slides.SceneGraph, recoMarkups []string,
	report slides.CompareReport, warnings []string, runtimeJS string,
	pngComparisons []slideComparison) string {

	assetsJSON, _ := json.Marshal(tmpl.Assets)
	tokenCSS := buildTokenCSS(tmpl.Tokens)
	hasRT := runtimeJS != ""
	hasPNGs := len(pngComparisons) > 0

	// Build a fast lookup for PNG data by slide index
	pngBySlide := map[int]slideComparison{}
	for _, c := range pngComparisons {
		pngBySlide[c.SlideIndex] = c
	}

	var b strings.Builder

	// ── head ─────────────────────────────────────────────────────────────────
	b.WriteString(`<!DOCTYPE html><html lang="en"><head>
<meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Slides Diag — ` + gohtml.EscapeString(tmpl.Label) + `</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;font-size:14px;background:#111;color:#eee}
header{background:#002a86;color:#fff;padding:14px 24px;display:flex;align-items:baseline;gap:16px;flex-wrap:wrap}
header h1{font-size:18px;font-weight:700}
header p{font-size:12px;opacity:.75}
.scores{display:flex;gap:10px;padding:14px 24px;background:#1a1a2e;border-bottom:1px solid #333;flex-wrap:wrap}
.sc{background:#222;border-radius:6px;padding:10px 16px;min-width:130px}
.sc .v{font-size:24px;font-weight:800}.sc .l{font-size:11px;color:#888;margin-top:2px}
.pass{color:#4caf50}.fail{color:#f44336}
/* tabs */
.tabs{display:flex;background:#1a1a2e;border-bottom:1px solid #333;padding:0 24px;flex-wrap:wrap}
.tab{padding:10px 18px;background:none;border:none;cursor:pointer;font-size:13px;
     font-weight:600;color:#888;border-bottom:3px solid transparent;margin-bottom:-1px}
.tab.on{color:#fff;border-color:#4488ff}
.pane{display:none;padding:20px 24px}.pane.on{display:block}
/* slide comparison grid */
.slides-wrap{display:flex;flex-direction:column;gap:24px}
.slide-row{background:#1e1e2e;border-radius:10px;overflow:hidden}
.slide-header{padding:10px 16px;background:#252540;font-size:13px;font-weight:700;
              display:flex;align-items:center;gap:12px;flex-wrap:wrap}
.slide-header .num{background:#4488ff;color:#fff;padding:2px 8px;border-radius:4px;font-size:12px}
.slide-header .layout-name{color:#aaa;font-weight:400;font-size:12px}
.slide-header .sim{padding:2px 8px;border-radius:4px;font-size:12px;font-weight:700}
.sim-hi{background:#1a3a1a;color:#4caf50}
.sim-mid{background:#2a1a08;color:#ff9800}
.sim-lo{background:#3a1010;color:#f44336}
/* 3-column grid for live renders, 4-column when PNG diff is present */
.slide-cols-3{display:grid;grid-template-columns:1fr 1fr 1fr;gap:1px;background:#333}
.slide-cols-4{display:grid;grid-template-columns:1fr 1fr 1fr 1fr;gap:1px;background:#333}
.slide-col{background:#1a1a2e;padding:12px}
.slide-col label{font-size:10px;font-weight:700;text-transform:uppercase;
                 letter-spacing:.08em;color:#888;display:block;margin-bottom:8px}
/* PNG comparison images */
.png-frame{position:relative;width:100%;padding-top:56.25%;background:#0a0a14;
           border-radius:6px;overflow:hidden;border:2px solid #333}
.png-frame img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain}
.png-frame.src{border-color:#555}
.png-frame.gen{border-color:#4488ff44}
.png-frame.has-crit{border-color:#f44336}
.png-frame.has-major{border-color:#ff980080}
.png-frame.ok{border-color:#4caf5044}
.png-frame.diff{border-color:#ff980080}
/* live render frames */
.frame{position:relative;width:100%;padding-top:56.25%;background:#0a0a14;
       border-radius:6px;overflow:hidden;border:2px solid #333}
.frame.src-frame{border-color:#555}
.frame.arch-frame{border-color:#4488ff44}
.frame.has-crit{border-color:#f44336}
.frame.has-major{border-color:#ff980080}
.frame.ok{border-color:#4caf5044}
.frame-inner{position:absolute;inset:0;overflow:hidden}
ast-deck{position:absolute;width:1920px;height:1080px;transform-origin:top left}
/* gap list */
.gap-list{margin-top:10px;display:flex;flex-direction:column;gap:4px}
.gap{font-size:11px;padding:4px 8px;border-radius:4px;display:flex;gap:6px}
.gap.critical{background:#3a1010;border-left:3px solid #f44336}
.gap.major{background:#2a1a08;border-left:3px solid #ff9800}
.gap.minor{background:#0a2010;border-left:3px solid #4caf50}
.gap .sev{font-weight:700;white-space:nowrap;min-width:50px;font-size:10px}
.ok-msg{font-size:11px;color:#4caf50;margin-top:8px}
/* archetype catalog */
.arch-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(360px,1fr));gap:16px}
.arch-card{background:#1e1e2e;border-radius:8px;overflow:hidden}
.arch-card.in{border-top:3px solid #4caf50}
.arch-card.out{border-top:3px solid #555}
.arch-card.bookend{border-top:3px solid #4488ff}
.arch-preview{width:100%;padding-top:56.25%;position:relative;background:#0a0a14}
.arch-preview .frame-inner{position:absolute;inset:0;overflow:hidden}
.arch-meta{padding:10px 14px 12px;font-size:12px}
.arch-meta h4{font-size:13px;font-weight:700;margin-bottom:4px}
.arch-meta .kind{font-family:monospace;font-size:11px;background:#252540;
                 padding:2px 6px;border-radius:3px;color:#aaa}
.arch-meta .slots{color:#888;margin-top:4px}
/* fingerprint table */
table{width:100%;border-collapse:collapse;font-size:12px}
thead{background:#252540}th,td{padding:6px 10px;text-align:left;border-bottom:1px solid #2a2a3e;vertical-align:top}
.rr{color:#60a5fa}.el{color:#4ade80}.st{color:#fbbf24}.tl{color:#f87171}
.dot{display:inline-block;width:14px;height:14px;border-radius:3px;
     vertical-align:middle;border:1px solid rgba(255,255,255,.2);margin:1px}
code{font-family:monospace;font-size:11px;background:#252540;padding:1px 4px;border-radius:3px}
.badge{display:inline-block;padding:2px 7px;border-radius:4px;font-size:11px;font-weight:700}
.badge-ok{background:#1a3a1a;color:#4caf50;border:1px solid #4caf5044}
.badge-fail{background:#3a1010;color:#f44336;border:1px solid #f4433644}
.badge-info{background:#1a2040;color:#60a5fa;border:1px solid #60a5fa44}
.badge-gray{background:#252540;color:#888}
.no-rt{padding:40px;text-align:center;color:#555;font-size:13px;
       border:2px dashed #333;border-radius:8px}
.notice{padding:10px 16px;background:#1a2a1a;border-left:3px solid #4caf50;
        border-radius:4px;font-size:12px;color:#aaa;margin-bottom:16px}
</style>
`)

	if hasRT {
		b.WriteString("<script>\n" + runtimeJS + "\n</script>\n")
	}
	b.WriteString("</head><body>\n")

	// ── header ────────────────────────────────────────────────────────────────
	passClass, passText := "fail", "FAILED"
	if report.Passed {
		passClass, passText = "pass", "PASSED"
	}
	b.WriteString(`<header>
<h1>Slides Import Diagnostics — ` + gohtml.EscapeString(tmpl.Label) + `</h1>
<p>` + fmt.Sprintf("%d archetypes · %d source slides · structural fidelity ", len(tmpl.Archetypes), len(tmpl.Model.Slides)) +
		`<strong class="` + passClass + `">` + fmt.Sprintf("%.1f%%", report.FidelityScore*100) + `</strong>
	· verdict <strong class="` + passClass + `">` + passText + `</strong>`)
	if hasPNGs {
		avgSim := 0.0
		for _, c := range pngComparisons {
			avgSim += c.Similarity
		}
		avgSim /= float64(len(pngComparisons))
		b.WriteString(fmt.Sprintf(` · pixel-similarity <strong>%.1f%%</strong>`, avgSim*100))
	}
	b.WriteString(`</p>
<p style="font-size:12px;opacity:0.75;margin-top:4px">⚠ Structural fidelity measures chrome shape identity (roundRects, ellipses, slot counts), not pixel-level visual similarity. Use <code>-browser</code> for pixel comparison.</p>
</header>`)

	// ── score bar ─────────────────────────────────────────────────────────────
	b.WriteString(`<div class="scores">`)
	scoreCard(&b, "Structural (overall)", report.FidelityScore, report.Passed)
	scoreCard(&b, "Chrome shapes (50%)", report.StructureScore, report.StructureScore >= 0.75)
	scoreCard(&b, "Style tokens (30%)", report.StyleScore, report.StyleScore >= 0.75)
	scoreCard(&b, "Content slots (20%)", report.ContentScore, report.ContentScore >= 0.75)
	if hasPNGs {
		avgSim := 0.0
		for _, c := range pngComparisons {
			avgSim += c.Similarity
		}
		avgSim /= float64(len(pngComparisons))
		scoreCard(&b, "Pixel similarity", avgSim, avgSim >= 0.75)
	}
	b.WriteString(`</div>`)

	// ── tabs ──────────────────────────────────────────────────────────────────
	warnBadge := ""
	if len(warnings) > 0 {
		warnBadge = fmt.Sprintf(` <span class="badge badge-fail">%d</span>`, len(warnings))
	}
	firstTab := "slides"
	if hasPNGs {
		firstTab = "png"
	}
	b.WriteString(`<div class="tabs">`)
	if hasPNGs {
		b.WriteString(`<button class="tab on" onclick="showTab('png',this)">PNG Comparison</button>`)
		b.WriteString(`<button class="tab" onclick="showTab('slides',this)">Generated live</button>`)
	} else {
		b.WriteString(`<button class="tab on" onclick="showTab('slides',this)">Generated live</button>`)
	}
	b.WriteString(`<button class="tab" onclick="showTab('catalog',this)">Archetype Catalog</button>`)
	b.WriteString(`<button class="tab" onclick="showTab('gaps',this)">Gap Table</button>`)
	b.WriteString(`<button class="tab" onclick="showTab('fp',this)">Fingerprints</button>`)
	b.WriteString(`<button class="tab" onclick="showTab('warn',this)">Warnings` + warnBadge + `</button>`)
	b.WriteString(`</div>`)
	_ = firstTab

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 0 (when PNG mode): PNG comparison — SOURCE vs GENERATED vs DIFF
	// ═══════════════════════════════════════════════════════════════════════
	pngTabOn := ""
	if hasPNGs {
		pngTabOn = " on"
	}
	b.WriteString(`<div id="pane-png" class="pane` + pngTabOn + `">`)
	if hasPNGs {
		b.WriteString(`<div class="notice">
📷 <strong>Pixel comparison mode</strong> — 4 columns per slide:<br>
<strong>1 SOURCE</strong> = original PPTX slide rendered from its IR (ground truth) ·
<strong>2 ARCHETYPE</strong> = best-matching archetype rendered blank (chrome only) ·
<strong>3 FILLED</strong> = archetype with source text projected in (what Astonish generates) ·
<strong>4 DIFF</strong> = pixel diff of SOURCE vs ARCHETYPE (red = layout gap, score = layout fidelity).
No LibreOffice needed — everything rendered via Astonish's own HTML+Chrome pipeline.
</div>`)
		b.WriteString(`<div class="slides-wrap">`)

		gapsBySlide := map[int][]slides.GapItem{}
		for _, sf := range report.SlideFindings {
			gapsBySlide[sf.SlideIndex] = sf.Gaps
		}

		for i, srcSlide := range tmpl.Model.Slides {
			cmp, hasCmp := pngBySlide[i]
			if !hasCmp {
				continue
			}
			gaps := gapsBySlide[i]

			simClass := "sim-lo"
			if cmp.Similarity >= 0.85 {
				simClass = "sim-hi"
			} else if cmp.Similarity >= 0.65 {
				simClass = "sim-mid"
			}

			genFrameClass := "png-frame gen ok"
			if len(gaps) > 0 {
				worst := "minor"
				for _, g := range gaps {
					if g.Severity == slides.SeverityCritical {
						worst = "critical"
						break
					}
					if g.Severity == slides.SeverityMajor {
						worst = "major"
					}
				}
				switch worst {
				case "critical":
					genFrameClass = "png-frame gen has-crit"
				case "major":
					genFrameClass = "png-frame gen has-major"
				}
			}

			b.WriteString(fmt.Sprintf(`<div class="slide-row">
<div class="slide-header">
		<span class="num">%d</span>
		<span>%s</span>
		<span class="layout-name">→ <strong>%s</strong> · %d gaps</span>
		<span class="sim %s">%.1f%% vs reference</span>
</div>
<div class="slide-cols-3">`,
				i+1,
				gohtml.EscapeString(srcSlide.Name),
				gohtml.EscapeString(cmp.ArchKind),
				len(gaps),
				simClass, cmp.Similarity*100))

			b.WriteString(`<div class="slide-col"><label>Reference (pptx-glimpse)</label>`)
			b.WriteString(`<div class="png-frame src"><img src="` + cmp.SourcePNG + `" alt="reference"></div></div>`)
			b.WriteString(`<div class="slide-col"><label>Filled</label>`)
			if cmp.FilledPNG != "" {
				b.WriteString(`<div class="` + genFrameClass + `"><img src="` + cmp.FilledPNG + `" alt="filled slide"></div>`)
			} else {
				b.WriteString(`<div class="` + genFrameClass + `" style="display:flex;align-items:center;justify-content:center;color:#555;font-size:12px">fill unavailable</div>`)
			}
			b.WriteString(`</div>`)

			b.WriteString(`<div class="slide-col"><label>Pixel diff — reference vs filled</label>`)
			if cmp.DiffPNG != "" {
				b.WriteString(`<div class="png-frame diff"><img src="` + cmp.DiffPNG + `" alt="diff"></div>`)
			} else {
				b.WriteString(`<div class="png-frame diff" style="display:flex;align-items:center;justify-content:center;color:#555;font-size:12px">diff unavailable</div>`)
			}
			b.WriteString(`</div></div>`)

			if len(gaps) == 0 {
				b.WriteString(`<div class="ok-msg" style="padding:8px 16px">No structural gaps</div>`)
			} else {
				b.WriteString(`<div class="gap-list" style="padding:8px 16px">`)
				for _, g := range gaps {
					cls := strings.ToLower(string(g.Severity))
					desc := g.Description
					if g.Expected != "" {
						desc += " · expected: " + g.Expected
					}
					b.WriteString(fmt.Sprintf(`<div class="gap %s"><span class="sev">%s</span><span>%s</span></div>`,
						cls, string(g.Severity), gohtml.EscapeString(clip(desc, 140))))
				}
				b.WriteString(`</div>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`) // slides-wrap
	} else {
		b.WriteString(`<div class="no-rt">No reference thumbnails. Install pptx-glimpse in web/.</div>`)
	}
	b.WriteString(`</div>`) // pane-png

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 1: Live renders — SOURCE IR and GENERATED side by side
	// ═══════════════════════════════════════════════════════════════════════
	liveTabOn := ""
	if !hasPNGs {
		liveTabOn = " on"
	}
	b.WriteString(`<div id="pane-slides" class="pane` + liveTabOn + `">`)
	if !hasRT {
		b.WriteString(`<div class="no-rt">slides-runtime.js not found — rebuild web/ to enable live renders</div>`)
	} else {
		b.WriteString(`<div class="notice">
	<strong>REFERENCE</strong> pptx-glimpse thumbnail. <strong>GENERATED</strong> reconstructed archetype.
	<strong>GENERATED</strong> = archetype filled with original source content via ReconstructScene — this is what Astonish actually produces and is the primary comparison target.
	Both columns use the same source text. Differences in chrome placement indicate archetype gaps. Use <code>-browser</code> for pixel-accurate comparison.
	</div><div class="slides-wrap">`)

		gapsBySlide := map[int][]slides.GapItem{}
		for _, sf := range report.SlideFindings {
			gapsBySlide[sf.SlideIndex] = sf.Gaps
		}

		for i, srcSlide := range tmpl.Model.Slides {
			gaps := gapsBySlide[i]

			genFrameClass := "frame arch-frame ok"
			if len(gaps) > 0 {
				worst := "minor"
				for _, g := range gaps {
					if g.Severity == slides.SeverityCritical {
						worst = "critical"
						break
					}
					if g.Severity == slides.SeverityMajor {
						worst = "major"
					}
				}
				switch worst {
				case "critical":
					genFrameClass = "frame arch-frame has-crit"
				case "major":
					genFrameClass = "frame arch-frame has-major"
				}
			}

			// Source slide → ASD markup from IR.
			// Try to prepend the shared master chrome (MASTER layout objects) so
			// the source column shows the eyebrow, logo etc. that are on every slide.
			genMarkup := ""
			if i < len(recoMarkups) {
				genMarkup = recoMarkups[i]
			}

			b.WriteString(fmt.Sprintf(`<div class="slide-row">
	<div class="slide-header">
		 <span class="num">%d</span>
		 <span>%s</span>
		 <span class="layout-name">%d source shapes · %d gaps</span>
	</div>
	<div class="slide-cols-3">`,
				i+1,
				gohtml.EscapeString(srcSlide.Name),
				len(srcSlide.Objects)+len(srcSlide.Placeholders),
				len(gaps)))

			// Column 1: SOURCE (slide IR only — missing layout/master chrome)
			b.WriteString(`<div class="slide-col"><label>Source (slide IR — no master chrome)</label>`)
			b.WriteString(`<div class="frame src-frame">`)
			b.WriteString(`<div class="slide-col"><label>Reference (pptx-glimpse)</label><div class="png-frame src">`)
			for _, c := range pngComparisons {
				if c.SlideIndex == i && c.SourcePNG != "" {
					b.WriteString(`<img src="` + c.SourcePNG + `" alt="reference" style="position:absolute;inset:0;width:100%;height:100%;object-fit:contain">`)
					break
				}
			}
			b.WriteString(`</div></div>`)
			b.WriteString(`</div></div>`)

			// Column 2: GENERATED (archetype + original source content)
			b.WriteString(`<div class="slide-col"><label>Generated — archetype filled with source content</label>`)
			b.WriteString(`<div class="` + genFrameClass + `">`)
			if genMarkup != "" {
				b.WriteString(renderCanvas(genMarkup, tmpl.Assets, tokenCSS, fmt.Sprintf("gen-%d", i)))
			} else {
				b.WriteString(`<div class="frame-inner" style="display:flex;align-items:center;justify-content:center;color:#555">no reconstruction</div>`)
			}
			b.WriteString(`</div></div>`)

			// Column 3: GAPS
			b.WriteString(`<div class="slide-col"><label>Gaps</label>`)
			if len(gaps) == 0 {
				b.WriteString(`<div class="ok-msg">✓ No gaps detected</div>`)
			} else {
				b.WriteString(`<div class="gap-list">`)
				for _, g := range gaps {
					cls := strings.ToLower(string(g.Severity))
					desc := g.Description
					if g.Expected != "" {
						desc += " · expected: " + g.Expected
					}
					b.WriteString(fmt.Sprintf(`<div class="gap %s"><span class="sev">%s</span><span>%s</span></div>`,
						cls, string(g.Severity), gohtml.EscapeString(clip(desc, 140))))
				}
				b.WriteString(`</div>`)
			}
			b.WriteString(`</div>`)

			b.WriteString(`</div></div>`) // slide-cols-3, slide-row
		}
		b.WriteString(`</div>`) // slides-wrap
	}
	b.WriteString(`</div>`) // pane-slides

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 2: Archetype Catalog — every archetype with live render
	// ═══════════════════════════════════════════════════════════════════════
	b.WriteString(`<div id="pane-catalog" class="pane"><div class="arch-grid">`)
	for idx, a := range tmpl.Archetypes {
		inCat := len(a.FillSlots) >= 2 && !isBookend(a.Kind)
		cardClass := "arch-card out"
		badge := `<span class="badge badge-gray">○ excluded (&lt;2 slots)</span>`
		if isBookend(a.Kind) {
			cardClass = "arch-card bookend"
			badge = `<span class="badge badge-info">⇔ bookend</span>`
		} else if inCat {
			cardClass = "arch-card in"
			badge = `<span class="badge badge-ok">✓ in catalog</span>`
		}
		label := a.Title
		if label == "" {
			label = a.Kind
		}
		b.WriteString(`<div class="` + cardClass + `">`)
		b.WriteString(`<div class="arch-preview">`)
		if hasRT && a.Markup != "" {
			b.WriteString(renderCanvas(a.Markup, tmpl.Assets, tokenCSS, fmt.Sprintf("cat-%d", idx)))
		}
		b.WriteString(`</div>`)
		b.WriteString(`<div class="arch-meta">`)
		b.WriteString(`<h4>` + gohtml.EscapeString(label) + ` ` + badge + `</h4>`)
		b.WriteString(`<div><span class="kind">` + gohtml.EscapeString(a.Kind) + `</span></div>`)
		b.WriteString(fmt.Sprintf(`<div class="slots">%d fill slots`, len(a.FillSlots)))
		if len(a.FillSlots) > 0 {
			b.WriteString(": ")
			for fi, s := range a.FillSlots {
				if fi > 0 {
					b.WriteString(", ")
				}
				if fi >= 4 {
					b.WriteString(fmt.Sprintf("…+%d", len(a.FillSlots)-4))
					break
				}
				b.WriteString(`<code>` + gohtml.EscapeString(s) + `</code>`)
			}
		}
		b.WriteString(`</div></div></div>`)
	}
	b.WriteString(`</div></div>`)

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 3: Gap Table
	// ═══════════════════════════════════════════════════════════════════════
	b.WriteString(`<div id="pane-gaps" class="pane">
<table><thead><tr><th>Slide</th><th>Severity</th><th>Kind</th><th>Description</th><th>Expected</th><th>Actual</th></tr></thead><tbody>`)
	for _, sf := range report.SlideFindings {
		for _, g := range sf.Gaps {
			b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td class="%s" style="font-weight:700">%s</td><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				sf.SlideIndex+1,
				strings.ToLower(string(g.Severity)), string(g.Severity),
				gohtml.EscapeString(string(g.Kind)),
				gohtml.EscapeString(g.Description),
				gohtml.EscapeString(g.Expected),
				gohtml.EscapeString(g.Actual)))
		}
	}
	b.WriteString(`</tbody></table></div>`)

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 4: Fingerprints
	// ═══════════════════════════════════════════════════════════════════════
	b.WriteString(`<div id="pane-fp" class="pane">
<p style="font-size:12px;color:#888;margin-bottom:12px">Visual DNA extracted from source IR — what the reconstruction must reproduce.</p>
<table><thead><tr><th>#</th><th>Name</th><th>BG</th><th>Shapes</th><th>Families</th><th>Accents</th><th>Flags</th>`)
	if hasPNGs {
		b.WriteString(`<th>Pixel Sim</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)
	for i, fp := range report.SlideFingerprints {
		bgSwatch := ""
		if fp.Background != "" {
			bgSwatch = fmt.Sprintf(`<span class="dot" style="background:%s" title="%s"></span>%s`, fp.Background, fp.Background, fp.Background)
		}
		families := ""
		for _, sf := range fp.ShapeFamilies {
			families += fmt.Sprintf(`<code>%s×%d</code> `, sf.Geom, sf.Count)
		}
		accents := ""
		for _, c := range fp.AccentColors {
			accents += fmt.Sprintf(`<span class="dot" style="background:%s" title="%s"></span>`, c, c)
		}
		flags := ""
		if fp.HasRoundRects {
			flags += `<span class="rr">⬡roundRect</span> `
		}
		if fp.HasEllipses {
			flags += `<span class="el">●ellipse</span> `
		}
		if fp.HasStripes {
			flags += `<span class="st">▌stripe</span> `
		}
		if fp.HasTimeline {
			flags += `<span class="tl">─●timeline</span> `
		}
		b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td>`,
			i+1, gohtml.EscapeString(fp.LayoutName), bgSwatch, fp.ChromeShapeCount, families, accents, flags))
		if hasPNGs {
			if cmp, ok := pngBySlide[i]; ok {
				sc := "sim-lo"
				if cmp.Similarity >= 0.85 {
					sc = "sim-hi"
				} else if cmp.Similarity >= 0.65 {
					sc = "sim-mid"
				}
				b.WriteString(fmt.Sprintf(`<td><span class="sim %s">%.1f%%</span></td>`, sc, cmp.Similarity*100))
			} else {
				b.WriteString(`<td>—</td>`)
			}
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div>`)

	// ═══════════════════════════════════════════════════════════════════════
	// TAB 5: Warnings
	// ═══════════════════════════════════════════════════════════════════════
	b.WriteString(`<div id="pane-warn" class="pane">`)
	if len(warnings) == 0 {
		b.WriteString(`<p style="color:#4caf50">No import warnings.</p>`)
	} else {
		b.WriteString(`<table><thead><tr><th>#</th><th>Warning</th></tr></thead><tbody>`)
		for i, w := range warnings {
			b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%s</td></tr>`, i+1, gohtml.EscapeString(w)))
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</div>`)

	// ── JS glue ───────────────────────────────────────────────────────────────
	b.WriteString(`<script>
window.__assets=` + string(assetsJSON) + `;

function showTab(id,btn){
  document.querySelectorAll('.pane').forEach(p=>p.classList.remove('on'));
  document.querySelectorAll('.tab').forEach(t=>t.classList.remove('on'));
  document.getElementById('pane-'+id).classList.add('on');
  btn.classList.add('on');
  scaleDecks();
}
function scaleDecks(){
  document.querySelectorAll('.frame-inner').forEach(wrap=>{
    const deck=wrap.querySelector('ast-deck');
    if(!deck)return;
    const wr=wrap.clientWidth, hr=wrap.clientHeight;
    if(!wr||!hr)return;
    const scale=Math.min(wr/1920,hr/1080);
    deck.style.transform='scale('+scale+')';
    deck.style.transformOrigin='top left';
    deck.style.left=Math.max(0,(wr-1920*scale)/2)+'px';
    deck.style.top=Math.max(0,(hr-1080*scale)/2)+'px';
  });
}
function injectAssets(){
  const a=window.__assets||{};
  document.querySelectorAll('ast-image[asset-ref]').forEach(el=>{
    const ref=el.getAttribute('asset-ref');
    if(ref&&a[ref])el.setAttribute('src',a[ref]);
  });
}
if(typeof customElements!=='undefined'){
  customElements.whenDefined('ast-deck').then(()=>{
    setTimeout(()=>{scaleDecks();injectAssets();},100);
  });
}
window.addEventListener('resize',scaleDecks);
</script></body></html>`)

	return b.String()
}

// ─── canvas helpers ───────────────────────────────────────────────────────────

func renderCanvas(markup string, assets map[string]string, tokenCSS, id string) string {
	// Patch image asset-refs to inline data: URIs
	for ref, data := range assets {
		if strings.HasPrefix(data, "data:image/") {
			markup = strings.ReplaceAll(markup,
				`asset-ref="`+ref+`"`,
				`asset-ref="`+ref+`" src="`+data+`"`)
		}
	}
	// Activate the first ast-slide
	markup = strings.Replace(markup, "<ast-slide", `<ast-slide active`, 1)

	return `<div class="frame-inner" id="` + gohtml.EscapeString(id) + `">` +
		`<ast-deck style="` + tokenCSS + `">` + markup + `</ast-deck>` +
		`</div>`
}

// ─── small helpers ────────────────────────────────────────────────────────────

func buildTokenCSS(tokens map[string]string) string {
	var parts []string
	for k, v := range tokens {
		parts = append(parts, "--ast-"+k+":"+v)
	}
	return strings.Join(parts, ";")
}

func scoreCard(b *strings.Builder, label string, score float64, ok bool) {
	cls := "pass"
	if !ok {
		cls = "fail"
	}
	b.WriteString(fmt.Sprintf(
		`<div class="sc"><div class="v %s">%.1f%%</div><div class="l">%s</div></div>`,
		cls, score*100, gohtml.EscapeString(label)))
}

func isBookend(kind string) bool {
	base := kind
	if i := strings.LastIndexByte(kind, '-'); i > 0 {
		if _, err := strconv.Atoi(kind[i+1:]); err == nil {
			base = kind[:i]
		}
	}
	return base == "title" || base == "closing"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func loadRuntime(webDir string) (string, error) {
	for _, p := range []string{
		filepath.Join(webDir, "dist", "2.4.0", "slides-runtime.js"),
		filepath.Join(webDir, "dist", "slides-runtime.js"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("slides-runtime.js not found under %s/dist/", webDir)
}

func resolveWorkerPaths() (workingDir, scriptPath string) {
	roots := []string{}
	if root := os.Getenv("ASTONISH_ROOT"); root != "" {
		roots = append(roots, root)
	}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		wd := filepath.Join(root, "web")
		sp := filepath.Join(root, "pkg", "docs", "slides", "pptxworker", "import_worker.mjs")
		if info, err := os.Stat(wd); err == nil && info.IsDir() {
			if _, err := os.Stat(sp); err == nil {
				return wd, sp
			}
		}
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "web"),
		filepath.Join(cwd, "pkg", "docs", "slides", "pptxworker", "import_worker.mjs")
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "slides-diag: "+f+"\n", a...)
	os.Exit(1)
}
