package slides

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/pptxworker"
	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// -------------------------------------------------------------------------
// Mock LLM (mirrors pkg/api/mock_llm_test.go pattern)
// -------------------------------------------------------------------------

type slidesTestMockLLM struct {
	mu    sync.Mutex
	turns []string // pre-programmed text responses in order
	index int
	Calls []*model.LLMRequest
}

func newSlidesTestMockLLM(responses ...string) *slidesTestMockLLM {
	return &slidesTestMockLLM{turns: responses}
}

func (m *slidesTestMockLLM) Name() string { return "slides_test_mock_llm" }

func (m *slidesTestMockLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.Calls = append(m.Calls, req)
	if m.index >= len(m.turns) {
		m.mu.Unlock()
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, fmt.Errorf("mock LLM: no more turns (index=%d)", m.index))
		}
	}
	text := m.turns[m.index]
	m.index++
	m.mu.Unlock()

	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: text}},
			},
			TurnComplete: true,
		}, nil)
	}
}

var _ model.LLM = (*slidesTestMockLLM)(nil)

// -------------------------------------------------------------------------
// Stub worker helpers
// -------------------------------------------------------------------------

// minimalTemplate builds a minimal themes.Template as if produced by the JS worker.
// model carries a single slide so reconstruction can run.
func minimalValidTemplate() themes.Template {
	model := &themes.TemplateModel{
		Schema: themes.SchemaModelV3,
		Size:   themes.IRSize{W: 1920, H: 1080},
		Slides: []themes.IRLayout{
			{
				ID:   "slide-1",
				Name: "Title Slide",
				Background: themes.IRBackground{
					Kind:  "solid",
					Color: "#FFFFFF",
				},
				Placeholders: []themes.IRPlaceholder{
					{Name: "title", Type: "title", X: 100, Y: 100, W: 800, H: 100, Prompt: "Title"},
				},
			},
		},
	}
	// A minimal valid archetype the slide can be reconstructed from.
	archMarkup := `<ast-slide id="title">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" alt="" decorative="true"></ast-shape>` +
		`<ast-text id="title-heading" x="160" y="380" w="1600" h="220" color="#172033" weight="bold" size="80" align="center">{{TITLE}}</ast-text>` +
		`</ast-slide>`

	return themes.Template{
		Schema: 3,
		Model:  model,
		Archetypes: []themes.Archetype{
			{Kind: "title", Markup: archMarkup, FillSlots: []string{"title-heading"}},
		},
	}
}

// -------------------------------------------------------------------------
// Test 1: RunImportLoopNoLLMSinglePass
// -------------------------------------------------------------------------

// TestRunImportLoopNoLLMSinglePass verifies that when llm is nil, exactly one
// import pass is run and ImportState is set correctly based on the fidelity score.
func TestRunImportLoopNoLLMSinglePass(t *testing.T) {
	origFn := importWorkerFn
	t.Cleanup(func() { importWorkerFn = origFn })

	stub := minimalValidTemplate()
	importWorkerFn = func(_ context.Context, _ ImportLoopOptions, _ string) (themes.Template, error) {
		return stub, nil
	}

	opts := ImportLoopOptions{MaxIterations: 1}
	tmpl, report, err := RunImportLoop(context.Background(), "fake-b64", opts, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ImportIterations must be exactly 1.
	if tmpl.ImportIterations != 1 {
		t.Errorf("ImportIterations = %d, want 1", tmpl.ImportIterations)
	}

	// ImportState must be consistent with the FidelityScore.
	if report.Passed {
		if tmpl.ImportState != "validated" {
			t.Errorf("report.Passed=true but ImportState = %q, want \"validated\"", tmpl.ImportState)
		}
	} else {
		if tmpl.ImportState != "validation_incomplete" {
			t.Errorf("report.Passed=false but ImportState = %q, want \"validation_incomplete\"", tmpl.ImportState)
		}
	}

	// ImportHistory must have one entry.
	if tmpl.Model == nil {
		t.Fatal("tmpl.Model is nil")
	}
	if len(tmpl.Model.ImportHistory) != 1 {
		t.Errorf("len(ImportHistory) = %d, want 1", len(tmpl.Model.ImportHistory))
	}
	if len(tmpl.Model.ImportHistory) > 0 && tmpl.Model.ImportHistory[0].Iteration != 1 {
		t.Errorf("ImportHistory[0].Iteration = %d, want 1", tmpl.Model.ImportHistory[0].Iteration)
	}
}

// -------------------------------------------------------------------------
// Test 2: TestLLMRepairPassPatchesArchetype
// -------------------------------------------------------------------------

// TestLLMRepairPassPatchesArchetype verifies that a valid LLM response patches
// the matching archetype markup in-place.
func TestLLMRepairPassPatchesArchetype(t *testing.T) {
	// Build a minimal compare report with one Major gap on a content archetype.
	report := CompareReport{
		FidelityScore: 0.5,
		Passed:        false,
		SlideFindings: []SlideFinding{
			{
				SlideIndex: 0,
				Gaps: []GapItem{
					{
						Kind:          GapWrongBackground,
						Severity:      SeverityMajor,
						Description:   "background color mismatch",
						ArchetypeKind: "content",
						Expected:      "#002A86",
						Actual:        "#FFFFFF",
					},
				},
			},
		},
	}

	// Prepare original archetypes.
	origMarkup := `<ast-slide id="content">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" alt="" decorative="true"></ast-shape>` +
		`</ast-slide>`
	patchedMarkup := `<ast-slide id="content">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#002A86" alt="" decorative="true"></ast-shape>` +
		`</ast-slide>`

	archetypes := []themes.Archetype{
		{Kind: "content", Markup: origMarkup},
	}

	// The mock LLM returns a JSON array with one patch.
	patch := []map[string]string{{"kind": "content", "markup": patchedMarkup}}
	patchJSON, _ := json.Marshal(patch)

	llm := newSlidesTestMockLLM(string(patchJSON))

	result, err := llmRepairPass(context.Background(), llm, report, archetypes)
	if err != nil {
		t.Fatalf("llmRepairPass returned error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Markup != patchedMarkup {
		t.Errorf("archetype markup not patched\ngot:  %q\nwant: %q", result[0].Markup, patchedMarkup)
	}
	if len(llm.Calls) != 1 {
		t.Errorf("LLM was called %d times, want 1", len(llm.Calls))
	}
}

// -------------------------------------------------------------------------
// Test 3: TestLLMRepairPassIgnoresMalformedJSON
// -------------------------------------------------------------------------

// TestLLMRepairPassIgnoresMalformedJSON verifies that a malformed LLM JSON
// response returns archetypes unchanged and does not panic.
func TestLLMRepairPassIgnoresMalformedJSON(t *testing.T) {
	report := CompareReport{
		FidelityScore: 0.4,
		Passed:        false,
		SlideFindings: []SlideFinding{
			{
				SlideIndex: 0,
				Gaps: []GapItem{
					{Kind: GapWrongBackground, Severity: SeverityMajor, Description: "bad bg"},
				},
			},
		},
	}

	origMarkup := `<ast-slide id="title">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" alt="" decorative="true"></ast-shape>` +
		`</ast-slide>`
	archetypes := []themes.Archetype{{Kind: "title", Markup: origMarkup}}

	// LLM returns invalid JSON.
	llm := newSlidesTestMockLLM("this is not valid JSON at all {{{")

	result, err := llmRepairPass(context.Background(), llm, report, archetypes)

	// The function should return an error but NOT panic.
	if err == nil {
		t.Error("expected an error for malformed JSON response, got nil")
	}

	// Archetypes must be returned unchanged.
	if len(result) != len(archetypes) {
		t.Fatalf("len(result) = %d, want %d", len(result), len(archetypes))
	}
	if result[0].Markup != origMarkup {
		t.Errorf("archetype markup changed unexpectedly")
	}
}

// -------------------------------------------------------------------------
// Test 4: TestRunImportLoopWorkerError
// -------------------------------------------------------------------------

// TestRunImportLoopWorkerError verifies that a worker failure sets
// ImportState = "failed" and returns the error.
func TestRunImportLoopWorkerError(t *testing.T) {
	origFn := importWorkerFn
	t.Cleanup(func() { importWorkerFn = origFn })

	importWorkerFn = func(_ context.Context, _ ImportLoopOptions, _ string) (themes.Template, error) {
		return themes.Template{}, fmt.Errorf("pptx import worker: process exited with status 1")
	}

	tmpl, _, err := RunImportLoop(context.Background(), "b64", ImportLoopOptions{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if tmpl.ImportState != "failed" {
		t.Errorf("ImportState = %q, want \"failed\"", tmpl.ImportState)
	}
}

// -------------------------------------------------------------------------
// Ensure test helpers compile and the stub pptxworker protocol is satisfied
// -------------------------------------------------------------------------

var _ = pptxworker.ImportProtocolVersion // confirm import is reachable
