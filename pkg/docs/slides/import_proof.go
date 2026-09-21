package slides

import (
	"encoding/json"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// ImportProofAssetKey is the template asset key under which the import proof
// deck SceneGraph is stored. The value is a JSON-encoded SceneGraph.
// It is served by GetSlidesTemplateImportProofHandler.
const ImportProofAssetKey = "import-proof-deck"

// GenerateImportProofDeck builds a SceneGraph that contains one slide per
// imported archetype, rendering each archetype's markup verbatim (no fill
// projection). This deck is the "Phase 1 proof": it shows exactly what the
// import worker captured so a reviewer can confirm every visual pattern —
// roundRect cards, stripes, timelines, eyebrow chrome — was faithfully
// extracted before moving to authoring.
//
// The slide title for each slide is the archetype's human label (Title field),
// falling back to its Kind. The scene title is "Import Proof".
//
// Slides are ordered: fixed-tier archetypes (title, closing, section, agenda)
// first, then flexible/pattern archetypes — matching the visual progression
// of the original deck.
func GenerateImportProofDeck(tmpl themes.Template) *SceneGraph {
	if len(tmpl.Archetypes) == 0 {
		return nil
	}

	scene := &SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         "Import Proof — " + tmpl.Label,
		Theme:         tmpl.Tokens,
		// Assets are referenced by asset-ref in archetype markup and resolved
		// at render time from the template's own asset map; we carry a subset
		// here so the SceneGraph is self-contained for the preview renderer.
		Assets: lightweightAssetRefs(tmpl.Assets),
	}

	// Order: fixed-tier (brand chrome) archetypes first, then flexible/pattern.
	fixed := make([]themes.Archetype, 0, len(tmpl.Archetypes))
	flexible := make([]themes.Archetype, 0, len(tmpl.Archetypes))
	for _, a := range tmpl.Archetypes {
		if a.Tier == "fixed" || isOfficialBookendKind(a.Kind) {
			fixed = append(fixed, a)
		} else {
			flexible = append(flexible, a)
		}
	}
	ordered := append(fixed, flexible...)

	for i, arch := range ordered {
		markup := arch.Markup
		if markup == "" {
			continue
		}

		slide, _, parseErr := ParseSlide(markup)
		if parseErr != nil {
			// Un-parseable archetype: emit an empty placeholder slide so the
			// proof deck position still reflects the archetype exists.
			slide = Slide{
				ID:    slideIDForProof(i, arch),
				Title: archTitle(arch) + " (parse error)",
				Nodes: nil,
			}
		}

		if slide.ID == "" {
			slide.ID = slideIDForProof(i, arch)
		}
		if slide.Title == "" {
			slide.Title = archTitle(arch)
		}
		// Annotate the slide notes with archetype metadata so a reviewer sees
		// which archetype kind/tier each proof slide represents.
		slide.Notes = archetypeProofNote(arch)

		scene.Slides = append(scene.Slides, slide)
	}

	return scene
}

// StoreImportProofDeck serialises proof into JSON and stores it in tmpl.Assets
// under ImportProofAssetKey. It modifies tmpl.Assets in-place (allocating the
// map if nil) and returns any serialisation error.
func StoreImportProofDeck(tmpl *themes.Template, proof *SceneGraph) error {
	if proof == nil {
		return nil
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	if tmpl.Assets == nil {
		tmpl.Assets = make(map[string]string)
	}
	// Store as a plain JSON string (not a data: URI) so the API handler can
	// return it directly with Content-Type: application/json.
	tmpl.Assets[ImportProofAssetKey] = string(raw)
	return nil
}

// RetrieveImportProofDeck extracts and deserialises the proof deck stored in
// tmpl.Assets[ImportProofAssetKey]. Returns nil, nil when no proof exists.
func RetrieveImportProofDeck(tmpl themes.Template) (*SceneGraph, error) {
	raw, ok := tmpl.Assets[ImportProofAssetKey]
	if !ok || raw == "" {
		return nil, nil
	}
	var scene SceneGraph
	if err := json.Unmarshal([]byte(raw), &scene); err != nil {
		return nil, err
	}
	return &scene, nil
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

// lightweightAssetRefs returns a copy of the assets map that includes only
// non-data-URI entries (i.e. keys whose values are asset refs, not raw bytes)
// plus any small thumbnail refs. Large image/font data: URIs are excluded to
// keep the proof deck JSON lean — the renderer resolves them via the template
// media endpoint at display time.
func lightweightAssetRefs(assets map[string]string) map[string]string {
	if len(assets) == 0 {
		return nil
	}
	out := make(map[string]string, 4)
	for k, v := range assets {
		// Skip data: URIs (they can be megabytes).
		if len(v) > 512 {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func slideIDForProof(idx int, arch themes.Archetype) string {
	id := arch.Kind
	if id == "" {
		id = "slide"
	}
	// Append index suffix to keep ids unique when two archetypes share a kind.
	return id + "-proof-" + itoa(idx+1)
}

func archTitle(arch themes.Archetype) string {
	if arch.Title != "" {
		return arch.Title
	}
	return arch.Kind
}

func archetypeProofNote(arch themes.Archetype) string {
	tier := arch.Tier
	if tier == "" {
		tier = "unset"
	}
	note := "Import proof slide\nKind: " + arch.Kind + "\nTier: " + tier
	if len(arch.FillSlots) > 0 {
		note += "\nFill slots: "
		for i, s := range arch.FillSlots {
			if i > 0 {
				note += ", "
			}
			note += s
		}
	}
	return note
}

// itoa is a minimal int-to-string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
