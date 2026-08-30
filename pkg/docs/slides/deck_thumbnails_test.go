package slides

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/pdfgen"
)

// seedDeckWithSlides creates a deck via the service and writes n valid slides,
// returning the service (backed by an in-memory store) and the deck slug.
func seedDeckWithSlides(t *testing.T, n int) (Service, string) {
	t.Helper()
	ctx := context.Background()
	svc := Service{Store: &memoryDocsStore{}}
	deck, err := svc.CreateDeck(ctx, "deck-thumbs", "Deck Thumbs", "", map[string]string{"surface": "#FFFFFF"})
	if err != nil {
		t.Fatalf("create deck: %v", err)
	}
	for i := 0; i < n; i++ {
		markup := `<ast-slide id="s` + string(rune('a'+i)) + `"><ast-text id="t" x="100" y="100" w="800" h="120">Slide</ast-text></ast-slide>`
		if _, diags, err := svc.WriteSlide(ctx, deck.Slug, i, markup, ""); err != nil || HasErrors(diags) {
			t.Fatalf("write slide %d: diags=%#v err=%v", i, diags, err)
		}
	}
	return svc, deck.Slug
}

func TestGenerateDeckThumbnailsBakesAssetsAndRefs(t *testing.T) {
	ctx := context.Background()
	svc, slug := seedDeckWithSlides(t, 3)

	var seenHTML []string
	opts := deckThumbnailOptions{
		RuntimeJS: []byte("/* runtime */"),
		Browser:   stubProvider{},
		Render: func(html string, _ pdfgen.BrowserProvider, sopts pdfgen.ScreenshotOptions) ([]byte, error) {
			seenHTML = append(seenHTML, html)
			if sopts.Width != CanvasWidth || sopts.Height != CanvasHeight {
				t.Errorf("unexpected viewport: %dx%d", sopts.Width, sopts.Height)
			}
			if sopts.Scale != deckSlideThumbScale {
				t.Errorf("thumbnail scale not propagated: got %v want %v", sopts.Scale, deckSlideThumbScale)
			}
			if sopts.ReadinessExpression != SlidesReadinessExpression {
				t.Errorf("readiness expression not propagated: %q", sopts.ReadinessExpression)
			}
			return fakePNG, nil
		},
	}

	if err := generateDeckThumbnails(ctx, svc, slug, opts); err != nil {
		t.Fatalf("generateDeckThumbnails: %v", err)
	}
	if len(seenHTML) != 3 {
		t.Fatalf("expected 3 renders, got %d", len(seenHTML))
	}

	deck, slidesContent, err := svc.Deck(ctx, slug)
	if err != nil {
		t.Fatalf("reload deck: %v", err)
	}
	for i, sc := range slidesContent {
		wantRef := deckSlideThumbRef(sc.Position)
		if sc.ThumbnailRef != wantRef {
			t.Fatalf("slide %d: ThumbnailRef=%q want %q", i, sc.ThumbnailRef, wantRef)
		}
		dataURI, ok := deck.Assets[wantRef]
		if !ok {
			t.Fatalf("slide %d: missing asset %q", i, wantRef)
		}
		if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
			t.Fatalf("slide %d: asset is not a PNG data URI: %q", i, dataURI[:min(40, len(dataURI))])
		}
	}
}

func TestGenerateDeckThumbnailsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, slug := seedDeckWithSlides(t, 2)

	calls := 0
	render := func(string, pdfgen.BrowserProvider, pdfgen.ScreenshotOptions) ([]byte, error) {
		calls++
		return fakePNG, nil
	}
	opts := deckThumbnailOptions{RuntimeJS: []byte("rt"), Browser: stubProvider{}, Render: render}

	if err := generateDeckThumbnails(ctx, svc, slug, opts); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if calls != 2 {
		t.Fatalf("first pass expected 2 renders, got %d", calls)
	}
	// Second pass: every slide already has a ThumbnailRef + asset, so nothing
	// should be re-rendered.
	if err := generateDeckThumbnails(ctx, svc, slug, opts); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if calls != 2 {
		t.Fatalf("second pass re-rendered: total renders=%d want 2", calls)
	}
}

func TestGenerateDeckThumbnailsRendererErrorLeavesRefEmpty(t *testing.T) {
	ctx := context.Background()
	svc, slug := seedDeckWithSlides(t, 2)

	opts := deckThumbnailOptions{
		RuntimeJS: []byte("rt"),
		Browser:   stubProvider{},
		Render: func(string, pdfgen.BrowserProvider, pdfgen.ScreenshotOptions) ([]byte, error) {
			return nil, errors.New("boom")
		},
	}
	if err := generateDeckThumbnails(ctx, svc, slug, opts); err == nil || !strings.Contains(err.Error(), "slide 0 render") {
		t.Fatalf("generateDeckThumbnails error = %v, want slide render failure", err)
	}
	deck, slidesContent, err := svc.Deck(ctx, slug)
	if err != nil {
		t.Fatalf("reload deck: %v", err)
	}
	if deck.ThumbnailReady {
		t.Fatal("deck must not be thumbnail-ready after a render failure")
	}
	for i, sc := range slidesContent {
		if sc.ThumbnailRef != "" {
			t.Fatalf("slide %d: expected empty ThumbnailRef on render error, got %q", i, sc.ThumbnailRef)
		}
		if _, ok := deck.Assets[deckSlideThumbRef(sc.Position)]; ok {
			t.Fatalf("slide %d: no asset should be stored on render error", i)
		}
	}
}

func TestGenerateDeckThumbnailsNilBrowserFails(t *testing.T) {
	ctx := context.Background()
	svc, slug := seedDeckWithSlides(t, 2)

	called := false
	err := generateDeckThumbnails(ctx, svc, slug, deckThumbnailOptions{
		RuntimeJS: []byte("rt"),
		Browser:   nil,
		Render: func(string, pdfgen.BrowserProvider, pdfgen.ScreenshotOptions) ([]byte, error) {
			called = true
			return fakePNG, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "browser is unavailable") {
		t.Fatalf("nil browser error = %v, want unavailable error", err)
	}
	if called {
		t.Fatal("renderer must not be called when browser is nil")
	}
	_, slidesContent, _ := svc.Deck(ctx, slug)
	for i, sc := range slidesContent {
		if sc.ThumbnailRef != "" {
			t.Fatalf("slide %d: ThumbnailRef must stay empty when disabled", i)
		}
	}
}
