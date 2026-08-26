package migrations

import (
	"github.com/SAP/astonish/pkg/docs/slides"
	"testing"
)

func TestNormalize(t *testing.T) {
	g, err := Normalize(slides.SceneGraph{})
	if err != nil || g.SchemaVersion != slides.SchemaV1 {
		t.Fatalf("got %#v %v", g, err)
	}
	if _, err := Normalize(slides.SceneGraph{SchemaVersion: 99}); err == nil {
		t.Fatal("expected unsupported version")
	}
}
