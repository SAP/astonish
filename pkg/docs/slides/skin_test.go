package slides

import (
	"testing"

	"github.com/SAP/astonish/pkg/docs/slides/themes"
)

func TestCorporateSkinUsesTemplateAccent(t *testing.T) {
	s := CorporateSkin(map[string]string{"surface": "#FFFFFF", "ink": "#111111", "accent": "#C8102E"})
	if s.ID != SkinCorporate || s.Accent != "#C8102E" {
		t.Fatalf("%#v", s)
	}
	if s.Panel == "" || s.Panel == s.Surface {
		t.Fatal("corporate panel should be a mixed card fill")
	}
}

func TestProductSkinDefaults(t *testing.T) {
	s := ProductSkin(nil)
	if s.ID != SkinProduct || s.Accent != "#8B5CF6" || s.MonoFont == "" {
		t.Fatalf("%#v", s)
	}
	if !s.IsProduct() {
		t.Fatal("IsProduct")
	}
}

func TestProductSkinRecolorsDerivedFromAccent(t *testing.T) {
	s := ProductSkin(map[string]string{"accent": "#F97316", "surface": "#0B0D0F", "ink": "#ECEDEE"})
	if s.Accent != "#F97316" {
		t.Fatalf("accent = %q", s.Accent)
	}
	if s.AccentFill == "#1B1430" {
		t.Fatal("AccentFill should follow the new accent, not the default violet fill")
	}
	if s.AccentGlow != "#F97316" {
		t.Fatalf("AccentGlow = %q", s.AccentGlow)
	}
}

func TestProductSkinLightSurface(t *testing.T) {
	s := ProductSkin(map[string]string{"surface": "#FAFAF8", "ink": "#171717", "accent": "#171717"})
	if s.Panel == "#15181C" {
		t.Fatal("panel should be derived from the light surface, not the default dark furniture")
	}
}

func TestSkinForNameFallback(t *testing.T) {
	if SkinFor(themes.Template{Name: "product"}).ID != SkinProduct {
		t.Fatal("name product")
	}
	if SkinFor(themes.Template{Name: "acme", Skin: "product"}).ID != SkinProduct {
		t.Fatal("explicit skin")
	}
	if SkinFor(themes.Template{Name: "acme"}).ID != SkinCorporate {
		t.Fatal("default corporate")
	}
}
