package slides

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// ImportLoopOptions configures the iterative import process.
type ImportLoopOptions struct {
	// MaxIterations is the maximum number of reconstruct→compare→repair cycles.
	// When 0 a default of 3 is used.
	MaxIterations int
	// WorkingDir is the directory passed to pptxworker.ImportRunner.
	WorkingDir string
	// ScriptPath is the path passed to pptxworker.ImportRunner.
	ScriptPath string
}

// importWorkerFn is the function used to invoke the PPTX import worker.
// It is a package-level variable so tests can replace it with a stub.
var importWorkerFn = defaultImportWorkerFn

// defaultImportWorkerFn delegates to a real pptxworker.ImportRunner.
func defaultImportWorkerFn(ctx context.Context, opts ImportLoopOptions, b64 string) (themes.Template, error) {
	runner := pptxworker.ImportRunner{
		WorkingDir: opts.WorkingDir,
		ScriptPath: opts.ScriptPath,
	}
	resp, err := runner.Run(ctx, pptxworker.ImportRequest{PPTXBase64: b64, Mode: "template"})
	if err != nil {
		return themes.Template{}, err
	}
	var tmpl themes.Template
	if err := json.Unmarshal(resp.SceneOrTemplate, &tmpl); err != nil {
		return themes.Template{}, fmt.Errorf("decode import worker template: %w", err)
	}
	return tmpl, nil
}

// RunImportLoop runs the full capture → reconstruct → compare → LLM-repair
// cycle for a PPTX template import.
//
// The JS worker is called exactly once (on the first iteration). Subsequent
// iterations apply LLM-proposed archetype patches and re-run reconstruction
// and comparison without re-parsing the OOXML. This makes repair fast and
// keeps the worker's job focused on faithful OOXML extraction.
//
// llm may be nil; when nil, a single import pass is run without LLM repair.
// Returns the best Template seen, the final CompareReport, and any hard error.
// A hard error (worker failure) sets ImportState = "failed".
// Exhausting all iterations without passing sets ImportState = "validation_incomplete".
// Passing sets ImportState = "validated".
func RunImportLoop(ctx context.Context, pptxBase64 string, opts ImportLoopOptions, llm model.LLM) (themes.Template, CompareReport, error) {
	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = 3
	}

	// ---- Iteration 1: run the JS worker to get the initial template ----------
	tmpl, err := importWorkerFn(ctx, opts, pptxBase64)
	if err != nil {
		tmpl.ImportState = "failed"
		return tmpl, CompareReport{}, fmt.Errorf("import worker: %w", err)
	}

	var finalReport CompareReport
	var repairsApplied []string

	for iter := 1; iter <= maxIter; iter++ {
		// Reconstruct a deck scene from the current template + archetypes.
		var assets map[string]string
		if tmpl.Assets != nil {
			assets = tmpl.Assets
		}
		scene, warnings, reconstructErr := ReconstructScene(tmpl.Model, tmpl.Archetypes, assets)
		if reconstructErr != nil {
			// A reconstruction failure is non-retryable.
			tmpl.ImportState = "failed"
			return tmpl, CompareReport{}, fmt.Errorf("reconstruct (iter %d): %w", iter, reconstructErr)
		}

		// Compare source model against the reconstruction.
		report := Compare(tmpl.Model, scene)
		finalReport = report

		// Build a gap summary (top 5 most severe gaps) for the history entry.
		gapSummary := topGapDescriptions(report, 5)

		// Append iteration result to the model's ImportHistory.
		if tmpl.Model != nil {
			tmpl.Model.ImportHistory = append(tmpl.Model.ImportHistory, themes.IterationResult{
				Iteration:     iter,
				Archetypes:    append([]themes.Archetype(nil), tmpl.Archetypes...),
				FidelityScore: report.FidelityScore,
				GapSummary:    gapSummary,
			})
		}

		tmpl.ImportIterations = iter

		if report.Passed {
			tmpl.ImportState = "validated"
			// Build the evidence-grounded style guide before returning.
			if tmpl.Model != nil {
				evidence := buildImportEvidence(finalReport, repairsApplied, warnings, iter)
				tmpl.StyleGuide = themes.GenerateStyleGuideFromEvidence(tmpl.Model, tmpl.Tokens, tmpl.Archetypes, evidence)
				tmpl.Model.StyleGuide = tmpl.StyleGuide
			}
			// Phase 1: store the import proof deck so the UI can render it.
			if proof := GenerateImportProofDeck(tmpl); proof != nil {
				if err := StoreImportProofDeck(&tmpl, proof); err != nil {
					slog.Warn("import proof deck: failed to store", "error", err)
				}
			}
			return tmpl, report, nil
		}

		// If we have more iterations AND an LLM, attempt a repair pass.
		if iter < maxIter && llm != nil {
			patched, repairErr := llmRepairPass(ctx, llm, report, tmpl.Archetypes)
			if repairErr != nil {
				// Repair errors are soft — log and continue to next iteration
				// with unmodified archetypes (or bail on the last attempt).
				slog.Warn("llmRepairPass error", "iter", iter, "error", repairErr)
			} else {
				tmpl.Archetypes = patched
				// Accumulate gap descriptions as "repairs applied" evidence.
				repairsApplied = append(repairsApplied, gapSummary...)
			}
		}
	}

	// All iterations exhausted without passing.
	tmpl.ImportState = "validation_incomplete"
	// Build the evidence-grounded style guide with the best evidence we have.
	if tmpl.Model != nil {
		evidence := buildImportEvidence(finalReport, repairsApplied, nil, tmpl.ImportIterations)
		tmpl.StyleGuide = themes.GenerateStyleGuideFromEvidence(tmpl.Model, tmpl.Tokens, tmpl.Archetypes, evidence)
		tmpl.Model.StyleGuide = tmpl.StyleGuide
	}
	// Phase 1: always store the proof deck, even on incomplete validation, so
	// a reviewer can see what was captured and decide whether to repair further.
	if proof := GenerateImportProofDeck(tmpl); proof != nil {
		if err := StoreImportProofDeck(&tmpl, proof); err != nil {
			slog.Warn("import proof deck: failed to store", "error", err)
		}
	}
	return tmpl, finalReport, nil
}

// buildImportEvidence constructs a themes.ImportEvidence from the final
// CompareReport, accumulated repairs, reconstruction warnings, and iteration count.
func buildImportEvidence(report CompareReport, repairsApplied []string, warnings []string, iterations int) themes.ImportEvidence {
	return themes.ImportEvidence{
		FidelityScore:         report.FidelityScore,
		Passed:                report.Passed,
		GapDescriptions:       collectGapDescriptions(report),
		RepairsApplied:        repairsApplied,
		UnsupportedConstructs: warnings,
		Iterations:            iterations,
	}
}

// collectGapDescriptions flattens the top-10 gap descriptions from a report.
func collectGapDescriptions(report CompareReport) []string {
	gaps := flattenGapsBySeverity(report, 10)
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.item.Description)
	}
	return out
}

// llmRepairPatch is the shape of one element in the LLM repair JSON response.
type llmRepairPatch struct {
	Kind   string `json:"kind"`
	Markup string `json:"markup"`
}

// llmRepairPass sends the gap report and current archetype markup to the LLM
// and receives targeted patches. It returns the (possibly modified) archetypes
// slice. Only patches whose markup passes ParseSlide validation are applied.
//
// The repair prompt includes the source IR visual fingerprint for each affected
// slide so the LLM knows exactly which shapes, geom families, and fill colors
// are expected but missing — rather than relying on abstract gap descriptions.
func llmRepairPass(ctx context.Context, llm model.LLM, report CompareReport, archetypes []themes.Archetype) ([]themes.Archetype, error) {
	if llm == nil {
		return archetypes, nil
	}

	// Collect the top-8 gaps by severity for the prompt (more context for visual gaps).
	topGaps := flattenGapsBySeverity(report, 8)

	// Build set of archetype kinds that appear in the gap list.
	affectedKinds := map[string]bool{}
	for _, g := range topGaps {
		if g.item.ArchetypeKind != "" {
			affectedKinds[g.item.ArchetypeKind] = true
		}
	}

	// Build index of source fingerprints for affected slides.
	affectedSlides := map[int]bool{}
	for _, g := range topGaps {
		affectedSlides[g.slideIndex] = true
	}

	// Build the repair prompt.
	var sb strings.Builder
	sb.WriteString("You are repairing ASD v2 slide archetypes to faithfully reproduce a PowerPoint template's visual structure.\n")
	sb.WriteString("The ASD markup uses <ast-slide>, <ast-shape>, <ast-text>, <ast-image> elements on a 1920×1080 canvas.\n")
	sb.WriteString("Return ONLY a JSON array — no prose, no markdown fences.\n\n")

	// Section 1: source IR visual fingerprints for affected slides.
	if len(report.SlideFingerprints) > 0 {
		sb.WriteString("## Source template visual structure (what the archetype MUST reproduce)\n\n")
		for _, fp := range report.SlideFingerprints {
			if !affectedSlides[fp.SlideIndex] {
				continue
			}
			sb.WriteString(fmt.Sprintf("### Slide %d", fp.SlideIndex+1))
			if fp.LayoutName != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", fp.LayoutName))
			}
			sb.WriteString("\n")
			if fp.Background != "" {
				sb.WriteString(fmt.Sprintf("- Background: %s\n", fp.Background))
			}
			if fp.ChromeShapeCount > 0 {
				sb.WriteString(fmt.Sprintf("- Chrome shapes: %d total\n", fp.ChromeShapeCount))
			}
			if fp.HasRoundRects {
				sb.WriteString("- ⚠️  Has roundRect card shapes (REQUIRED in archetype)\n")
			}
			if fp.HasStripes {
				sb.WriteString("- ⚠️  Has narrow stripe/accent bar shapes (left or top border accents — REQUIRED)\n")
			}
			if fp.HasEllipses {
				sb.WriteString("- ⚠️  Has ellipse/circle marker shapes (REQUIRED in archetype)\n")
			}
			if fp.HasTimeline {
				sb.WriteString("- ⚠️  Has timeline rail (horizontal line + small dots — REQUIRED)\n")
			}
			if len(fp.AccentColors) > 0 {
				sb.WriteString(fmt.Sprintf("- Accent fill colors (ALL must appear on shapes): %s\n", strings.Join(fp.AccentColors, ", ")))
			}
			if len(fp.ShapeFamilies) > 0 {
				sb.WriteString("- Shape families:\n")
				for _, sf := range fp.ShapeFamilies {
					line := fmt.Sprintf("  - %s (×%d)", sf.Geom, sf.Count)
					if len(sf.Fills) > 0 {
						line += " fills: " + strings.Join(sf.Fills, ", ")
					}
					sb.WriteString(line + "\n")
				}
			}
			sb.WriteString("\n")
		}
	}

	// Section 2: top gaps.
	sb.WriteString("## Gaps to fix\n\n")
	for i, g := range topGaps {
		sb.WriteString(fmt.Sprintf("%d. [slide %d] %s severity=%s: %s",
			i+1, g.slideIndex+1, string(g.item.Kind), string(g.item.Severity), g.item.Description))
		if g.item.Expected != "" {
			sb.WriteString(fmt.Sprintf(" (expected: %s", g.item.Expected))
			if g.item.Actual != "" {
				sb.WriteString(fmt.Sprintf(", actual: %s", g.item.Actual))
			}
			sb.WriteString(")")
		}
		if g.item.ArchetypeKind != "" {
			sb.WriteString(fmt.Sprintf(" [archetype: %s]", g.item.ArchetypeKind))
		}
		sb.WriteString("\n")
	}

	// Section 3: current archetype markup to repair.
	sb.WriteString("\n## Current archetype markup to repair\n\n")
	for _, arch := range archetypes {
		if len(affectedKinds) > 0 && !affectedKinds[arch.Kind] {
			continue
		}
		markup := arch.Markup
		if len(markup) > 4000 {
			markup = markup[:4000] + "…(truncated)"
		}
		sb.WriteString(fmt.Sprintf("### kind: %s\n```xml\n%s\n```\n\n", arch.Kind, markup))
	}

	// Section 4: ASD markup rules.
	sb.WriteString("## ASD v2 markup rules\n\n")
	sb.WriteString("- Canvas: 1920×1080 logical pixels. All x/y/w/h are integers.\n")
	sb.WriteString("- `<ast-shape kind=\"rect\">` for rectangles; add `geom=\"roundRect\"` for rounded cards.\n")
	sb.WriteString("- `<ast-shape kind=\"ellipse\" geom=\"ellipse\">` for circles.\n")
	sb.WriteString("- `<ast-shape kind=\"line\" geom=\"line\">` for horizontal/vertical lines.\n")
	sb.WriteString("- `fill=\"#RRGGBB\"` on ast-shape for solid fills; `line=\"#RRGGBB\"` for strokes.\n")
	sb.WriteString("- `decorative=\"true\"` on pure chrome shapes that have no alt text.\n")
	sb.WriteString("- Fill slots use id=\"ph-N\" and contain `<ast-run>{{TITLE}}</ast-run>` or `<ast-run>{{BODY}}</ast-run>`.\n")
	sb.WriteString("- Do NOT remove existing fill slots (ph-1, ph-2, etc.) — only add or correct chrome shapes.\n\n")

	sb.WriteString("## Response format\n\n")
	sb.WriteString("Return a JSON array where each element fixes one archetype:\n")
	sb.WriteString("```json\n")
	sb.WriteString(`[{"kind":"<archetype-kind>","markup":"<full corrected ast-slide markup>"}]`)
	sb.WriteString("\n```\n")
	sb.WriteString("Only include archetypes you are changing. The markup must be a complete, valid ast-slide element.\n")

	prompt := sb.String()

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{{
			Role:  "user",
			Parts: []*genai.Part{genai.NewPartFromText(prompt)},
		}},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.2)),
		},
	}

	var respBuilder strings.Builder
	for resp, err := range llm.GenerateContent(ctx, llmReq, true) {
		if err != nil {
			return archetypes, fmt.Errorf("llm repair: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.Text != "" {
					respBuilder.WriteString(part.Text)
				}
			}
		}
	}

	raw := strings.TrimSpace(respBuilder.String())
	if raw == "" {
		return archetypes, fmt.Errorf("llm repair: empty response")
	}

	// Strip markdown code fences if present.
	raw = stripJSONFence(raw)

	var patches []llmRepairPatch
	if err := json.Unmarshal([]byte(raw), &patches); err != nil {
		slog.Warn("llmRepairPass: failed to parse LLM JSON response",
			"error", err, "prefix", truncate(raw, 200))
		return archetypes, fmt.Errorf("llm repair: unmarshal patches: %w", err)
	}

	// Build a mutable copy and apply only valid patches.
	result := append([]themes.Archetype(nil), archetypes...)
	for _, patch := range patches {
		_, diags, err := ParseSlide(patch.Markup)
		if err != nil {
			slog.Warn("llmRepairPass: patch markup failed ParseSlide",
				"kind", patch.Kind, "error", err)
			continue
		}
		hasErrors := false
		for _, d := range diags {
			if d.Severity == "error" {
				hasErrors = true
				break
			}
		}
		if hasErrors {
			slog.Warn("llmRepairPass: patch markup has parse diagnostics errors, skipping",
				"kind", patch.Kind)
			continue
		}
		// Apply the patch to the matching archetype.
		for i := range result {
			if result[i].Kind == patch.Kind {
				result[i].Markup = patch.Markup
				slog.Info("llmRepairPass: applied patch", "kind", patch.Kind)
				break
			}
		}
	}

	return result, nil
}

// -------------------------------------------------------------------------
// Internal helpers
// -------------------------------------------------------------------------

type indexedGap struct {
	slideIndex int
	item       GapItem
}

// flattenGapsBySeverity flattens all slide findings into a sorted slice,
// ordered Critical → Major → Minor, and returns at most n entries.
func flattenGapsBySeverity(report CompareReport, n int) []indexedGap {
	var all []indexedGap
	for _, sf := range report.SlideFindings {
		for _, g := range sf.Gaps {
			all = append(all, indexedGap{slideIndex: sf.SlideIndex, item: g})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return severityRank(all[i].item.Severity) < severityRank(all[j].item.Severity)
	})
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

// topGapDescriptions returns the descriptions of the top n gaps.
func topGapDescriptions(report CompareReport, n int) []string {
	gaps := flattenGapsBySeverity(report, n)
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.item.Description)
	}
	return out
}

// severityRank maps Severity to a sort key (lower = more severe).
func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityMajor:
		return 1
	default:
		return 2
	}
}

// stripJSONFence removes markdown code fences (```json … ``` or ``` … ```)
// that an LLM sometimes wraps around its JSON output.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			if idx := strings.LastIndex(s, "```"); idx >= 0 {
				s = s[:idx]
			}
			return strings.TrimSpace(s)
		}
	}
	return s
}
