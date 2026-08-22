package migrations

import (
	"fmt"

	"github.com/SAP/astonish/pkg/docs/slides"
)

// Normalize upgrades an in-memory graph without mutating persisted source.
func Normalize(in slides.SceneGraph) (slides.SceneGraph, error) {
	if in.SchemaVersion == 0 {
		in.SchemaVersion = slides.SchemaV1
	}
	if in.SchemaVersion != slides.SchemaV1 {
		return slides.SceneGraph{}, fmt.Errorf("unsupported slides schema version %d", in.SchemaVersion)
	}
	return in, nil
}
