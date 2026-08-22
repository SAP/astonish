package pdfgen

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

func TestMarkdownToHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:  "heading",
			input: "# Hello World",
			contains: []string{
				"<h1>Hello World</h1>",
			},
		},
		{
			name:  "bullet list",
			input: "- Item one\n- Item two\n- Item three",
			contains: []string{
				"<ul>",
				"<li>Item one</li>",
				"<li>Item two</li>",
				"</ul>",
			},
		},
		{
			name:  "GFM table",
			input: "| Name | Value |\n|------|-------|\n| A    | 1     |",
			contains: []string{
				"<table>",
				"<th>Name</th>",
				"<td>A</td>",
				"</table>",
			},
		},
		{
			name:  "bold and italic",
			input: "This is **bold** and *italic*.",
			contains: []string{
				"<strong>bold</strong>",
				"<em>italic</em>",
			},
		},
		{
			name:  "code block",
			input: "```go\nfunc main() {}\n```",
			contains: []string{
				"<pre><code",
				"func main()",
				"</code></pre>",
			},
		},
		{
			name:  "ampersand and quotes preserved",
			input: "Tom & Jerry said \"hello\"",
			contains: []string{
				"Tom &amp; Jerry", // goldmark HTML-encodes & for valid HTML
				"&quot;hello&quot;",
			},
		},
		{
			name:  "emoji preserved in HTML",
			input: "🚀 Launch day ⚠️ Warning",
			contains: []string{
				"🚀",
				"⚠️",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html, err := markdownToHTML([]byte(tt.input))
			if err != nil {
				t.Fatalf("markdownToHTML failed: %v", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(html, want) {
					t.Errorf("markdownToHTML output missing %q\ngot: %s", want, html)
				}
			}
		})
	}
}

func TestWrapInHTMLTemplate(t *testing.T) {
	body := "<h1>Test</h1><p>Hello world</p>"
	result := wrapInHTMLTemplate(body)

	checks := []string{
		"<!DOCTYPE html>",
		"<html lang=\"en\">",
		"<meta charset=\"UTF-8\">",
		"<style>",
		"font-family:",
		"@page",
		body,
		"</body>",
		"</html>",
	}

	for _, want := range checks {
		if !strings.Contains(result, want) {
			t.Errorf("wrapInHTMLTemplate missing %q", want)
		}
	}
}

func TestWrapInHTMLTemplate_MermaidSupport(t *testing.T) {
	result := wrapInHTMLTemplate("<p>test</p>")

	checks := []string{
		"mermaid",                  // mermaid.js CDN reference
		"mermaid.min.js",           // script source
		"code.language-mermaid",    // selector for mermaid code blocks
		"mermaid.initialize",       // initialization call
		"mermaid.render",           // render call
		"window.__mermaidDone",     // completion signal
		".mermaid-diagram",         // CSS for rendered diagrams
		"page-break-inside: avoid", // diagrams avoid page breaks
	}

	for _, want := range checks {
		if !strings.Contains(result, want) {
			t.Errorf("wrapInHTMLTemplate missing mermaid support: %q not found", want)
		}
	}
}

func TestConvertMarkdownToPDFChrome_NilBrowser(t *testing.T) {
	_, err := ConvertMarkdownToPDFChrome([]byte("# Test"), nil)
	if err == nil {
		t.Fatal("expected error for nil browser, got nil")
	}
	if !strings.Contains(err.Error(), "no browser provider") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHTMLPrintOptionsNormalizedDefaults(t *testing.T) {
	got, err := (HTMLPrintOptions{}).normalized()
	if err != nil {
		t.Fatalf("normalized returned error: %v", err)
	}
	if got.PaperWidth != 8.5 || got.PaperHeight != 11 {
		t.Fatalf("unexpected default paper size: %gx%g", got.PaperWidth, got.PaperHeight)
	}
	if got.Timeout != defaultHTMLPrintTimeout {
		t.Fatalf("unexpected default timeout: %s", got.Timeout)
	}
}

func TestHTMLPrintOptionsPrintParams(t *testing.T) {
	opts := HTMLPrintOptions{
		Landscape:       true,
		PaperWidth:      13.333,
		PaperHeight:     7.5,
		MarginTop:       0.1,
		MarginBottom:    0.2,
		MarginLeft:      0.3,
		MarginRight:     0.4,
		PrintBackground: true,
	}
	params := opts.printParams()
	if !params.Landscape || !params.PrintBackground {
		t.Fatal("landscape and print background were not propagated")
	}
	values := []struct {
		name string
		got  *float64
		want float64
	}{
		{"width", params.PaperWidth, 13.333}, {"height", params.PaperHeight, 7.5},
		{"top", params.MarginTop, 0.1}, {"bottom", params.MarginBottom, 0.2},
		{"left", params.MarginLeft, 0.3}, {"right", params.MarginRight, 0.4},
	}
	for _, value := range values {
		if value.got == nil || *value.got != value.want {
			t.Errorf("%s: got %v, want %g", value.name, value.got, value.want)
		}
	}
}

func TestHTMLPrintOptionsValidation(t *testing.T) {
	tests := []struct {
		name string
		opts HTMLPrintOptions
	}{
		{"partial paper size", HTMLPrintOptions{PaperWidth: 10}},
		{"negative margin", HTMLPrintOptions{PaperWidth: 10, PaperHeight: 5, MarginLeft: -1}},
		{"non-finite width", HTMLPrintOptions{PaperWidth: math.Inf(1), PaperHeight: 5}},
		{"negative timeout", HTMLPrintOptions{PaperWidth: 10, PaperHeight: 5, Timeout: -time.Second}},
		{"excessive timeout", HTMLPrintOptions{PaperWidth: 10, PaperHeight: 5, Timeout: maxHTMLPrintTimeout + time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.opts.normalized(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRenderHTMLToPDFChromeValidationWithoutBrowser(t *testing.T) {
	if _, err := RenderHTMLToPDFChrome("<!doctype html><title>test</title>", nil, HTMLPrintOptions{}); err == nil || !strings.Contains(err.Error(), "no browser provider") {
		t.Fatalf("unexpected nil provider error: %v", err)
	}

	provider := stubBrowserProvider{}
	if _, err := RenderHTMLToPDFChrome(" \n\t", provider, HTMLPrintOptions{}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected empty HTML error: %v", err)
	}
	if _, err := RenderHTMLToPDFChrome("<!doctype html><title>test</title>", provider, HTMLPrintOptions{PaperWidth: 10}); err == nil || !strings.Contains(err.Error(), "invalid HTML print options") {
		t.Fatalf("unexpected options error: %v", err)
	}
}

type stubBrowserProvider struct {
	browser *rod.Browser
	err     error
}

func (p stubBrowserProvider) GetOrLaunch() (*rod.Browser, error) {
	return p.browser, p.err
}
