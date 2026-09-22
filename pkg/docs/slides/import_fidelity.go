package slides

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// ComponentGap is one source component the imported markup did not keep.
// Kind is stable across decks (picture_opacity, text_align, text_runs) so a
// repair pass can fix the importer, not one template's markup.
type ComponentGap struct {
	Slide int
	Kind  string
	What  string
}

func (g ComponentGap) String() string {
	return fmt.Sprintf("slide %d %s: %s", g.Slide+1, g.Kind, g.What)
}

func recordComponentGaps(tmpl *themes.Template) {
	if tmpl == nil || tmpl.Model == nil {
		return
	}
	for _, g := range AuditImportComponents(tmpl.Model, tmpl.Archetypes) {
		tmpl.ImportWarnings = append(tmpl.ImportWarnings, g.String())
	}
}

// AuditImportComponents compares the source IR with the archetype markup chosen
// for each slide. It does not know any deck's colors or layout. A gap means a
// component the worker extracted was dropped on the way to ASD.
func AuditImportComponents(model *themes.TemplateModel, archetypes []themes.Archetype) []ComponentGap {
	if model == nil {
		return nil
	}
	var gaps []ComponentGap
	for i, slide := range model.Slides {
		markup := markupForSlide(archetypes, i)
		if markup == "" {
			continue
		}
		gaps = append(gaps, auditSlideComponents(i, slide, markup)...)
	}
	return gaps
}

func markupForSlide(archetypes []themes.Archetype, index int) string {
	want := index + 1
	for _, a := range archetypes {
		if a.SourceSlideIndex == want && a.Markup != "" {
			return a.Markup
		}
	}
	return ""
}

func auditSlideComponents(index int, slide themes.IRLayout, markup string) []ComponentGap {
	var gaps []ComponentGap
	for _, obj := range slide.Objects {
		if obj.Kind == "image" && obj.Opacity > 0 && obj.Opacity < 0.999 {
			token := fmt.Sprintf("opacity=\"%.3f\"", obj.Opacity)
			if !strings.Contains(markup, token) {
				gaps = append(gaps, ComponentGap{index, "picture_opacity", fmt.Sprintf("picture alpha %.3f was not emitted", obj.Opacity)})
			}
		}
		if obj.Align == "ctr" || obj.Align == "r" || obj.Align == "justify" {
			if !strings.Contains(markup, `align="`+obj.Align+`"`) {
				gaps = append(gaps, ComponentGap{index, "text_align", "horizontal align " + obj.Align + " was dropped"})
			}
		}
		if obj.Anchor == "ctr" || obj.Anchor == "b" {
			if !strings.Contains(markup, `anchor="`+obj.Anchor+`"`) {
				gaps = append(gaps, ComponentGap{index, "text_anchor", "vertical anchor " + obj.Anchor + " was dropped"})
			}
		}
		if len(obj.Runs) >= 2 {
			missing := 0
			for _, r := range obj.Runs[1:] {
				second := strings.TrimSpace(r.Text)
				if len(second) < 2 {
					continue
				}
				if !strings.Contains(markup, second) {
					missing++
				}
			}
			if missing > 0 {
				gaps = append(gaps, ComponentGap{index, "text_runs", fmt.Sprintf("%d later text runs were flattened away", missing)})
			}
			if c := strings.ToUpper(obj.Runs[1].Color); c != "" && c != strings.ToUpper(obj.Runs[0].Color) && !strings.Contains(strings.ToUpper(markup), c) {
				gaps = append(gaps, ComponentGap{index, "text_runs", "run color " + c + " was flattened into the first run"})
			}
		}
	}
	for _, ph := range slide.Placeholders {
		if ph.Align == "ctr" || ph.Align == "r" || ph.Align == "justify" {
			if !strings.Contains(markup, `align="`+ph.Align+`"`) {
				gaps = append(gaps, ComponentGap{index, "text_align", "placeholder align " + ph.Align + " was dropped"})
			}
		}
		if ph.Anchor == "ctr" || ph.Anchor == "b" {
			if !strings.Contains(markup, `anchor="`+ph.Anchor+`"`) {
				gaps = append(gaps, ComponentGap{index, "text_anchor", "placeholder anchor " + ph.Anchor + " was dropped"})
			}
		}
	}
	return gaps
}
