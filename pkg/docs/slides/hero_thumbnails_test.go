package slides

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

// makePNGDataURI creates a solid-color PNG of the given size and returns a
// data:image/png;base64,… string suitable for a template asset.
func makePNGDataURI(w, h int, c color.Color) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestGenerateHeroPhotoThumbnailsBakesSmallPNGs(t *testing.T) {
	// Create a template with a large hero-eligible raster.
	ref := "sha256-abc123"
	origURI := makePNGDataURI(1920, 1080, color.RGBA{R: 255, A: 255})

	tmpl := &themes.Template{
		Name: "test-hero",
		Assets: map[string]string{
			ref: origURI,
		},
		// Model with a slide whose object references the asset and is large
		// enough to pass the heroPhotoMinArea filter.
		Model: &themes.TemplateModel{
			Slides: []themes.IRLayout{
				{
					Name: "Hero Slide",
					Objects: []themes.IRChrome{
						{MediaKey: ref, W: 800, H: 600},
					},
				},
			},
		},
	}

	GenerateHeroPhotoThumbnails(tmpl)

	thumbKey := HeroThumbKey(ref)
	thumbURI, ok := tmpl.Assets[thumbKey]
	if !ok {
		t.Fatalf("expected herothumb asset at key %q, got none", thumbKey)
	}

	// Verify it's a valid PNG data URI.
	if !strings.HasPrefix(thumbURI, "data:image/png;base64,") {
		t.Fatalf("thumb URI is not a PNG data URI: %s", thumbURI[:min(len(thumbURI), 40)])
	}

	// Verify the thumbnail is smaller than the original.
	if len(thumbURI) >= len(origURI) {
		t.Fatalf("thumb URI (%d chars) should be smaller than original (%d chars)",
			len(thumbURI), len(origURI))
	}

	// Decode and check dimensions fit within the max bounds.
	raw, err := decodeDataURIBytes(thumbURI)
	if err != nil {
		t.Fatalf("decode thumb data URI: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode thumb PNG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > heroThumbMaxWidth || bounds.Dy() > heroThumbMaxHeight {
		t.Fatalf("thumb dimensions %dx%d exceed max %dx%d",
			bounds.Dx(), bounds.Dy(), heroThumbMaxWidth, heroThumbMaxHeight)
	}
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Fatal("thumb has zero dimensions")
	}
}

func TestGenerateHeroPhotoThumbnailsIdempotent(t *testing.T) {
	ref := "sha256-abc123"
	origURI := makePNGDataURI(800, 600, color.RGBA{G: 200, A: 255})

	tmpl := &themes.Template{
		Name: "test-idempotent",
		Assets: map[string]string{
			ref: origURI,
		},
		Model: &themes.TemplateModel{
			Slides: []themes.IRLayout{
				{Objects: []themes.IRChrome{{MediaKey: ref, W: 800, H: 600}}},
			},
		},
	}

	GenerateHeroPhotoThumbnails(tmpl)
	thumbKey := HeroThumbKey(ref)
	first := tmpl.Assets[thumbKey]
	if first == "" {
		t.Fatal("first call did not generate thumbnail")
	}

	// Second call should not overwrite.
	GenerateHeroPhotoThumbnails(tmpl)
	if tmpl.Assets[thumbKey] != first {
		t.Fatal("second call changed the thumbnail")
	}
}

func TestGenerateHeroPhotoThumbnailsNilTemplate(t *testing.T) {
	// Must not panic on nil.
	GenerateHeroPhotoThumbnails(nil)
}

func TestGenerateHeroPhotoThumbnailsNoPhotos(t *testing.T) {
	tmpl := &themes.Template{
		Name:   "no-photos",
		Assets: map[string]string{},
	}
	GenerateHeroPhotoThumbnails(tmpl)
	for k := range tmpl.Assets {
		if strings.HasPrefix(k, heroThumbPrefix) {
			t.Fatalf("unexpected herothumb asset: %s", k)
		}
	}
}

func TestGenerateHeroPhotoThumbnailsSkipsSVG(t *testing.T) {
	ref := "sha256-svg"
	tmpl := &themes.Template{
		Name: "svg-template",
		Assets: map[string]string{
			ref: "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", // <svg></svg>
		},
		Model: &themes.TemplateModel{
			Slides: []themes.IRLayout{
				{Objects: []themes.IRChrome{{MediaKey: ref, W: 800, H: 600}}},
			},
		},
	}
	GenerateHeroPhotoThumbnails(tmpl)
	if _, ok := tmpl.Assets[HeroThumbKey(ref)]; ok {
		t.Fatal("SVG assets should not get hero thumbnails")
	}
}

func TestFitDimensionsPreservesAspectRatio(t *testing.T) {
	tests := []struct {
		srcW, srcH, maxW, maxH int
		wantW, wantH           int
	}{
		{1920, 1080, 320, 180, 320, 180},  // exact fit
		{1600, 900, 320, 180, 320, 180},   // 16:9
		{800, 800, 320, 180, 180, 180},    // square → height-limited
		{100, 50, 320, 180, 100, 50},      // already small → no upscale
		{3840, 2160, 320, 180, 320, 180},  // 4K → scaled down
		{640, 480, 320, 180, 240, 180},    // 4:3 → height-limited
	}
	for _, tt := range tests {
		w, h := fitDimensions(tt.srcW, tt.srcH, tt.maxW, tt.maxH)
		if w != tt.wantW || h != tt.wantH {
			t.Errorf("fitDimensions(%d,%d,%d,%d) = %d,%d want %d,%d",
				tt.srcW, tt.srcH, tt.maxW, tt.maxH, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestHeroThumbKey(t *testing.T) {
	ref := "sha256-deadbeef1234"
	got := HeroThumbKey(ref)
	want := "herothumb/sha256-deadbeef1234"
	if got != want {
		t.Fatalf("HeroThumbKey(%q) = %q, want %q", ref, got, want)
	}
}

func TestDecodeDataURIBytes(t *testing.T) {
	payload := []byte("hello world")
	uri := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(payload)
	got, err := decodeDataURIBytes(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}

	_, err = decodeDataURIBytes("not-a-data-uri")
	if err == nil {
		t.Fatal("expected error for non-data URI")
	}
}
