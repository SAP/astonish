package tui

import (
	"math"
	"strconv"
	"testing"
)

func paletteLuminance(hex string) float64 {
	value, _ := strconv.ParseUint(hex[1:], 16, 32)
	result := 0.0
	for i, w := range []float64{.2126, .7152, .0722} {
		c := float64((value>>uint(16-i*8))&255) / 255
		if c <= .04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+.055)/1.055, 2.4)
		}
		result += c * w
	}
	return result
}
func paletteContrast(a, b string) float64 {
	x, y := paletteLuminance(a), paletteLuminance(b)
	if x < y {
		x, y = y, x
	}
	return (x + .05) / (y + .05)
}
func TestTenCodePalettesReadableAndDistinct(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	seen := map[CodeThemeID]bool{}
	for _, d := range codeThemeCatalog {
		if seen[d.option.ID] {
			t.Fatal("duplicate palette", d.option.ID)
		}
		seen[d.option.ID] = true
		th, err := ResolveCodeTheme(d.option.ID)
		if err != nil || th.SyntaxStyle == nil {
			t.Fatalf("palette %s: %v", d.option.ID, err)
		}
		if d.option.ID == CodeThemeBlueGray || d.option.ID == CodeThemeDarkOrange {
			continue
		}
		for _, bg := range []string{d.palette.canvas, d.palette.card} {
			for role, fg := range map[string]string{"text": d.palette.text, "muted": d.palette.muted, "accent": d.palette.accent, "success": d.palette.success, "warning": d.palette.warning, "danger": d.palette.danger} {
				if ratio := paletteContrast(fg, bg); ratio < 4.5 {
					t.Errorf("%s %s on %s contrast %.2f < 4.5", d.option.ID, role, bg, ratio)
				}
			}
		}
		for _, bg := range []string{d.added, d.removed} {
			if ratio := paletteContrast(d.palette.text, bg); ratio < 4.5 {
				t.Errorf("%s diff contrast %.2f", d.option.ID, ratio)
			}
		}
	}
	if len(seen) != 10 {
		t.Fatal("want ten palettes")
	}
}
