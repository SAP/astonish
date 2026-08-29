package slides

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"github.com/SAP/astonish/pkg/pdfgen"
	"github.com/go-rod/rod"
)

// fakePNG is canned PNG-magic-prefixed bytes returned by the injected renderer.
var fakePNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}

func testArchetypeTemplate() *themes.Template {
	return &themes.Template{
		Name:   "brandco",
		Label:  "BrandCo",
		Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#111111"},
		Assets: map[string]string{},
		Archetypes: []themes.Archetype{
			{
				Kind:   "title",
				Title:  "Cover",
				Markup: `<ast-slide id="title"><ast-text id="t" x="100" y="100" w="800" h="120">{{TITLE}}</ast-text><ast-text id="b" x="100" y="240" w="800" h="80">{{BODY}}</ast-text></ast-slide>`,
			},
			{
				Kind:   "content",
				Title:  "Content",
				Markup: `<ast-slide id="content"><ast-text id="h" x="100" y="80" w="800" h="100">{{TITLE}}</ast-text></ast-slide>`,
			},
		},
	}
}

func TestGenerateArchetypeThumbnailsBakesAssetsAndRefs(t *testing.T) {
	tmpl := testArchetypeTemplate()
	var mu sync.Mutex
	var seenHTML []string
	opts := thumbnailOptions{
		RuntimeJS: []byte("/* runtime */"),
		Browser:   stubProvider{},
		Render: func(html string, _ pdfgen.BrowserProvider, sopts pdfgen.ScreenshotOptions) ([]byte, error) {
			mu.Lock()
			seenHTML = append(seenHTML, html)
			mu.Unlock()
			if sopts.Width != CanvasWidth || sopts.Height != CanvasHeight {
				t.Errorf("unexpected viewport: %dx%d", sopts.Width, sopts.Height)
			}
			if sopts.ReadinessExpression != SlidesReadinessExpression {
				t.Errorf("readiness expression not propagated: %q", sopts.ReadinessExpression)
			}
			return fakePNG, nil
		},
	}

	generateArchetypeThumbnails(context.Background(), tmpl, opts)

	for _, arch := range tmpl.Archetypes {
		wantRef := "thumb/" + arch.Kind
		if arch.ThumbnailRef != wantRef {
			t.Fatalf("archetype %q: ThumbnailRef=%q want %q", arch.Kind, arch.ThumbnailRef, wantRef)
		}
		dataURI, ok := tmpl.Assets[wantRef]
		if !ok {
			t.Fatalf("archetype %q: missing asset %q", arch.Kind, wantRef)
		}
		if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
			t.Fatalf("archetype %q: asset is not a PNG data URI: %q", arch.Kind, dataURI[:min(40, len(dataURI))])
		}
	}

	// Placeholders must be substituted before rendering (no raw mustache tokens).
	for _, html := range seenHTML {
		if strings.Contains(html, "{{TITLE}}") || strings.Contains(html, "{{BODY}}") {
			t.Fatalf("rendered HTML still contains placeholders: %s", html)
		}
		if !strings.Contains(html, thumbnailSampleTitle) {
			t.Fatalf("rendered HTML missing sample title text")
		}
	}
}

func TestGenerateArchetypeThumbnailsRendererErrorLeavesRefEmpty(t *testing.T) {
	tmpl := testArchetypeTemplate()
	opts := thumbnailOptions{
		RuntimeJS: []byte("/* runtime */"),
		Browser:   stubProvider{},
		Render: func(string, pdfgen.BrowserProvider, pdfgen.ScreenshotOptions) ([]byte, error) {
			return nil, errors.New("boom")
		},
	}

	generateArchetypeThumbnails(context.Background(), tmpl, opts)

	for _, arch := range tmpl.Archetypes {
		if arch.ThumbnailRef != "" {
			t.Fatalf("archetype %q: expected empty ThumbnailRef on renderer error, got %q", arch.Kind, arch.ThumbnailRef)
		}
		if _, ok := tmpl.Assets["thumb/"+arch.Kind]; ok {
			t.Fatalf("archetype %q: no asset should be stored on renderer error", arch.Kind)
		}
	}
}

func TestGenerateArchetypeThumbnailsNilBrowserIsNoop(t *testing.T) {
	tmpl := testArchetypeTemplate()
	called := false
	generateArchetypeThumbnails(context.Background(), tmpl, thumbnailOptions{
		RuntimeJS: []byte("/* runtime */"),
		Browser:   nil,
		Render: func(string, pdfgen.BrowserProvider, pdfgen.ScreenshotOptions) ([]byte, error) {
			called = true
			return fakePNG, nil
		},
	})
	if called {
		t.Fatal("renderer must not be called when browser is nil")
	}
	for _, arch := range tmpl.Archetypes {
		if arch.ThumbnailRef != "" {
			t.Fatalf("archetype %q: ThumbnailRef must stay empty when disabled", arch.Kind)
		}
	}
}

// stubProvider is a non-nil BrowserProvider whose GetOrLaunch is never reached in
// these tests because the injected renderer short-circuits it.
type stubProvider struct{}

func (stubProvider) GetOrLaunch() (*rod.Browser, error) { return nil, nil }
