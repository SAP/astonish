package api

import (
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestDockerBaseState(t *testing.T) {
	exists := func(id string) bool { return id == "abc123" }

	tests := []struct {
		name         string
		overlayReady bool
		layerID      string
		hasConfig    bool
		wantLive     bool
		wantLegacy   bool
	}{
		{
			name:         "incus leftover config, no overlay",
			overlayReady: false,
			hasConfig:    true,
			wantLive:     false,
			wantLegacy:   true,
		},
		{
			name:         "incus leftover layer id, no overlay",
			overlayReady: false,
			layerID:      "deadbeef",
			wantLive:     false,
			wantLegacy:   true,
		},
		{
			name:         "fresh docker, overlay missing, no db config",
			overlayReady: false,
			wantLive:     false,
			wantLegacy:   false,
		},
		{
			name:         "seeded overlay, no customized layer",
			overlayReady: true,
			layerID:      sandbox.BaseTemplateID,
			hasConfig:    false,
			wantLive:     false,
			wantLegacy:   false,
		},
		{
			name:         "live docker layer",
			overlayReady: true,
			layerID:      "abc123",
			hasConfig:    true,
			wantLive:     true,
			wantLegacy:   false,
		},
		{
			name:         "db points at missing docker layer",
			overlayReady: true,
			layerID:      "ghost",
			hasConfig:    true,
			wantLive:     false,
			wantLegacy:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live, legacy := dockerBaseState(tt.overlayReady, tt.layerID, tt.hasConfig, exists)
			if live != tt.wantLive || legacy != tt.wantLegacy {
				t.Fatalf("dockerBaseState() live=%v legacy=%v, want live=%v legacy=%v",
					live, legacy, tt.wantLive, tt.wantLegacy)
			}
		})
	}
}
