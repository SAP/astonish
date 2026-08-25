// Package pdfgen — chrome.go provides high-quality PDF generation by rendering
// markdown as styled HTML in a headless Chrome instance via go-rod.
//
// This produces professional output with full Unicode/emoji support, proper CSS
// typography, and print-quality layout. It requires a running browser instance
// managed by the browser.Manager.
package pdfgen

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"
)

// BrowserProvider is a minimal interface for obtaining a rod Browser instance.
// This is satisfied by browser.Manager's GetOrLaunch method, keeping pdfgen
// decoupled from the browser package.
type BrowserProvider interface {
	GetOrLaunch() (*rod.Browser, error)
}

const (
	defaultHTMLPrintTimeout = 30 * time.Second
	maxHTMLPrintTimeout     = 5 * time.Minute
)

// HTMLPrintOptions controls how a complete HTML document is printed by Chrome.
// Dimensions and margins are expressed in inches. ReadinessExpression may be a
// JavaScript value, promise, or zero-argument predicate; printing waits until it
// resolves to a truthy value. A zero Timeout uses a bounded default.
type HTMLPrintOptions struct {
	Landscape           bool
	PaperWidth          float64
	PaperHeight         float64
	MarginTop           float64
	MarginBottom        float64
	MarginLeft          float64
	MarginRight         float64
	PrintBackground     bool
	ReadinessExpression string
	Timeout             time.Duration
}

func (o HTMLPrintOptions) normalized() (HTMLPrintOptions, error) {
	if o.Timeout == 0 {
		o.Timeout = defaultHTMLPrintTimeout
	}
	if o.PaperWidth == 0 && o.PaperHeight == 0 {
		o.PaperWidth, o.PaperHeight = 8.5, 11
	}
	if o.Timeout < 0 || o.Timeout > maxHTMLPrintTimeout {
		return HTMLPrintOptions{}, fmt.Errorf("timeout must be between 0 and %s", maxHTMLPrintTimeout)
	}
	if err := validatePositiveDimension("paper width", o.PaperWidth); err != nil {
		return HTMLPrintOptions{}, err
	}
	if err := validatePositiveDimension("paper height", o.PaperHeight); err != nil {
		return HTMLPrintOptions{}, err
	}
	for name, value := range map[string]float64{
		"top margin": o.MarginTop, "bottom margin": o.MarginBottom,
		"left margin": o.MarginLeft, "right margin": o.MarginRight,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return HTMLPrintOptions{}, fmt.Errorf("%s must be a finite non-negative number", name)
		}
	}
	return o, nil
}

func validatePositiveDimension(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return fmt.Errorf("%s must be a finite positive number", name)
	}
	return nil
}

func (o HTMLPrintOptions) printParams() *proto.PagePrintToPDF {
	return &proto.PagePrintToPDF{
		Landscape:       o.Landscape,
		PrintBackground: o.PrintBackground,
		PaperWidth:      &o.PaperWidth,
		PaperHeight:     &o.PaperHeight,
		MarginTop:       &o.MarginTop,
		MarginBottom:    &o.MarginBottom,
		MarginLeft:      &o.MarginLeft,
		MarginRight:     &o.MarginRight,
	}
}

// ConvertMarkdownToPDFChrome converts markdown source bytes to a high-quality
// PDF using headless Chrome. It renders the markdown as styled HTML, then uses
// Chrome's Page.printToPDF for professional output with full Unicode, emoji,
// and CSS support.
//
// The browser parameter provides access to a running Chrome instance. If nil
// or if the browser is unavailable, returns an error.
func ConvertMarkdownToPDFChrome(source []byte, browser BrowserProvider) ([]byte, error) {
	if browser == nil {
		return nil, fmt.Errorf("no browser provider available")
	}

	// Step 1: Convert markdown to HTML using goldmark.
	htmlContent, err := markdownToHTML(source)
	if err != nil {
		return nil, fmt.Errorf("markdown to HTML conversion failed: %w", err)
	}

	// Step 2: Wrap in a full HTML document with CSS styling.
	fullHTML := wrapInHTMLTemplate(htmlContent)

	// Step 3: Render to PDF using Chrome. The readiness expression retains the
	// existing bounded, best-effort Mermaid wait before printing.
	return renderHTMLToPDFChrome(fullHTML, browser, HTMLPrintOptions{
		PaperWidth:          8.5,
		PaperHeight:         11,
		MarginTop:           0.6,
		MarginBottom:        0.6,
		MarginLeft:          0.75,
		MarginRight:         0.75,
		PrintBackground:     true,
		ReadinessExpression: mermaidReadinessExpression,
		Timeout:             defaultHTMLPrintTimeout,
	}, false)
}

// markdownToHTML converts markdown bytes to an HTML string using goldmark.
func markdownToHTML(source []byte) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(
			goldhtml.WithUnsafe(), // allow raw HTML in markdown
		),
	)

	var buf bytes.Buffer
	if err := md.Convert(source, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderHTMLToPDFChrome prints a complete, trusted HTML document in a dedicated
// headless Chrome page. It waits for page load, document fonts, and the optional
// readiness expression before printing. The timeout bounds the complete page
// lifecycle, including PDF generation.
func RenderHTMLToPDFChrome(html string, bp BrowserProvider, options HTMLPrintOptions) ([]byte, error) {
	return renderHTMLToPDFChrome(html, bp, options, true)
}

func renderHTMLToPDFChrome(html string, bp BrowserProvider, options HTMLPrintOptions, readinessRequired bool) ([]byte, error) {
	if bp == nil {
		return nil, fmt.Errorf("no browser provider available")
	}
	if strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("HTML document must not be empty")
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, fmt.Errorf("invalid HTML print options: %w", err)
	}

	b, err := bp.GetOrLaunch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	// Timeout the target creation too; after creation the page clone carries the
	// same total deadline for every subsequent operation.
	pg, err := b.Timeout(normalized.Timeout).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create browser page: %w", err)
	}
	defer func() { _ = pg.CancelTimeout().Close() }()

	if err := pg.SetDocumentContent(html); err != nil {
		return nil, fmt.Errorf("failed to set document content: %w", err)
	}
	if err := pg.WaitLoad(); err != nil {
		return nil, fmt.Errorf("failed to wait for page load: %w", err)
	}
	if _, err := pg.Eval(`() => document.fonts.ready`); err != nil {
		return nil, fmt.Errorf("failed to wait for fonts: %w", err)
	}
	if expression := strings.TrimSpace(normalized.ReadinessExpression); expression != "" {
		if err := pg.Wait(rod.Eval(`() => {
			const readiness = (` + expression + `);
			return typeof readiness === 'function' ? readiness() : readiness;
		}`).ByPromise()); err != nil {
			if readinessRequired {
				return nil, fmt.Errorf("failed to wait for HTML readiness: %w", err)
			}
			fmt.Printf("warning: mermaid wait failed: %v\n", err)
		}
	}

	reader, err := pg.PDF(normalized.printParams())
	if err != nil {
		return nil, fmt.Errorf("Chrome PDF generation failed: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF data: %w", err)
	}
	return data, nil
}

// ScreenshotOptions controls a full-page PNG capture of a trusted HTML document.
// Width/Height set the emulated device viewport so a fixed-size canvas (e.g. the
// 1920x1080 ast-deck) is captured exactly; zero values fall back to 1920x1080.
// ReadinessExpression and Timeout mirror HTMLPrintOptions semantics.
type ScreenshotOptions struct {
	Width               int
	Height              int
	ReadinessExpression string
	Timeout             time.Duration
	// Scale downscales the captured image relative to the emulated viewport
	// while leaving page LAYOUT untouched (the page still renders at Width x
	// Height, but the PNG is Scale*Width x Scale*Height). Use it to produce a
	// small thumbnail from a large fixed canvas without megabyte-sized output.
	// Values <= 0 mean 1.0 (no downscale). Values must be <= 1 (upscaling is
	// pointless and only bloats the PNG).
	Scale float64
}

func (o ScreenshotOptions) normalized() (ScreenshotOptions, error) {
	if o.Width <= 0 {
		o.Width = 1920
	}
	if o.Height <= 0 {
		o.Height = 1080
	}
	if o.Scale <= 0 {
		o.Scale = 1
	}
	if o.Scale > 1 {
		return ScreenshotOptions{}, fmt.Errorf("scale must be between 0 and 1")
	}
	// Apply scale to the effective viewport dimensions so the captured PNG is
	// (Width*Scale) x (Height*Scale). The page's own scaling logic (e.g.
	// ast-deck's DeckController) adapts the fixed canvas to fit the smaller
	// viewport — layout is correct but the output is lighter.
	if o.Scale < 1 {
		o.Width = max(1, int(float64(o.Width)*o.Scale))
		o.Height = max(1, int(float64(o.Height)*o.Scale))
	}
	if o.Timeout == 0 {
		o.Timeout = defaultHTMLPrintTimeout
	}
	if o.Timeout < 0 || o.Timeout > maxHTMLPrintTimeout {
		return ScreenshotOptions{}, fmt.Errorf("timeout must be between 0 and %s", maxHTMLPrintTimeout)
	}
	return o, nil
}

// RenderHTMLToPNGChrome renders a complete, trusted HTML document in a dedicated
// headless Chrome page and captures it as a PNG. It mirrors RenderHTMLToPDFChrome's
// page lifecycle (load, fonts.ready, optional readiness expression) but emulates a
// fixed device viewport and captures a full-page screenshot instead of printing.
// The timeout bounds the complete page lifecycle including capture.
func RenderHTMLToPNGChrome(html string, bp BrowserProvider, options ScreenshotOptions) ([]byte, error) {
	if bp == nil {
		return nil, fmt.Errorf("no browser provider available")
	}
	if strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("HTML document must not be empty")
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, fmt.Errorf("invalid screenshot options: %w", err)
	}

	b, err := bp.GetOrLaunch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	pg, err := b.Timeout(normalized.Timeout).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create browser page: %w", err)
	}
	defer func() { _ = pg.CancelTimeout().Close() }()

	// Emulate a device viewport matching the requested canvas. For thumbnails the
	// caller passes smaller Width/Height (e.g. 480×270) and the page's own
	// scaling logic (ast-deck's DeckController) shrinks the 1920×1080 fixed
	// canvas to fit, producing a small output PNG directly. DeviceScaleFactor
	// stays 1 (reliable across all Chrome versions).
	metrics := proto.EmulationSetDeviceMetricsOverride{
		Width:             normalized.Width,
		Height:            normalized.Height,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}
	if err := metrics.Call(pg); err != nil {
		return nil, fmt.Errorf("failed to set device metrics: %w", err)
	}

	if err := pg.SetDocumentContent(html); err != nil {
		return nil, fmt.Errorf("failed to set document content: %w", err)
	}
	if err := pg.WaitLoad(); err != nil {
		return nil, fmt.Errorf("failed to wait for page load: %w", err)
	}
	if _, err := pg.Eval(`() => document.fonts.ready`); err != nil {
		return nil, fmt.Errorf("failed to wait for fonts: %w", err)
	}
	if expression := strings.TrimSpace(normalized.ReadinessExpression); expression != "" {
		if err := pg.Wait(rod.Eval(`() => {
			const readiness = (` + expression + `);
			return typeof readiness === 'function' ? readiness() : readiness;
		}`).ByPromise()); err != nil {
			return nil, fmt.Errorf("failed to wait for HTML readiness: %w", err)
		}
	}

	// When Scale < 1, capture only the viewport (the runtime's transform scales
	// the fixed 1920×1080 canvas into the smaller viewport; a fullPage capture
	// would still see the DOM's intrinsic 1920×1080 layout). At Scale == 1, use
	// fullPage so nothing is clipped.
	fullPage := normalized.Scale >= 1
	data, err := pg.Screenshot(fullPage, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		return nil, fmt.Errorf("Chrome PNG capture failed: %w", err)
	}
	return data, nil
}

const mermaidReadinessExpression = `new Promise((resolve) => {
	if (window.__mermaidDone) { resolve(true); return; }
	let tries = 0;
	const iv = setInterval(() => {
		tries++;
		if (window.__mermaidDone || tries > 100) { clearInterval(iv); resolve(true); }
	}, 50);
})`

// wrapInHTMLTemplate wraps an HTML fragment in a complete HTML document with
// CSS styling optimized for print/PDF output. Includes mermaid.js for
// rendering mermaid fenced code blocks as SVG diagrams.
func wrapInHTMLTemplate(body string) string {
	return fmt.Sprintf("<!DOCTYPE html>\n"+
		"<html lang=\"en\">\n<head>\n<meta charset=\"UTF-8\">\n"+
		"<style>\n%s\n</style>\n"+
		"<script src=\"https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js\"></script>\n"+
		"<script>\n%s\n</script>\n"+
		"</head>\n<body>\n%s\n</body>\n</html>",
		pdfCSS, mermaidInitScript, body)
}

// mermaidInitScript is the inline JavaScript that converts
// <pre><code class="language-mermaid">...</code></pre> blocks (produced by
// goldmark from fenced mermaid code blocks) into rendered SVG diagrams.
// It runs on DOMContentLoaded and sets window.__mermaidDone when complete,
// which the Go code polls for before printing to PDF.
const mermaidInitScript = `
document.addEventListener('DOMContentLoaded', async function() {
  var codeBlocks = document.querySelectorAll('code.language-mermaid');
  if (codeBlocks.length === 0) {
    window.__mermaidDone = true;
    return;
  }

  mermaid.initialize({
    startOnLoad: false,
    theme: 'neutral',
    fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif',
    securityLevel: 'loose'
  });

  try {
    for (var i = 0; i < codeBlocks.length; i++) {
      var codeEl = codeBlocks[i];
      var preEl = codeEl.parentElement;
      var source = codeEl.textContent || '';
      var id = 'mermaid-pdf-' + i;

      var result = await mermaid.render(id, source.trim());
      var container = document.createElement('div');
      container.className = 'mermaid-diagram';
      container.innerHTML = result.svg;

      if (preEl && preEl.tagName === 'PRE') {
        preEl.replaceWith(container);
      } else {
        codeEl.replaceWith(container);
      }
    }
  } catch (e) {
    console.error('Mermaid rendering failed:', e);
  }

  window.__mermaidDone = true;
});
`

// pdfCSS is the embedded stylesheet for PDF output. It provides clean
// typography, compact list spacing, styled tables, and code blocks.
const pdfCSS = `
/* Reset and base */
*, *::before, *::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

/* Page layout */
@page {
  size: A4;
}

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji";
  font-size: 11pt;
  line-height: 1.5;
  color: #1a1a1a;
  max-width: 100%;
}

/* Headings */
h1, h2, h3, h4, h5, h6 {
  margin-top: 1.2em;
  margin-bottom: 0.4em;
  line-height: 1.25;
  color: #111;
  page-break-after: avoid;
}

h1 { font-size: 22pt; border-bottom: 2px solid #e0e0e0; padding-bottom: 0.3em; }
h2 { font-size: 18pt; border-bottom: 1px solid #e8e8e8; padding-bottom: 0.2em; }
h3 { font-size: 14pt; }
h4 { font-size: 12pt; }
h5 { font-size: 11pt; font-weight: 600; }
h6 { font-size: 10pt; font-weight: 600; color: #555; }

/* First heading: no top margin */
body > h1:first-child,
body > h2:first-child,
body > h3:first-child {
  margin-top: 0;
}

/* Paragraphs */
p {
  margin-top: 0.5em;
  margin-bottom: 0.5em;
}

/* Lists — compact spacing */
ul, ol {
  margin-top: 0.3em;
  margin-bottom: 0.5em;
  padding-left: 1.8em;
}

li {
  margin-bottom: 0.15em;
  line-height: 1.45;
}

li > ul, li > ol {
  margin-top: 0.1em;
  margin-bottom: 0.1em;
}

/* Tables */
table {
  border-collapse: collapse;
  width: 100%;
  margin-top: 0.5em;
  margin-bottom: 0.8em;
  font-size: 10pt;
  page-break-inside: auto;
}

th, td {
  border: 1px solid #d0d0d0;
  padding: 6px 10px;
  text-align: left;
  vertical-align: top;
}

th {
  background-color: #f5f5f5;
  font-weight: 600;
  color: #333;
}

tr:nth-child(even) {
  background-color: #fafafa;
}

/* Code */
code {
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
  font-size: 0.9em;
  background-color: #f4f4f4;
  padding: 0.15em 0.35em;
  border-radius: 3px;
  color: #c7254e;
}

pre {
  background-color: #f6f6f6;
  border: 1px solid #e0e0e0;
  border-radius: 5px;
  padding: 12px 16px;
  margin-top: 0.5em;
  margin-bottom: 0.8em;
  overflow-x: auto;
  page-break-inside: avoid;
}

pre code {
  background: none;
  padding: 0;
  border-radius: 0;
  color: inherit;
  font-size: 9pt;
  line-height: 1.45;
}

/* Blockquotes */
blockquote {
  border-left: 4px solid #d0d0d0;
  margin: 0.5em 0;
  padding: 0.4em 1em;
  color: #555;
  background-color: #fafafa;
}

blockquote p {
  margin: 0.2em 0;
}

/* Horizontal rules */
hr {
  border: none;
  border-top: 1px solid #e0e0e0;
  margin: 1.2em 0;
}

/* Links */
a {
  color: #0066cc;
  text-decoration: none;
}

/* Bold and emphasis */
strong { font-weight: 600; }
em { font-style: italic; }

/* Images */
img {
  max-width: 100%;
  height: auto;
}

/* Print-specific */
@media print {
  body { color: #000; }
  a { color: #0066cc; }
  h1, h2, h3 { page-break-after: avoid; }
  table, pre, blockquote { page-break-inside: avoid; }
}

/* Mermaid diagrams */
.mermaid-diagram {
  display: flex;
  justify-content: center;
  margin: 0.8em 0;
  padding: 16px;
  page-break-inside: avoid;
}

.mermaid-diagram svg {
  max-width: 100%;
  height: auto;
}
`
