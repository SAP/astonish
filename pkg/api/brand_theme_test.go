package api

import "testing"

func TestNormalizeBrandTheme(t *testing.T) {
	if got := NormalizeBrandTheme("Aster"); got != "aster" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeBrandTheme("nova"); got != "nova" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeBrandTheme("sage"); got != "sage" {
		t.Fatalf("sage should be shipped, got %q", got)
	}
	if got := NormalizeBrandTheme("ember"); got != "ember" {
		t.Fatalf("ember should be shipped, got %q", got)
	}
	if got := NormalizeBrandTheme("classic"); got != "classic" {
		t.Fatalf("classic should be shipped, got %q", got)
	}
	if got := NormalizeBrandTheme("amethyst"); got != "" {
		t.Fatalf("unshipped amethyst should be empty, got %q", got)
	}
	if got := NormalizeBrandTheme(""); got != "" {
		t.Fatalf("empty should stay empty, got %q", got)
	}
}

func TestResolveBrandTheme(t *testing.T) {
	if got := ResolveBrandTheme("", ""); got != defaultBrandTheme {
		t.Fatalf("empty cascade: want %q got %q", defaultBrandTheme, got)
	}
	if got := ResolveBrandTheme("", "nova"); got != "nova" {
		t.Fatalf("platform only: got %q", got)
	}
	if got := ResolveBrandTheme("aster", "nova"); got != "aster" {
		t.Fatalf("user wins: got %q", got)
	}
	if got := ResolveBrandTheme("amethyst", "nova"); got != "nova" {
		t.Fatalf("unshipped user falls to platform: got %q", got)
	}
	if got := ResolveBrandTheme("amethyst", "amethyst"); got != defaultBrandTheme {
		t.Fatalf("unshipped both → default: got %q", got)
	}
	if got := ResolveBrandTheme("sage", "nova"); got != "sage" {
		t.Fatalf("calm pack user wins: got %q", got)
	}
}
