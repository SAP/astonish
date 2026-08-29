package slides

import "strings"

// assetsUsedByScene returns the subset of scene.Assets referenced by slide
// markup (ast-image asset-ref) or by the embedded-font manifest. Present/PDF/
// PPTX export must not carry unused template sample photos.
func assetsUsedByScene(scene SceneGraph) map[string]string {
	if len(scene.Assets) == 0 {
		return scene.Assets
	}
	need := make(map[string]bool, 8)
	for _, ref := range parseEmbeddedFonts(scene.Theme) {
		if ref.AssetKey != "" {
			need[ref.AssetKey] = true
		}
	}
	for _, slide := range scene.Slides {
		collectNodeAssetRefs(slide.Nodes, need)
	}
	out := make(map[string]string, len(need))
	for k := range need {
		if v, ok := scene.Assets[k]; ok {
			out[k] = v
		}
	}
	return out
}

func collectNodeAssetRefs(nodes []Node, need map[string]bool) {
	for _, n := range nodes {
		if n.Props != nil {
			if v, ok := n.Props["asset-ref"].(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					need[v] = true
				}
			}
		}
		if len(n.Children) > 0 {
			collectNodeAssetRefs(n.Children, need)
		}
	}
}
