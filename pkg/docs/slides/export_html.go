package slides

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
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
	if scene.SchemaVersion != SchemaV1 {
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
	body.WriteString(`html,body{width:100%;height:100%;margin:0;overflow:hidden}body{background:#111827}ast-deck{display:block;position:relative;width:1920px;height:1080px;overflow:hidden;transform-origin:top left;background:var(--ast-surface);color:var(--ast-ink)}ast-slide{display:none;position:absolute;inset:0;width:1920px;height:1080px;overflow:hidden}ast-slide[active]{display:block}ast-notes{display:none}</style></head><body>`)
	body.WriteString(`<ast-deck schema="1" ratio="16:9"`)
	if e.Print {
		body.WriteString(` print`)
	}
	body.WriteString(`>`)
	for _, slide := range scene.Slides {
		renderSlide(&body, slide)
	}
	body.WriteString(`</ast-deck><script>`)
	body.Write(runtimeJS)
	body.WriteString(`</script></body></html>`)
	return ExportResult{Bytes: body.Bytes(), Diagnostics: diagnostics}, nil
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
		if !safeAttributeName(key) || !safeCSSValue(theme[key]) {
			continue
		}
		out.WriteString("--ast-")
		out.WriteString(key)
		out.WriteByte(':')
		out.WriteString(theme[key])
		out.WriteByte(';')
	}
	out.WriteByte('}')
}

func safeCSSValue(value string) bool {
	return value != "" && !strings.ContainsAny(value, `;{}<>`)
}

func renderSlide(out *bytes.Buffer, slide Slide) {
	out.WriteString(`<ast-slide id="` + html.EscapeString(slide.ID) + `"`)
	if slide.Title != "" {
		out.WriteString(` title="` + html.EscapeString(slide.Title) + `"`)
	}
	out.WriteString(`>`)
	for _, node := range slide.Nodes {
		renderNode(out, node)
	}
	if slide.Notes != "" {
		out.WriteString(`<ast-notes>` + html.EscapeString(slide.Notes) + `</ast-notes>`)
	}
	out.WriteString(`</ast-slide>`)
}

func renderNode(out *bytes.Buffer, node Node) {
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
		if ok {
			writeAttr(out, key, value)
		}
	}
	if node.Table != nil {
		data, _ := json.Marshal(node.Table)
		writeAttr(out, "data-json", string(data))
	}
	if len(node.Series) > 0 {
		data, _ := json.Marshal(node.Series)
		writeAttr(out, "data-json", string(data))
	}
	out.WriteByte('>')
	out.WriteString(html.EscapeString(node.Text))
	for _, child := range node.Children {
		renderNode(out, child)
	}
	out.WriteString(`</` + tag + `>`)
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
