package slides

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/SAP/astonish/pkg/pdfgen"
)

// deckSlideThumbVersion is bumped whenever the baked-thumbnail FORMAT changes
// (e.g. resolution/scale) so already-finished decks re-bake once instead of
// keeping stale images. It is embedded in the asset key, so a version bump
// makes the idempotency check (ThumbnailRef == current ref) miss and re-render.
const deckSlideThumbVersion = "v2"

// deckSlideThumbRef returns the deck Assets key holding the baked PNG for the
// slide at position i. It mirrors the template "thumb/<kind>" convention but is
// keyed by slide position (deck slides have no stable role kind) and carries a
// format version so a scale/format change invalidates old baked assets: a slide
// still pointing at an old-version ref fails the idempotency check and re-bakes
// to the current key. The superseded asset is left orphaned in the manifest
// (never served once ThumbnailRef moves, and stripped from the list DTO).
func deckSlideThumbRef(position int) string {
	return fmt.Sprintf("slidethumb/%s/%d", deckSlideThumbVersion, position)
}

// GenerateDeckThumbnails rasterizes each slide of a finished deck to a static
// PNG once and records it as a content-addressed asset on the deck. For every
// slide it builds a single-slide SceneGraph carrying the deck theme + assets,
// renders the ast-deck HTML, screenshots it to PNG, stores the data URI at
// Assets["slidethumb/<position>"], and sets SlideContent.ThumbnailRef to that
// key.
//
// It is BEST-EFFORT and idempotent: a slide whose ThumbnailRef is already set
// AND whose asset still exists is skipped (no re-render); any per-slide failure
// is logged and skipped (that slide keeps ThumbnailRef=""); a nil browser or
// empty runtime disables generation entirely. It returns an error only for a
// fatal load failure (deck/scene unreadable); the caller treats even that as
// soft (log + continue) so finishing a deck never fails over thumbnails.
func GenerateDeckThumbnails(ctx context.Context, svc Service, slug string, runtimeJS []byte, browser pdfgen.BrowserProvider) error {
	return generateDeckThumbnails(ctx, svc, slug, deckThumbnailOptions{
		RuntimeJS: runtimeJS,
		Browser:   browser,
		Render:    pdfgen.RenderHTMLToPNGChrome,
		Timeout:   90 * time.Second,
	})
}

// deckThumbnailOptions bundles the inputs the deck baker needs plus an
// injectable renderer so the SceneGraph-building path is unit-testable without
// a browser (mirrors thumbnailOptions for archetypes).
type deckThumbnailOptions struct {
	RuntimeJS []byte
	Browser   pdfgen.BrowserProvider
	Render    thumbnailPNGRenderer
	Timeout   time.Duration
}

func generateDeckThumbnails(ctx context.Context, svc Service, slug string, opts deckThumbnailOptions) error {
	render := opts.Render
	if render == nil {
		render = pdfgen.RenderHTMLToPNGChrome
	}
	if opts.Browser == nil || len(opts.RuntimeJS) == 0 {
		slog.Warn("deck slide thumbnail generation skipped: no browser or runtime",
			"deck", slug, "hasBrowser", opts.Browser != nil, "runtimeBytes", len(opts.RuntimeJS))
		return nil
	}

	deck, slidesContent, err := svc.Deck(ctx, slug)
	if err != nil {
		return fmt.Errorf("load deck: %w", err)
	}
	scene, _, err := svc.Scene(ctx, slug)
	if err != nil {
		return fmt.Errorf("load scene: %w", err)
	}
	// Scene.Slides is built in the same position order as ListSlides (both
	// ordered by position), so index i lines up with slidesContent[i].
	if len(scene.Slides) != len(slidesContent) {
		slog.Warn("deck slide thumbnail generation skipped: scene/slide count mismatch",
			"deck", slug, "sceneSlides", len(scene.Slides), "storedSlides", len(slidesContent))
		return nil
	}

	for i := range slidesContent {
		persisted := slidesContent[i]
		ref := deckSlideThumbRef(persisted.Position)
		// Idempotent: skip when this slide already has a baked thumbnail whose
		// asset is still present. Re-running review_deck therefore does not
		// re-render unchanged slides.
		if persisted.ThumbnailRef == ref {
			if _, ok := deck.Assets[ref]; ok {
				continue
			}
		}

		png, rErr := renderDeckSlideThumbnail(scene.Slides[i], deck.Theme, deck.Assets, opts.RuntimeJS, opts.Browser, render, opts.Timeout)
		if rErr != nil {
			// Best-effort: log and continue; leave this slide's ThumbnailRef empty.
			slog.Warn("deck slide thumbnail generation failed for slide",
				"deck", slug, "position", persisted.Position, "error", rErr)
			continue
		}
		dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		if _, aErr := svc.AddDeckAsset(ctx, slug, ref, dataURI); aErr != nil {
			slog.Warn("deck slide thumbnail asset persist failed",
				"deck", slug, "position", persisted.Position, "error", aErr)
			continue
		}
		// Persist the ref on the slide row so the deck response surfaces it and
		// the HTTP endpoint can resolve the asset. Re-use the existing content
		// so the upsert does not disturb markup/notes/title/position.
		persisted.ThumbnailRef = ref
		if uErr := svc.Store.UpsertSlide(ctx, persisted); uErr != nil {
			slog.Warn("deck slide thumbnail ref persist failed",
				"deck", slug, "position", persisted.Position, "error", uErr)
			// The asset is stored but the ref is not; leave in-memory ref set so
			// a retry re-bakes deterministically (asset overwrite is a no-op).
			continue
		}
		// Keep the local deck.Assets copy in sync so a later slide's idempotency
		// check and count stay consistent within this pass.
		if deck.Assets == nil {
			deck.Assets = map[string]string{}
		}
		deck.Assets[ref] = dataURI
	}
	return nil
}

// deckSlideThumbScale downscales the baked per-slide PNG relative to the full
// 1920x1080 canvas. The Slides view renders each tile at ~160px wide, so a
// 1/6 scale (320x180) is retina-ready at that display size while producing a
// truly small PNG (~5-15 KB) — the list view was slow because every deck card
// was downloading a multi-megabyte full-canvas PNG.
const deckSlideThumbScale = 1.0 / 6.0

// renderDeckSlideThumbnail builds a one-slide SceneGraph for an already-parsed
// slide (carrying the deck theme + assets), exports it to self-contained
// ast-deck HTML, and captures a PNG via the injected renderer.
func renderDeckSlideThumbnail(
	slide Slide,
	theme, assets map[string]string,
	runtimeJS []byte,
	browser pdfgen.BrowserProvider,
	render thumbnailPNGRenderer,
	timeout time.Duration,
) ([]byte, error) {
	scene := sceneForSlide(slide, theme, assets)
	htmlResult, err := (HTMLExporter{RuntimeJS: runtimeJS, Print: false}).Export(scene)
	if err != nil {
		return nil, fmt.Errorf("export slide HTML: %w", err)
	}
	png, err := render(string(htmlResult.Bytes), browser, pdfgen.ScreenshotOptions{
		Width:               CanvasWidth,
		Height:              CanvasHeight,
		Scale:               deckSlideThumbScale,
		ReadinessExpression: SlidesReadinessExpression,
		Timeout:             timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("render slide PNG: %w", err)
	}
	return png, nil
}

// sceneForSlide wraps a single parsed slide in a SceneGraph carrying the deck's
// theme + assets so it renders exactly as it does inside the full deck.
func sceneForSlide(slide Slide, theme, assets map[string]string) SceneGraph {
	return SceneGraph{
		SchemaVersion: SchemaV2,
		Theme:         theme,
		Assets:        assets,
		Slides:        []Slide{slide},
	}
}
