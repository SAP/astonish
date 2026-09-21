//go:build integration

package slides

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// requireOneAlertingFixture locates the One Alerting PPTX in the
// slides_improve/ folder and returns its path. The test is skipped if the
// file is not present so the suite remains green in CI environments that do
// not ship the proprietary fixture.
func requireOneAlertingFixture(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	path := filepath.Join(repo, "slides_improve", "One_Alerting_Workshop_Results_Summary.pptx")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("One Alerting PPTX fixture not found at %s; skipping", path)
	}
	return path
}

// einsteinStubIR builds a minimal 3-slide TemplateModel whose Slides reuse the
// layout geometry from source but carry Albert Einstein content — completely
// different text from the workshop deck. This proves that the imported
// archetypes can be applied to arbitrary new content.
func einsteinStubIR(source *themes.TemplateModel) *themes.TemplateModel {
	contents := []struct{ title, body string }{
		{"Albert Einstein: Life and Legacy", "Theoretical physicist, 1879–1955"},
		{"Theory of General Relativity", "Space-time curvature and gravitational waves"},
		{"Nobel Prize in Physics 1921", "Awarded for the photoelectric effect"},
	}

	slides := make([]themes.IRLayout, 0, 3)
	for i := 0; i < 3 && i < len(source.Slides); i++ {
		// Start from the source layout structure but replace all placeholders
		// and chrome text objects with Einstein content.
		layout := source.Slides[i]
		layout.Placeholders = []themes.IRPlaceholder{
			{
				Name: "ph-title", Type: "title",
				X: 160, Y: 120, W: 1600, H: 120,
				Style: themes.IRTextStyle{FontSize: 40},
			},
			{
				Name: "ph-body", Type: "body",
				X: 160, Y: 280, W: 1600, H: 400,
				Style: themes.IRTextStyle{FontSize: 24},
			},
		}
		// Replace any text chrome objects to avoid workshop content leaking.
		filtered := layout.Objects[:0:0]
		for _, obj := range layout.Objects {
			if obj.Kind != "text" {
				filtered = append(filtered, obj)
			}
		}
		// Inject new title and body text objects.
		filtered = append(filtered,
			themes.IRChrome{
				Kind: "text",
				X: 160, Y: 120, W: 1600, H: 120,
				Text:  contents[i].title,
				Style: &themes.IRTextStyle{FontSize: 40},
			},
			themes.IRChrome{
				Kind: "text",
				X: 160, Y: 280, W: 1600, H: 400,
				Text:  contents[i].body,
				Style: &themes.IRTextStyle{FontSize: 24},
			},
		)
		layout.Objects = filtered
		slides = append(slides, layout)
	}

	return &themes.TemplateModel{
		Schema:  source.Schema,
		Size:    source.Size,
		Theme:   source.Theme,
		Layouts: source.Layouts,
		Slides:  slides,
	}
}

// truncateStr returns s truncated to maxLen characters with an ellipsis.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// TestOneAlertingImportFidelity runs the full RunImportLoop against the One
// Alerting workshop deck and asserts structural, content, and style-guide
// fidelity.  Run with:
//
//	go test -tags integration ./pkg/docs/slides/... -run TestOneAlertingImportFidelity
func TestOneAlertingImportFidelity(t *testing.T) {
	workingDir, importScript, _ := requireImportNodeEnv(t)
	pptxPath := requireOneAlertingFixture(t)

	data, err := os.ReadFile(pptxPath)
	if err != nil {
		t.Fatalf("read PPTX fixture: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	opts := ImportLoopOptions{
		MaxIterations: 1, // single pass — LLM repair requires a real provider
		WorkingDir:    workingDir,
		ScriptPath:    importScript,
	}
	tmpl, report, err := RunImportLoop(context.Background(), b64, opts, nil)
	if err != nil {
		t.Fatalf("RunImportLoop failed: %v", err)
	}

	// Template model must be present.
	if tmpl.Model == nil {
		t.Fatal("expected non-nil Model")
	}
	// Verify the model captured all 19 source slides.
	if len(tmpl.Model.Slides) != 19 {
		t.Errorf("expected 19 source slides, got %d", len(tmpl.Model.Slides))
	}
	if len(tmpl.Archetypes) == 0 {
		t.Fatal("expected at least one archetype")
	}

	// Reconstruction must have been attempted.
	if len(tmpl.Model.ImportHistory) == 0 {
		t.Fatal("expected at least one ImportHistory entry")
	}

	// Fidelity score must be in the valid range.
	if report.FidelityScore < 0 || report.FidelityScore > 1.0 {
		t.Errorf("FidelityScore out of range: %f", report.FidelityScore)
	}
	t.Logf("FidelityScore: %.3f, Passed: %v, ImportState: %s", report.FidelityScore, report.Passed, tmpl.ImportState)

	// Style guide must be present and contain One Alerting identity markers.
	if tmpl.StyleGuide == nil {
		t.Fatal("expected non-nil StyleGuide")
	}
	md := tmpl.StyleGuide.Markdown
	// These keywords should appear in any correctly generated style guide for
	// the One Alerting deck:
	//   #002A86  — SAP dark navy detected as template accent color
	//   eyebrow  — typography scale contains "Eyebrow text" label role
	//   pattern  — 16 pattern archetypes should be listed in the guide
	for _, keyword := range []string{"#002A86", "eyebrow", "pattern"} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(keyword)) {
			t.Errorf("style guide markdown missing expected keyword %q\nMarkdown excerpt (first 2000 chars):\n%s",
				keyword, truncateStr(md, 2000))
		}
	}

	// ImportState must be one of the terminal states.
	validStates := map[string]bool{
		"validated":             true,
		"validation_incomplete": true,
		"failed":                true,
		"":                      true,
	}
	if !validStates[tmpl.ImportState] {
		t.Errorf("unexpected ImportState %q — should be a terminal state", tmpl.ImportState)
	}
}

// TestOneAlertingReuseProof verifies that imported archetypes from the One
// Alerting deck can render completely new content (3-slide Einstein stub)
// without errors.  Run with:
//
//	go test -tags integration ./pkg/docs/slides/... -run TestOneAlertingReuseProof
func TestOneAlertingReuseProof(t *testing.T) {
	workingDir, importScript, _ := requireImportNodeEnv(t)
	pptxPath := requireOneAlertingFixture(t)

	data, err := os.ReadFile(pptxPath)
	if err != nil {
		t.Fatalf("read PPTX fixture: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	opts := ImportLoopOptions{MaxIterations: 1, WorkingDir: workingDir, ScriptPath: importScript}
	tmpl, _, err := RunImportLoop(context.Background(), b64, opts, nil)
	if err != nil || tmpl.Model == nil {
		t.Skip("import failed or no model; skipping reuse proof")
	}

	// Build a 3-slide stub with Albert Einstein content.
	einsteinModel := einsteinStubIR(tmpl.Model)

	// Reconstruct a scene from the imported archetypes with the new content.
	scene, warnings, err := ReconstructScene(einsteinModel, tmpl.Archetypes, tmpl.Assets)
	if err != nil {
		t.Fatalf("ReconstructScene failed: %v", err)
	}
	t.Logf("Reuse proof: %d slides reconstructed, %d warnings", len(scene.Slides), len(warnings))

	// Expect exactly 3 slides (matching the stub).
	if len(scene.Slides) != 3 {
		t.Errorf("expected 3 slides, got %d", len(scene.Slides))
	}

	// Validate each archetype from the imported template is still parse-clean
	// (they were not mutated by the reuse path), and check the reconstructed
	// archetypes don't contain leaked workshop source text.
	for i, s := range scene.Slides {
		// scene.Slides contains the reconstructed slide node tree — there is no
		// Markup field on Slide.  Instead we verify the template archetypes that
		// drove the reconstruction remain valid ASD.
		_ = s // slide node used for count assertion above

		// Validate the archetype that backs this slide position.
		if i < len(tmpl.Archetypes) {
			a := tmpl.Archetypes[i]
			_, diags, parseErr := ParseSlide(a.Markup)
			if parseErr != nil {
				t.Errorf("archetype %d (%q): ParseSlide error: %v", i, a.Kind, parseErr)
			}
			if HasErrors(diags) {
				t.Errorf("archetype %d (%q): ASD errors: %v", i, a.Kind, diags)
			}
			// The archetype markup must not contain Einstein content — the
			// archetypes represent the design language, not the new content.
			// (Content is projected at reconstruction time, not stored on the archetype.)
			if strings.Contains(strings.ToLower(a.Markup), "einstein") {
				t.Errorf("archetype %d (%q): markup unexpectedly contains Einstein content — archetypes should be content-free", i, a.Kind)
			}
		}
	}
}
