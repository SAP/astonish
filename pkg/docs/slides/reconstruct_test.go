package slides

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// minimalArchetype returns a simple content archetype with a ph-title and ph-body
// fill slot, using minimal valid ASD v2 markup.
func minimalArchetype(kind string) themes.Archetype {
	markup := `<ast-slide id="` + kind + `">` +
		`<ast-shape id="bg" kind="rect" x="0" y="0" w="1920" h="1080" fill="#FFFFFF" alt="" decorative="true"></ast-shape>` +
		`<ast-text id="ph-title" x="160" y="120" w="1600" h="140" color="#172033" size="56">Placeholder Title</ast-text>` +
		`<ast-text id="ph-body" x="160" y="320" w="1600" h="600" color="#172033" size="36">Placeholder Body</ast-text>` +
		`</ast-slide>`
	return themes.Archetype{
		Kind:      kind,
		Title:     kind,
		Markup:    markup,
		FillSlots: []string{"ph-title", "ph-body"},
	}
}

// threeSlideModel returns a TemplateModel with three source slides of mixed
// kinds but no warnings.
func threeSlideModel() *themes.TemplateModel {
	return &themes.TemplateModel{
		Schema: themes.SchemaModelV3,
		Slides: []themes.IRLayout{
			{
				ID:   "slide-1",
				Name: "Title Slide",
				Placeholders: []themes.IRPlaceholder{
					{Name: "My Presentation", Type: "ctrTitle", Prompt: "My Presentation"},
					{Name: "Subtitle here", Type: "subTitle", Prompt: "Subtitle here"},
				},
			},
			{
				ID:   "slide-2",
				Name: "Section Divider",
				Placeholders: []themes.IRPlaceholder{
					{Name: "Chapter 1", Type: "title", Prompt: "Chapter 1"},
				},
			},
			{
				ID:   "slide-3",
				Name: "Content Slide",
				Placeholders: []themes.IRPlaceholder{
					{Name: "Key Points", Type: "title", Prompt: "Key Points"},
					{Name: "• Point one\n• Point two", Type: "body", Prompt: "• Point one\n• Point two"},
				},
			},
		},
	}
}

// TestReconstructSceneSlideCount verifies that the reconstructed scene has
// exactly as many slides as the source model.
func TestReconstructSceneSlideCount(t *testing.T) {
	model := threeSlideModel()
	archetypes := []themes.Archetype{
		minimalArchetype("title"),
		minimalArchetype("section"),
		minimalArchetype("content"),
	}

	scene, _, err := ReconstructScene(model, archetypes, nil)
	if err != nil {
		t.Fatalf("ReconstructScene returned error: %v", err)
	}
	if len(scene.Slides) != 3 {
		t.Errorf("expected 3 slides, got %d", len(scene.Slides))
	}
}

// TestReconstructSceneTextProjection verifies that a title placeholder with
// "Hello World" ends up in the reconstructed slide markup.
func TestReconstructSceneTextProjection(t *testing.T) {
	model := &themes.TemplateModel{
		Schema: themes.SchemaModelV3,
		Slides: []themes.IRLayout{
			{
				ID:   "slide-1",
				Name: "Title Slide",
				Placeholders: []themes.IRPlaceholder{
					{Name: "Hello World", Type: "title", Prompt: "Hello World"},
				},
			},
		},
	}
	archetypes := []themes.Archetype{minimalArchetype("content")}

	scene, _, err := ReconstructScene(model, archetypes, nil)
	if err != nil {
		t.Fatalf("ReconstructScene returned error: %v", err)
	}
	if len(scene.Slides) == 0 {
		t.Fatal("expected at least one slide")
	}

	// The parsed slide has nodes; reconstruct the markup check via the slide
	// title (set from the title placeholder) and nodes text content.
	found := false
	slide := scene.Slides[0]
	if strings.Contains(slide.Title, "Hello World") {
		found = true
	}
	for _, n := range slide.Nodes {
		if strings.Contains(n.Text, "Hello World") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Hello World' in reconstructed slide, got title=%q nodes=%v", slide.Title, slide.Nodes)
	}
}

// TestReconstructSceneUnsupportedWarning verifies that model warnings containing
// "table" are propagated to the returned warnings slice.
func TestReconstructSceneUnsupportedWarning(t *testing.T) {
	model := &themes.TemplateModel{
		Schema: themes.SchemaModelV3,
		Slides: []themes.IRLayout{
			{ID: "slide-1", Name: "Content"},
		},
		Warnings: []themes.IRWarning{
			{Code: "unsupported", Message: "table unsupported"},
			{Code: "unsupported", Message: "some other warning"},
		},
	}
	archetypes := []themes.Archetype{minimalArchetype("content")}

	_, warnings, err := ReconstructScene(model, archetypes, nil)
	if err != nil {
		t.Fatalf("ReconstructScene returned error: %v", err)
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "table unsupported") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'table unsupported' in warnings, got: %v", warnings)
	}

	// The non-table warning should NOT be in the returned warnings.
	for _, w := range warnings {
		if strings.Contains(w, "some other warning") {
			t.Errorf("unexpected warning propagated: %q", w)
		}
	}
}
