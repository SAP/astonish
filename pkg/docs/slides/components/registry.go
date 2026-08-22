// Package components defines the versioned, target-neutral ASD component registry.
package components

import "sort"

type Fidelity string

const (
	FidelityNative      Fidelity = "native"
	FidelityVector      Fidelity = "vector"
	FidelityRaster      Fidelity = "raster"
	FidelityUnsupported Fidelity = "unsupported"
)

type Definition struct {
	TagName          string
	RequiresGeometry bool
	AllowedChildren  []string
	PPTXFidelity     Fidelity
}

var v1 = map[string]Definition{
	"ast-deck":  {TagName: "ast-deck", AllowedChildren: []string{"ast-slide"}, PPTXFidelity: FidelityNative},
	"ast-slide": {TagName: "ast-slide", AllowedChildren: []string{"ast-text", "ast-shape", "ast-image", "ast-table", "ast-chart", "ast-group", "ast-notes"}, PPTXFidelity: FidelityNative},
	"ast-text":  {TagName: "ast-text", RequiresGeometry: true, PPTXFidelity: FidelityNative},
	"ast-shape": {TagName: "ast-shape", RequiresGeometry: true, AllowedChildren: []string{"ast-text"}, PPTXFidelity: FidelityNative},
	"ast-image": {TagName: "ast-image", RequiresGeometry: true, PPTXFidelity: FidelityNative},
	"ast-table": {TagName: "ast-table", RequiresGeometry: true, PPTXFidelity: FidelityNative},
	"ast-chart": {TagName: "ast-chart", RequiresGeometry: true, PPTXFidelity: FidelityNative},
	"ast-group": {TagName: "ast-group", RequiresGeometry: true, AllowedChildren: []string{"ast-text", "ast-shape", "ast-image"}, PPTXFidelity: FidelityNative},
	"ast-notes": {TagName: "ast-notes", PPTXFidelity: FidelityNative},
}

func LookupV1(tag string) (Definition, bool) {
	def, ok := v1[tag]
	return def, ok
}

func TagsV1() []string {
	tags := make([]string, 0, len(v1))
	for tag := range v1 {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}
