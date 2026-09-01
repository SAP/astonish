package slides

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"log/slog"
	"strings"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
	"golang.org/x/image/draw"
)

const (
	// heroThumbPrefix namespaces pre-baked hero photo thumbnail asset keys so
	// they are distinguishable from the full-size raster assets. We use a colon
	// separator (not slash) because the media endpoint route is
	// /templates/{name}/media/{ref} — a slash in the ref would break gorilla/mux
	// path matching. The colon convention matches font refs (font:Manrope:400).
	heroThumbPrefix = "herothumb:"

	// heroThumbMaxWidth and heroThumbMaxHeight bound the downscaled PNG.
	// They match the archetype thumbnail scale (1/6 of 1920×1080 → 320×180)
	// so both archetype and photo thumbnails render at the same card size.
	heroThumbMaxWidth  = 320
	heroThumbMaxHeight = 180
)

// HeroThumbKey returns the asset key for a pre-baked hero photo thumbnail
// derived from the original full-size asset ref.
func HeroThumbKey(ref string) string { return heroThumbPrefix + ref }

// GenerateHeroPhotoThumbnails downscales each hero-eligible raster in tmpl to a
// small PNG and stores it as an additional asset. Unlike archetype thumbnails
// this is pure Go image processing — no headless browser needed.
//
// It is BEST-EFFORT: any per-photo decode/resize failure is logged and skipped.
// The template imports normally regardless of thumbnail success.
func GenerateHeroPhotoThumbnails(tmpl *themes.Template) {
	if tmpl == nil {
		return
	}
	photos := collectTemplateHeroPhotos(*tmpl)
	if len(photos) == 0 {
		return
	}
	if tmpl.Assets == nil {
		tmpl.Assets = map[string]string{}
	}
	for _, p := range photos {
		key := HeroThumbKey(p.Ref)
		if _, exists := tmpl.Assets[key]; exists {
			continue // already baked (idempotent)
		}
		dataURI, ok := tmpl.Assets[p.Ref]
		if !ok {
			continue
		}
		thumbURI, err := resizeDataURIToPNGThumb(dataURI, heroThumbMaxWidth, heroThumbMaxHeight)
		if err != nil {
			slog.Warn("hero photo thumbnail generation failed",
				"template", tmpl.Name, "ref", p.Ref, "error", err)
			continue
		}
		tmpl.Assets[key] = thumbURI
	}
}

// resizeDataURIToPNGThumb decodes a data:image/…;base64,… URI, downscales the
// image to fit within maxW×maxH (preserving aspect ratio), and returns a new
// data:image/png;base64,… URI.
func resizeDataURIToPNGThumb(dataURI string, maxW, maxH int) (string, error) {
	raw, err := decodeDataURIBytes(dataURI)
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return "", fmt.Errorf("zero-size image %dx%d", srcW, srcH)
	}

	// Compute target dimensions preserving aspect ratio.
	dstW, dstH := fitDimensions(srcW, srcH, maxW, maxH)

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return "", fmt.Errorf("encode png: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// decodeDataURIBytes extracts the raw bytes from a data:…;base64,… string.
func decodeDataURIBytes(dataURI string) ([]byte, error) {
	const b64Marker = ";base64,"
	idx := strings.Index(dataURI, b64Marker)
	if idx < 0 {
		return nil, fmt.Errorf("not a base64 data URI")
	}
	encoded := dataURI[idx+len(b64Marker):]
	return base64.StdEncoding.DecodeString(encoded)
}

// fitDimensions scales srcW×srcH to fit within maxW×maxH, preserving aspect
// ratio and never upscaling.
func fitDimensions(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= maxW && srcH <= maxH {
		return srcW, srcH
	}
	ratioW := float64(maxW) / float64(srcW)
	ratioH := float64(maxH) / float64(srcH)
	ratio := ratioW
	if ratioH < ratioW {
		ratio = ratioH
	}
	w := int(float64(srcW) * ratio)
	h := int(float64(srcH) * ratio)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}
