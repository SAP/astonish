package slides

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/SAP/astonish/pkg/docs/slides/components"
)

const MaxMarkupBytes = 1 << 20

// ParseSlide parses one inert, allowlisted ast-slide fragment into a normalized slide.
func ParseSlide(markup string) (Slide, []Diagnostic, error) {
	if len(markup) > MaxMarkupBytes {
		return Slide{}, nil, fmt.Errorf("slide markup exceeds %d bytes", MaxMarkupBytes)
	}
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return Slide{}, nil, fmt.Errorf("parse slide markup: %w", err)
	}
	container := doc
	for _, name := range []string{"html", "body"} {
		for c := container.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == name {
				container = c
				break
			}
		}
	}
	var root *html.Node
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if root != nil {
			return Slide{}, nil, fmt.Errorf("markup must contain exactly one ast-slide root")
		}
		root = c
	}
	if root == nil || root.Data != "ast-slide" {
		return Slide{}, nil, fmt.Errorf("markup must contain exactly one ast-slide root")
	}
	if err := validateElement(root, ""); err != nil {
		return Slide{}, nil, err
	}
	out := Slide{ID: attr(root, "id"), Title: attr(root, "title")}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		if child.Data == "ast-notes" {
			out.Notes = strings.TrimSpace(textContent(child))
			continue
		}
		if child.Data == "script" {
			continue
		}
		n, err := nodeFromHTML(child)
		if err != nil {
			return Slide{}, nil, err
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out, ValidateSlide(out), nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
func validateElement(n *html.Node, parent string) error {
	schema, ok := components.SchemaV1(n.Data)
	if !ok {
		return fmt.Errorf("element %q is not allowed", n.Data)
	}
	allowed := map[string]bool{}
	for _, v := range schema.Required {
		allowed[v] = true
	}
	for _, v := range schema.Optional {
		allowed[v] = true
	}
	seen := map[string]bool{}
	for _, a := range n.Attr {
		if strings.HasPrefix(strings.ToLower(a.Key), "on") || a.Key == "style" || !allowed[a.Key] {
			return fmt.Errorf("attribute %q is not allowed on %s", a.Key, n.Data)
		}
		seen[a.Key] = true
	}
	for _, r := range schema.Required {
		if !seen[r] {
			return fmt.Errorf("%s requires attribute %q", n.Data, r)
		}
	}
	if n.Data == "script" && attr(n, "type") != "application/json" {
		return fmt.Errorf("only application/json script blocks are allowed")
	}
	if parent != "" && n.Data != "script" {
		def, _ := components.LookupV1(parent)
		ok := false
		for _, v := range def.AllowedChildren {
			if v == n.Data {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s is not allowed inside %s", n.Data, parent)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if err := validateElement(c, n.Data); err != nil {
				return err
			}
		}
	}
	if n.Data == "script" {
		var v any
		dec := json.NewDecoder(strings.NewReader(textContent(n)))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("invalid JSON data block: %w", err)
		}
		if err := ensureJSONEOF(dec); err != nil {
			return err
		}
	}
	return nil
}
func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("JSON data block has trailing content")
	}
	return nil
}
func nodeFromHTML(n *html.Node) (Node, error) {
	g := Geometry{}
	if def, _ := components.LookupV1(n.Data); def.RequiresGeometry {
		var err error
		if g.X, err = parseIntAttr(n, "x"); err != nil {
			return Node{}, err
		}
		if g.Y, err = parseIntAttr(n, "y"); err != nil {
			return Node{}, err
		}
		if g.W, err = parseIntAttr(n, "w"); err != nil {
			return Node{}, err
		}
		if g.H, err = parseIntAttr(n, "h"); err != nil {
			return Node{}, err
		}
	}
	out := Node{ID: attr(n, "id"), Type: strings.TrimPrefix(n.Data, "ast-"), Geometry: g, Props: map[string]any{}}
	for _, a := range n.Attr {
		switch a.Key {
		case "id", "x", "y", "w", "h":
			// geometry/identity handled separately
		case "rot":
			v, err := strconv.Atoi(strings.TrimSpace(a.Val))
			if err != nil {
				return Node{}, fmt.Errorf("%s attribute %q must be an integer", n.Data, a.Key)
			}
			out.Rot = v
		case "opacity":
			v, err := strconv.ParseFloat(strings.TrimSpace(a.Val), 64)
			if err != nil {
				return Node{}, fmt.Errorf("%s attribute %q must be a number", n.Data, a.Key)
			}
			out.Opacity = v
		case "fill":
			out.Fill = a.Val
		case "line":
			out.Line = a.Val
		case "line-dash":
			out.Dash = a.Val
		case "geom":
			out.Geom = a.Val
		case "path":
			out.Path = a.Val
		default:
			out.Props[a.Key] = a.Val
		}
	}
	if err := collectChildren(n, &out); err != nil {
		return Node{}, err
	}
	// Text content is only meaningful when there are no rich runs; a leaf's own
	// text still populates Text for the single-run fallback path.
	out.Text = strings.TrimSpace(directText(n))
	return out, nil
}

// collectChildren walks element children of n, populating Node.Runs from
// ast-run elements, Node.Gradient from a gradient JSON script block, and
// Node.Children from nested ast-* elements.
func collectChildren(n *html.Node, out *Node) error {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch {
		case c.Data == "ast-run":
			out.Runs = append(out.Runs, runFromHTML(c))
		case c.Data == "script":
			grad, err := gradientFromScript(c)
			if err != nil {
				return err
			}
			if grad != nil {
				out.Gradient = grad
			}
		case strings.HasPrefix(c.Data, "ast-"):
			child, err := nodeFromHTML(c)
			if err != nil {
				return err
			}
			out.Children = append(out.Children, child)
		}
	}
	return nil
}

// directText returns the concatenated text of n excluding text inside nested
// ast-* elements so a parent's Text does not absorb its rich runs' content.
func directText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.TextNode:
			b.WriteString(c.Data)
		case c.Type == html.ElementNode && (strings.HasPrefix(c.Data, "ast-") || c.Data == "script"):
			// skip: rich runs and data blocks are captured separately
		case c.Type == html.ElementNode:
			b.WriteString(textContent(c))
		}
	}
	return b.String()
}

func runFromHTML(n *html.Node) TextRun {
	// Preserve the run's authored text verbatim (do NOT TrimSpace): the model
	// emits deliberate whitespace-only separator runs like <ast-run>\n\n</ast-run>
	// between items, and trimming would destroy the only line-break signal.
	// Renderers set white-space:pre-wrap on ast-text so these newlines display.
	run := TextRun{Text: textContent(n)}
	for _, a := range n.Attr {
		switch a.Key {
		case "b":
			run.Bold = true
		case "i":
			run.Italic = true
		case "u":
			run.Underline = true
		case "color":
			run.Color = a.Val
		case "font":
			run.Font = a.Val
		case "weight":
			run.Weight = a.Val
		case "size":
			if v, err := strconv.Atoi(strings.TrimSpace(a.Val)); err == nil {
				run.Size = v
			}
		}
	}
	return run
}

func gradientFromScript(n *html.Node) (*Gradient, error) {
	raw := strings.TrimSpace(textContent(n))
	if raw == "" {
		return nil, nil
	}
	var g Gradient
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("invalid gradient JSON: %w", err)
	}
	return &g, nil
}
func parseIntAttr(n *html.Node, key string) (int, error) {
	v := attr(n, key)
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s %q must be an integer", n.Data, key)
	}
	return i, nil
}
