package api

import "testing"

func TestNormalizeBrandTheme(t *testing.T) {
	if got := NormalizeBrandTheme("Aster"); got != "aster" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeBrandTheme("nova"); got != "nova" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeBrandTheme("ember"); got != "" {
		t.Fatalf("unshipped should be empty, got %q", got)
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
	if got := ResolveBrandTheme("ember", "nova"); got != "nova" {
		t.Fatalf("unshipped user falls to platform: got %q", got)
	}
	if got := ResolveBrandTheme("ember", "ember"); got != defaultBrandTheme {
		t.Fatalf("unshipped both → default: got %q", got)
	}
}
