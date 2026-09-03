package slides

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
)

// HTMLExporter serializes a normalized scene and the trusted, pre-built slides
// runtime into a self-contained document. Asset references must already be
// content-addressed data URLs before export.
type HTMLExporter struct {
	RuntimeJS        []byte
	RuntimeScriptURL string
	Print            bool
}

func (e HTMLExporter) Export(scene SceneGraph) (ExportResult, error) {
	if scene.SchemaVersion != SchemaV1 && scene.SchemaVersion != SchemaV2 && scene.SchemaVersion != SchemaV3 {
		return ExportResult{}, fmt.Errorf("unsupported slides schema version %d", scene.SchemaVersion)
	}
	if len(scene.Slides) == 0 {
		return ExportResult{}, fmt.Errorf("deck requires at least one slide")
	}
	if len(e.RuntimeJS) == 0 {
		return ExportResult{}, fmt.Errorf("slides runtime is empty")
	}
	var diagnostics []Diagnostic
	for _, slide := range scene.Slides {
		diagnostics = append(diagnostics, ValidateSlide(slide)...)
	}
	if HasErrors(diagnostics) {
		return ExportResult{Diagnostics: diagnostics}, fmt.Errorf("slide scene is invalid")
	}

	var body bytes.Buffer
	body.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	body.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	runtimeJS := bytes.ReplaceAll(e.RuntimeJS, []byte("</script"), []byte("<\\/script"))
	hash := sha256.Sum256(runtimeJS)
	body.WriteString(`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'sha256-`)
	body.WriteString(base64.StdEncoding.EncodeToString(hash[:]))
	body.WriteString(`'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'">`)
	body.WriteString(`<title>` + html.EscapeString(scene.Title) + `</title>`)
	body.WriteString(`<style>:root{--ast-surface:#fff;--ast-ink:#172033;--ast-ink-muted:#64748b;--ast-accent:#1e40af;--ast-accent-soft:#dbeafe;--ast-display:Aptos Display,Arial,sans-serif;--ast-body-font:Aptos,Arial,sans-serif}`)
	writeThemeCSS(&body, scene.Theme)
	// Load exactly the faces the deck declares. Missing files may be filled
	// from the bundled library; undeclared families are never loaded.
	theme := cloneStringMap(scene.Theme)
	assets := cloneStringMap(scene.Assets)
	if assets == nil {
		assets = map[string]string{}
	}
	if theme == nil {
		theme = map[string]string{}
	}
	inheritDeclaredFontsFromTemplate(theme)
	fillDeclaredFontAssets(theme, assets)
	writeFontFaces(&body, theme, assets)
	body.WriteString(`html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#000}ast-deck{display:block;position:absolute;width:1920px;height:1080px;overflow:hidden;transform-origin:top left;background:var(--ast-surface);color:var(--ast-ink)}ast-slide{display:none;position:absolute;inset:0;width:1920px;height:1080px;overflow:hidden}ast-slide[active]{display:block}ast-text{white-space:pre-wrap;overflow-wrap:break-word;overflow-x:clip;overflow-y:visible;overflow-clip-margin:0.32em;font-variant-ligatures:none}ast-notes{display:none}`)
	if e.Print {
		// Print layout: paginate one slide per page. The @page box and every
		// slide are declared in the SAME inch units as the PDF paper (20in x
		// 11.25in == 1920x1080px at 96dpi) so Chrome maps content 1:1 to the
		// sheet with no scale-to-fit (no white margins) and no sub-pixel
		// overflow spilling onto a blank second page. The child nodes remain in
		// px on the 1920x1080 canvas, which is exactly the inch box at 96dpi.
		// break-inside:avoid on the slide prevents the trailing blank page that
		// a page-sized block otherwise forces after its own forced break.
		body.WriteString(`@page{size:20in 11.25in;margin:0}html,body{width:20in;margin:0;padding:0;overflow:visible}body{background:var(--ast-surface)}ast-deck{display:block;position:static;width:20in;height:auto;overflow:visible;background:transparent}ast-slide{display:block;position:relative;inset:auto;width:20in;height:11.25in;overflow:hidden;break-inside:avoid;break-after:page;page-break-after:always}ast-slide:last-of-type{break-after:auto;page-break-after:auto}`)
	}
	body.WriteString(`</style></head><body>`)
	body.WriteString(`<ast-deck schema="1" ratio="16:9"`)
	if e.Print {
		body.WriteString(` print`)
	}
	body.WriteString(`>`)
	for _, slide := range scene.Slides {
		renderSlide(&body, slide, scene.Assets)
	}
	body.WriteString(`</ast-deck><script>`)
	body.Write(runtimeJS)
	body.WriteString(`</script></body></html>`)
	return ExportResult{Bytes: body.Bytes(), Diagnostics: diagnostics}, nil
}

// fontTokenAliases maps camelCase template token keys to the kebab-case CSS
// variable names the runtime expects. When a template (or imported deck) stores
// "displayFont" → "Manrope", the runtime's AstText reads var(--ast-display),
// NOT var(--ast-displayFont). This map bridges the gap: both --ast-displayFont
// and --ast-display are emitted so every consumer finds the value it expects.
// The PPTX exporter (worker.mjs) already checks both variants.
var fontTokenAliases = map[string]string{
	"displayFont": "display",
	"bodyFont":    "body-font",
	"monoFont":    "mono-font",
}

func writeThemeCSS(out *bytes.Buffer, theme map[string]string) {
	if len(theme) == 0 {
		return
	}
	out.WriteString(`:root{`)
	keys := make([]string, 0, len(theme))
	for key := range theme {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// The embedded-fonts key carries a JSON manifest consumed by
		// writeFontFaces (emitted as @font-face rules), NOT a CSS value — never
		// surface it as a --ast-* variable.
		if isThemeMetaKey(key) {
			continue
		}
		if !safeAttributeName(key) || !safeCSSValue(theme[key]) {
			continue
		}
		out.WriteString("--ast-")
		out.WriteString(key)
		out.WriteByte(':')
		out.WriteString(theme[key])
		out.WriteByte(';')
		// Emit a CSS-compatible alias when the canonical name differs from
		// the template token key (e.g. displayFont → display).  Skip if
		// the theme already contains the alias key to avoid double-emission.
		if alias, ok := fontTokenAliases[key]; ok {
			if _, exists := theme[alias]; !exists {
				out.WriteString("--ast-")
				out.WriteString(alias)
				out.WriteByte(':')
				out.WriteString(theme[key])
				out.WriteByte(';')
			}
		}
	}
	out.WriteByte('}')
}

func safeCSSValue(value string) bool {
	if value == "" || strings.ContainsAny(value, `;{}<>`) {
		return false
	}
	// Allow the characters needed for hex colors (#), rgb()/rgba() and
	// var(--ast-...) declarations, plus general letters/digits/spacing.
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '#' || r == '(' || r == ')' || r == ',' || r == '.' || r == '%':
		case r == '-' || r == ' ' || r == '_':
		default:
			return false
		}
	}
	return true
}

func renderSlide(out *bytes.Buffer, slide Slide, assets map[string]string) {
	out.WriteString(`<ast-slide id="` + html.EscapeString(slide.ID) + `"`)
	if slide.Title != "" {
		out.WriteString(` title="` + html.EscapeString(slide.Title) + `"`)
	}
	out.WriteString(`>`)
	for _, node := range slide.Nodes {
		renderNode(out, node, assets, slide.ID)
	}
	if slide.Notes != "" {
		out.WriteString(`<ast-notes>` + html.EscapeString(slide.Notes) + `</ast-notes>`)
	}
	out.WriteString(`</ast-slide>`)
}

func renderNode(out *bytes.Buffer, node Node, assets map[string]string, slideID string) {
	tag := "ast-" + node.Type
	if !allowedNodeTag(tag) {
		return
	}
	out.WriteByte('<')
	out.WriteString(tag)
	writeAttr(out, "id", node.ID)
	writeAttr(out, "x", strconv.Itoa(node.Geometry.X))
	writeAttr(out, "y", strconv.Itoa(node.Geometry.Y))
	writeAttr(out, "w", strconv.Itoa(node.Geometry.W))
	writeAttr(out, "h", strconv.Itoa(node.Geometry.H))
	// FlipH/FlipV are struct fields (not Props), so emit them explicitly as
	// reflected attributes so the embedded Lit runtime element composes the same
	// scaleX/scaleY(-1) transform as nodeInlineStyle.
	if node.FlipH {
		writeAttr(out, "flip-h", "true")
	}
	if node.FlipV {
		writeAttr(out, "flip-v", "true")
	}
	keys := make([]string, 0, len(node.Props))
	for key := range node.Props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !safeAttributeName(key) {
			continue
		}
		value, ok := scalarString(node.Props[key])
		if !ok {
			continue
		}
		// ast-image references its bitmap by content-addressed asset-ref; the Lit
		// runtime only renders the `src` property. Keep the asset-ref (other
		// consumers/exporters and a future editor rely on it) AND additionally
		// emit a concrete data: URL resolved from the deck asset map so imported
		// -template logos/media actually render and the export is self-contained.
		if tag == "ast-image" && key == "asset-ref" {
			writeAttr(out, key, value)
			if src, ok := resolveImageSrc(value, assets); ok {
				writeAttr(out, "src", src)
			}
			continue
		}
		writeAttr(out, key, value)
	}
	if node.Table != nil {
		data, _ := json.Marshal(node.Table)
		writeAttr(out, "data-json", string(data))
	}
	if len(node.Series) > 0 {
		data, _ := json.Marshal(node.Series)
		writeAttr(out, "data-json", string(data))
	}
	if style := nodeInlineStyle(node); style != "" {
		writeAttr(out, "style", style)
	}
	out.WriteByte('>')
	// v2 fidelity: rich shape rendering via inline SVG.
	if tag == "ast-shape" && shapeNeedsSVG(node) {
		writeShapeSVG(out, node, slideID)
	}
	// v2 fidelity: rich text runs.
	if tag == "ast-text" && len(node.Runs) > 0 {
		writeTextRuns(out, node.Runs)
	} else {
		out.WriteString(html.EscapeString(node.Text))
	}
	for _, child := range node.Children {
		renderNode(out, child, assets, slideID)
	}
	out.WriteString(`</` + tag + `>`)
}

// resolveImageSrc turns an ast-image asset-ref into a concrete, self-contained
// data: URL by looking it up in the deck asset map. It accepts an already-inline
// data: URL as-is (idempotent) and rejects anything that is not a data:image/*
// URL, so a stray or external reference can never become a live network src
// under the export CSP. Returns ("", false) when the ref cannot be resolved to
// a safe data URL.
func resolveImageSrc(ref string, assets map[string]string) (string, bool) {
	candidate := ref
	if !strings.HasPrefix(candidate, "data:") {
		resolved, ok := assets[ref]
		if !ok {
			return "", false
		}
		candidate = resolved
	}
	if !strings.HasPrefix(candidate, "data:image/") {
		return "", false
	}
	return candidate, true
}

// nodeInlineStyle assembles an inline style string from v2 fidelity fields.
// Only safe, self-contained values are emitted (transform + opacity). The
// transform composes rotation and horizontal/vertical flip (scaleX/scaleY(-1)),
// mirroring the Lit runtime's PositionedElement.updated() so the standalone HTML
// and live web renderer agree.
func nodeInlineStyle(node Node) string {
	var parts []string
	var transform []string
	if node.Rot != 0 {
		transform = append(transform, "rotate("+strconv.Itoa(node.Rot)+"deg)")
	}
	if node.FlipH {
		transform = append(transform, "scaleX(-1)")
	}
	if node.FlipV {
		transform = append(transform, "scaleY(-1)")
	}
	if len(transform) > 0 {
		parts = append(parts, "transform:"+strings.Join(transform, " ")+";transform-origin:center")
	}
	if node.Opacity > 0 && node.Opacity < 1 {
		parts = append(parts, "opacity:"+strconv.FormatFloat(node.Opacity, 'g', -1, 64))
	}
	return strings.Join(parts, ";")
}

func shapeNeedsSVG(node Node) bool {
	if node.Geom != "" || node.Path != "" || node.Gradient != nil {
		return true
	}
	// Raw color fill also warrants an SVG shape.
	if isRawColor(node.Fill) {
		return true
	}
	return false
}

// isRawColor reports whether v is a literal color value (#hex or rgb()/rgba()).
func isRawColor(v string) bool {
	if v == "" {
		return false
	}
	return strings.HasPrefix(v, "#") || strings.HasPrefix(v, "rgb(") || strings.HasPrefix(v, "rgba(")
}

// resolveColor turns a fidelity color-ish value into a CSS color. Raw colors
// (#hex, rgb()) are used directly; anything else is treated as a theme token
// and resolved to var(--ast-<token>). Returns "" if unsafe/empty.
func resolveColor(v string) string {
	if v == "" {
		return ""
	}
	if isRawColor(v) {
		if safeCSSValue(v) {
			return v
		}
		return ""
	}
	if safeAttributeName(v) {
		return "var(--ast-" + v + ")"
	}
	return ""
}

func dashArray(dash string) string {
	switch dash {
	case "dash":
		return "8 6"
	case "dot":
		return "2 4"
	default:
		return ""
	}
}

// writeShapeSVG emits an inline SVG that fills the node box, honoring geom,
// path, gradient, fill/line/dash and arrow-head props. All content is inline
// (no external URLs), satisfying the existing CSP.
func writeShapeSVG(out *bytes.Buffer, node Node, slideID string) {
	w := node.Geometry.W
	h := node.Geometry.H
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 100
	}
	vb := fmt.Sprintf("0 0 %d %d", w, h)

	// Resolve fill. Gradient/marker ids must be unique across the whole
	// document: every recipe slide uses id="bg", so id="gradbg" would make
	// print/PDF paint the first slide's wash on every page (including the
	// closer's bottom-left glare).
	var fill string
	gradID := ""
	if node.Gradient != nil {
		gradID = "grad-" + safeID(slideID) + "-" + safeID(node.ID)
		fill = "url(#" + gradID + ")"
	} else if isRawColor(node.Fill) {
		fill = resolveColor(node.Fill)
	} else if node.Fill != "" {
		fill = resolveColor(node.Fill)
	}
	if fill == "" {
		fill = "none"
	}

	stroke := resolveColor(node.Line)
	strokeWidth := ""
	if node.Props != nil {
		if lw, ok := scalarString(node.Props["line-width"]); ok && safeCSSValue(lw) {
			strokeWidth = lw
		}
	}
	dash := dashArray(node.Dash)

	// Arrow markers (best-effort, mainly for line geom).
	headEnd, _ := propString(node, "head-end")
	tailEnd, _ := propString(node, "tail-end")
	wantStartMarker := tailEnd == "arrow" || tailEnd == "triangle"
	wantEndMarker := headEnd == "arrow" || headEnd == "triangle"
	markerStartID := "mstart-" + safeID(slideID) + "-" + safeID(node.ID)
	markerEndID := "mend-" + safeID(slideID) + "-" + safeID(node.ID)

	out.WriteString(`<svg style="width:100%;height:100%" viewBox="` + vb + `" preserveAspectRatio="none" xmlns="http://www.w3.org/2000/svg">`)

	// defs
	needDefs := node.Gradient != nil || wantStartMarker || wantEndMarker
	if needDefs {
		out.WriteString(`<defs>`)
		if node.Gradient != nil {
			writeGradientDef(out, gradID, node.Gradient)
		}
		markerColor := stroke
		if markerColor == "" {
			markerColor = "currentColor"
		}
		if wantStartMarker {
			writeArrowMarker(out, markerStartID, markerColor)
		}
		if wantEndMarker {
			writeArrowMarker(out, markerEndID, markerColor)
		}
		out.WriteString(`</defs>`)
	}

	// shape element
	geom := node.Geom
	if node.Path != "" && geom != "line" {
		geom = "path"
	}
	switch geom {
	case "ellipse":
		fmt.Fprintf(out, `<ellipse cx="%d" cy="%d" rx="%d" ry="%d"`, w/2, h/2, w/2, h/2)
		writeShapePaint(out, fill, stroke, strokeWidth, dash, "", "")
		out.WriteString(`/>`)
	case "roundRect":
		rx := w / 10
		if rx <= 0 {
			rx = 4
		}
		fmt.Fprintf(out, `<rect x="0" y="0" width="%d" height="%d" rx="%d"`, w, h, rx)
		writeShapePaint(out, fill, stroke, strokeWidth, dash, "", "")
		out.WriteString(`/>`)
	case "triangle":
		d := fmt.Sprintf("M %d 0 L %d %d L 0 %d Z", w/2, w, h, h)
		out.WriteString(`<path d="` + html.EscapeString(d) + `"`)
		writeShapePaint(out, fill, stroke, strokeWidth, dash, "", "")
		out.WriteString(`/>`)
	case "line":
		d := node.Path
		if d == "" {
			d = fmt.Sprintf("M 0 %d L %d %d", h/2, w, h/2)
		}
		ms, me := "", ""
		if wantStartMarker {
			ms = markerStartID
		}
		if wantEndMarker {
			me = markerEndID
		}
		lineStroke := stroke
		if lineStroke == "" {
			lineStroke = "currentColor"
		}
		out.WriteString(`<path d="` + html.EscapeString(d) + `"`)
		writeShapePaint(out, "none", lineStroke, strokeWidth, dash, ms, me)
		out.WriteString(`/>`)
	case "path":
		out.WriteString(`<path d="` + html.EscapeString(node.Path) + `"`)
		writeShapePaint(out, fill, stroke, strokeWidth, dash, "", "")
		out.WriteString(`/>`)
	default: // rect and unknown presets fall back to a rectangle.
		fmt.Fprintf(out, `<rect x="0" y="0" width="%d" height="%d"`, w, h)
		writeShapePaint(out, fill, stroke, strokeWidth, dash, "", "")
		out.WriteString(`/>`)
	}

	out.WriteString(`</svg>`)
}

func writeShapePaint(out *bytes.Buffer, fill, stroke, strokeWidth, dash, markerStart, markerEnd string) {
	out.WriteString(` fill="` + html.EscapeString(fill) + `"`)
	if stroke != "" {
		out.WriteString(` stroke="` + html.EscapeString(stroke) + `"`)
		if strokeWidth != "" {
			out.WriteString(` stroke-width="` + html.EscapeString(strokeWidth) + `"`)
		}
	}
	if dash != "" {
		out.WriteString(` stroke-dasharray="` + html.EscapeString(dash) + `"`)
	}
	if markerStart != "" {
		out.WriteString(` marker-start="url(#` + markerStart + `)"`)
	}
	if markerEnd != "" {
		out.WriteString(` marker-end="url(#` + markerEnd + `)"`)
	}
}

func writeGradientDef(out *bytes.Buffer, id string, g *Gradient) {
	if g.Kind == "radial" {
		cx, cy := radialOrigin(g)
		fmt.Fprintf(out, `<radialGradient id="%s" cx="%d%%" cy="%d%%" r="72%%">`, id, cx, cy)
		writeGradientStops(out, g.Stops)
		out.WriteString(`</radialGradient>`)
		return
	}
	// linear: derive endpoints from angle (degrees, clockwise from +x axis).
	rad := float64(g.Angle) * math.Pi / 180
	dx := math.Cos(rad)
	dy := math.Sin(rad)
	x1 := 0.5 - dx/2
	y1 := 0.5 - dy/2
	x2 := 0.5 + dx/2
	y2 := 0.5 + dy/2
	fmt.Fprintf(out, `<linearGradient id="%s" x1="%s" y1="%s" x2="%s" y2="%s">`,
		id, fmtCoord(x1), fmtCoord(y1), fmtCoord(x2), fmtCoord(y2))
	writeGradientStops(out, g.Stops)
	out.WriteString(`</linearGradient>`)
}

// radialOrigin is the wash center. Cover/body default top-right; a closer
// that sets cx/cy (e.g. 18/88) puts the glare bottom-left.
func radialOrigin(g *Gradient) (cx, cy int) {
	cx, cy = 80, 8
	if g == nil {
		return cx, cy
	}
	if g.Cx != 0 {
		cx = clampPct(g.Cx)
	}
	if g.Cy != 0 {
		cy = clampPct(g.Cy)
	}
	return cx, cy
}

func clampPct(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func writeGradientStops(out *bytes.Buffer, stops []GradientStop) {
	for _, s := range stops {
		color := s.Color
		if !safeCSSValue(color) {
			color = ""
		}
		fmt.Fprintf(out, `<stop offset="%d%%" stop-color="%s"/>`, s.Pos, html.EscapeString(color))
	}
}

func writeArrowMarker(out *bytes.Buffer, id, color string) {
	out.WriteString(`<marker id="` + id + `" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">`)
	out.WriteString(`<path d="M 0 0 L 10 5 L 0 10 z" fill="` + html.EscapeString(color) + `"/>`)
	out.WriteString(`</marker>`)
}

func fmtCoord(v float64) string {
	return strconv.FormatFloat(v, 'g', 4, 64)
}

// safeID sanitizes a node id for use in an SVG id attribute.
func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

func propString(node Node, key string) (string, bool) {
	if node.Props == nil {
		return "", false
	}
	return scalarString(node.Props[key])
}

// writeTextRuns renders rich text runs as nested styled spans.
func writeTextRuns(out *bytes.Buffer, runs []TextRun) {
	for _, run := range runs {
		style := runInlineStyle(run)
		out.WriteString(`<span`)
		if style != "" {
			out.WriteString(` style="` + html.EscapeString(style) + `"`)
		}
		out.WriteByte('>')
		out.WriteString(html.EscapeString(run.Text))
		out.WriteString(`</span>`)
	}
}

func runInlineStyle(run TextRun) string {
	var parts []string
	if run.Bold {
		parts = append(parts, "font-weight:700")
	} else if run.Weight != "" && safeCSSValue(run.Weight) {
		parts = append(parts, "font-weight:"+run.Weight)
	}
	if run.Italic {
		parts = append(parts, "font-style:italic")
	}
	if run.Underline {
		parts = append(parts, "text-decoration:underline")
	}
	if c := resolveColor(run.Color); c != "" {
		parts = append(parts, "color:"+c)
	}
	if run.Font != "" && safeCSSValue(run.Font) {
		parts = append(parts, "font-family:"+run.Font)
	}
	if run.Size > 0 {
		parts = append(parts, "font-size:"+strconv.Itoa(run.Size)+"px")
	}
	return strings.Join(parts, ";")
}

func writeAttr(out *bytes.Buffer, name, value string) {
	if value == "" {
		return
	}
	out.WriteByte(' ')
	out.WriteString(name)
	out.WriteString(`="`)
	out.WriteString(html.EscapeString(value))
	out.WriteByte('"')
}

func allowedNodeTag(tag string) bool {
	switch tag {
	case "ast-text", "ast-shape", "ast-image", "ast-table", "ast-chart", "ast-group", "ast-code", "ast-icon", "ast-fragment":
		return true
	default:
		return false
	}
}

func safeAttributeName(name string) bool {
	if name == "" || strings.HasPrefix(strings.ToLower(name), "on") {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func scalarString(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), true
	case int:
		return strconv.Itoa(value), true
	default:
		return "", false
	}
}
