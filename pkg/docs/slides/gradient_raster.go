package slides

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// rasterizeSceneGradients replaces gradient-filled shapes with PNG images so
// PPTX export does not depend on pptxgenjs SVG (which writes a fake .png
// preview and reuses id="g", so PowerPoint shows the first slide's wash on
// every page). The returned scene is a shallow clone; the caller's nodes are
// not mutated.
func rasterizeSceneGradients(scene SceneGraph) SceneGraph {
	if len(scene.Slides) == 0 {
		return scene
	}
	slides := make([]Slide, len(scene.Slides))
	for i, s := range scene.Slides {
		slides[i] = s
		slides[i].Nodes = rasterizeGradientNodes(s.Nodes)
	}
	scene.Slides = slides
	return scene
}

func rasterizeGradientNodes(nodes []Node) []Node {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		out[i] = n
		if len(n.Children) > 0 {
			out[i].Children = rasterizeGradientNodes(n.Children)
		}
		if n.Type != "shape" || n.Gradient == nil || len(n.Gradient.Stops) < 2 {
			continue
		}
		w, h := n.Geometry.W, n.Geometry.H
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		uri, err := rasterizeGradientPNG(n.Gradient, w, h)
		if err != nil || uri == "" {
			continue
		}
		props := cloneProps(n.Props)
		props["data"] = uri
		if _, ok := props["fit"]; !ok {
			props["fit"] = "fill"
		}
		out[i].Type = "image"
		out[i].Gradient = nil
		out[i].Geom = ""
		out[i].Fill = ""
		out[i].Props = props
	}
	return out
}

func cloneProps(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func rasterizeGradientPNG(g *Gradient, w, h int) (string, error) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	stops := make([]struct {
		t float64
		c color.NRGBA
	}, 0, len(g.Stops))
	for _, s := range g.Stops {
		c, ok := parseHexNRGBA(s.Color)
		if !ok {
			continue
		}
		t := float64(s.Pos) / 100
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		stops = append(stops, struct {
			t float64
			c color.NRGBA
		}{t, c})
	}
	if len(stops) < 2 {
		return "", fmt.Errorf("gradient needs 2 color stops")
	}
	cx, cy := radialOrigin(g)
	fx := float64(w) * float64(cx) / 100
	fy := float64(h) * float64(cy) / 100
	rx := 0.72 * float64(w)
	ry := 0.72 * float64(h)
	linear := strings.EqualFold(g.Kind, "linear")
	ang := float64(g.Angle) * math.Pi / 180
	ldx, ldy := math.Cos(ang), math.Sin(ang)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var t float64
			if linear {
				// Project onto the gradient axis through the box center.
				px := (float64(x)+0.5)/float64(w) - 0.5
				py := (float64(y)+0.5)/float64(h) - 0.5
				t = px*ldx + py*ldy + 0.5
			} else {
				dx := (float64(x) + 0.5 - fx) / rx
				dy := (float64(y) + 0.5 - fy) / ry
				t = math.Hypot(dx, dy)
			}
			img.SetNRGBA(x, y, lerpStops(stops, t))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func lerpStops(stops []struct {
	t float64
	c color.NRGBA
}, t float64) color.NRGBA {
	if t <= stops[0].t {
		return stops[0].c
	}
	last := stops[len(stops)-1]
	if t >= last.t {
		return last.c
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].t {
			span := stops[i].t - stops[i-1].t
			u := 0.0
			if span > 0 {
				u = (t - stops[i-1].t) / span
			}
			return lerpNRGBA(stops[i-1].c, stops[i].c, u)
		}
	}
	return last.c
}

func lerpNRGBA(a, b color.NRGBA, t float64) color.NRGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t + 0.5),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t + 0.5),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t + 0.5),
		A: 255,
	}
}

func parseHexNRGBA(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, true
}
