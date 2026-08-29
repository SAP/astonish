package slides

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/pdfgen"
)

// thumbWorkers limits how many archetype thumbnails render concurrently inside
// a single import so we don't overwhelm headless Chrome.
const thumbWorkers = 4

// thumbnailSampleTitle and thumbnailSampleBody fill the {{TITLE}}/{{BODY}}
// placeholders so a baked thumbnail reads like a real slide rather than showing
// raw mustache tokens.
const (
	thumbnailSampleTitle = "Title"
	thumbnailSampleBody  = "Body text"
)

// archetypeThumbScale downscales the baked archetype PNG relative to the full
// 1920x1080 canvas. Template picker cards are small, so a 1/6 scale (320x180)
// is retina-ready while producing a truly small PNG (~5-15 KB). Layout is
// unaffected (the page still renders at the full canvas; the captured viewport
// is just smaller).
const archetypeThumbScale = 1.0 / 6.0

// thumbnailPNGRenderer captures a self-contained HTML document as PNG bytes. It
// matches pdfgen.RenderHTMLToPNGChrome so production uses headless Chrome while
// tests inject a fake without a browser (mirroring PDFExporter.Render).
type thumbnailPNGRenderer func(html string, bp pdfgen.BrowserProvider, opts pdfgen.ScreenshotOptions) ([]byte, error)

// thumbnailOptions bundles the inputs GenerateArchetypeThumbnails needs plus an
// injectable renderer so the SceneGraph-building/placeholder path is testable.
type thumbnailOptions struct {
	RuntimeJS []byte
	Browser   pdfgen.BrowserProvider
	Render    thumbnailPNGRenderer
	Timeout   time.Duration
}

// GenerateArchetypeThumbnails rasterizes each archetype in tmpl to a static PNG
// once (at .pptx import time) and records it as a content-addressed asset on the
// template. For every archetype it builds a single-slide SceneGraph (placeholders
// filled with sample text), renders the ast-deck HTML, screenshots it to PNG, and
// stores the data URI at Assets["thumb/<kind>"] while setting
// Archetype.ThumbnailRef to that key.
//
// It is BEST-EFFORT: any per-archetype failure is logged and skipped (that
// archetype keeps ThumbnailRef="" and falls back to a live render in the picker);
// a nil browser/runtime disables generation entirely. It never returns an error
// so a template still imports when no browser is available.
func GenerateArchetypeThumbnails(ctx context.Context, tmpl *themes.Template, runtimeJS []byte, browser pdfgen.BrowserProvider) {
	generateArchetypeThumbnails(ctx, tmpl, thumbnailOptions{
		RuntimeJS: runtimeJS,
		Browser:   browser,
		Render:    pdfgen.RenderHTMLToPNGChrome,
		Timeout:   90 * time.Second,
	})
}

func generateArchetypeThumbnails(ctx context.Context, tmpl *themes.Template, opts thumbnailOptions) {
	if tmpl == nil {
		return
	}
	render := opts.Render
	if render == nil {
		render = pdfgen.RenderHTMLToPNGChrome
	}
	if opts.Browser == nil || len(opts.RuntimeJS) == 0 {
		slog.Warn("slides thumbnail generation skipped: no browser or runtime",
			"template", tmpl.Name, "hasBrowser", opts.Browser != nil, "runtimeBytes", len(opts.RuntimeJS))
		return
	}
	if tmpl.Assets == nil {
		tmpl.Assets = map[string]string{}
	}

	type result struct {
		index int
		ref   string
		png   []byte
		err   error
	}

	n := len(tmpl.Archetypes)
	results := make([]result, n)

	// Prepare work items (scene HTML can be built cheaply outside the pool).
	var wg sync.WaitGroup
	sem := make(chan struct{}, thumbWorkers)
	for i := range tmpl.Archetypes {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			arch := tmpl.Archetypes[idx]
			png, err := renderArchetypeThumbnail(arch, tmpl.Tokens, tmpl.Assets, opts.RuntimeJS, opts.Browser, render, opts.Timeout)
			results[idx] = result{index: idx, ref: "thumb/" + arch.Kind, png: png, err: err}
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			slog.Warn("slides thumbnail generation failed for archetype",
				"template", tmpl.Name, "kind", tmpl.Archetypes[r.index].Kind, "error", r.err)
			continue
		}
		tmpl.Assets[r.ref] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(r.png)
		tmpl.Archetypes[r.index].ThumbnailRef = r.ref
	}
}

// renderArchetypeThumbnail builds a one-slide SceneGraph for arch (placeholders
// substituted with sample text), exports it to self-contained ast-deck HTML, and
// captures a PNG via the injected renderer.
func renderArchetypeThumbnail(
	arch themes.Archetype,
	tokens, assets map[string]string,
	runtimeJS []byte,
	browser pdfgen.BrowserProvider,
	render thumbnailPNGRenderer,
	timeout time.Duration,
) ([]byte, error) {
	scene, err := archetypeScene(arch, tokens, assets)
	if err != nil {
		return nil, err
	}
	htmlResult, err := (HTMLExporter{RuntimeJS: runtimeJS, Print: false}).Export(scene)
	if err != nil {
		return nil, fmt.Errorf("export archetype HTML: %w", err)
	}
	png, err := render(string(htmlResult.Bytes), browser, pdfgen.ScreenshotOptions{
		Width:               CanvasWidth,
		Height:              CanvasHeight,
		Scale:               archetypeThumbScale,
		ReadinessExpression: SlidesReadinessExpression,
		Timeout:             timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("render archetype PNG: %w", err)
	}
	return png, nil
}

// archetypeScene parses an archetype's markup (with {{TITLE}}/{{BODY}} replaced by
// sample text) into a single-slide SceneGraph carrying the template theme + assets.
func archetypeScene(arch themes.Archetype, tokens, assets map[string]string) (SceneGraph, error) {
	markup := fillPlaceholders(arch.Markup)
	slide, diags, err := ParseSlide(markup)
	if err != nil {
		return SceneGraph{}, fmt.Errorf("parse archetype markup: %w", err)
	}
	if HasErrors(diags) {
		return SceneGraph{}, fmt.Errorf("archetype markup has validation errors")
	}
	return SceneGraph{
		SchemaVersion: SchemaV2,
		Title:         arch.Title,
		Theme:         tokens,
		Assets:        assets,
		Slides:        []Slide{slide},
	}, nil
}

// fillPlaceholders swaps the archetype text holes for short sample copy so the
// thumbnail reads naturally instead of showing {{TITLE}}/{{BODY}}.
func fillPlaceholders(markup string) string {
	r := strings.NewReplacer(
		"{{TITLE}}", thumbnailSampleTitle,
		"{{BODY}}", thumbnailSampleBody,
	)
	return r.Replace(markup)
}
