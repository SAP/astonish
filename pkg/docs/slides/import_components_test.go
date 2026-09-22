package slides

import (
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

func TestAuditImportComponentsFlagsDroppedOpacityAndRuns(t *testing.T) {
	model := &themes.TemplateModel{Slides: []themes.IRLayout{{
		Objects: []themes.IRChrome{
			{Kind: "image", Opacity: 0.82, MediaKey: "sha256-abc"},
			{Kind: "text", Align: "ctr", Anchor: "ctr", Runs: []themes.IRRun{
				{Text: "Title", Color: "#002A86"},
				{Text: "Body line", Color: "#5B738B"},
			}},
		},
	}}}
	arch := []themes.Archetype{{
		SourceSlideIndex: 1,
		Markup:           `<ast-slide><ast-image opacity="1"></ast-image><ast-text>TitleBody line</ast-text></ast-slide>`,
	}}
	gaps := AuditImportComponents(model, arch)
	got := map[string]bool{}
	for _, g := range gaps {
		got[g.Kind] = true
	}
	for _, kind := range []string{"picture_opacity", "text_align", "text_anchor", "text_runs"} {
		if !got[kind] {
			t.Errorf("missing %s in %v", kind, gaps)
		}
	}
}

func TestAuditImportComponentsPassesWhenMarkupKeepsComponents(t *testing.T) {
	model := &themes.TemplateModel{Slides: []themes.IRLayout{{
		Objects: []themes.IRChrome{
			{Kind: "image", Opacity: 0.82},
			{Kind: "text", Align: "ctr", Anchor: "b", Runs: []themes.IRRun{
				{Text: "Title", Color: "#002A86"},
				{Text: "Body line", Color: "#5B738B"},
			}},
		},
	}}}
	arch := []themes.Archetype{{
		SourceSlideIndex: 1,
		Markup:           `<ast-image opacity="0.820"></ast-image><ast-text align="ctr" anchor="b"><ast-run color="#002A86">Title</ast-run><ast-run color="#5B738B">Body line</ast-run></ast-text>`,
	}}
	if gaps := AuditImportComponents(model, arch); len(gaps) != 0 {
		t.Fatalf("clean markup flagged: %v", gaps)
	}
}
