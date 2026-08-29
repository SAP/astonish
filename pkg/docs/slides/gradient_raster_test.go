package slides

import (
	"strings"
	"testing"
)

func TestRasterizeGradientPNGRejectsOversizedImage(t *testing.T) {
	gradient := &Gradient{Stops: []GradientStop{
		{Pos: 0, Color: "#000000"},
		{Pos: 100, Color: "#FFFFFF"},
	}}
	_, err := rasterizeGradientPNG(gradient, 100_000, 100_000)
	if err == nil || !strings.Contains(err.Error(), "exceed raster limit") {
		t.Fatalf("rasterizeGradientPNG() error = %v, want raster limit error", err)
	}
}
