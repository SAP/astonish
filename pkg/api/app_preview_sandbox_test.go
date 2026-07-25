package api

import (
	"strings"
	"testing"
)

func TestSandboxHTML_AppCanvasNovaNight(t *testing.T) {
	html := buildSandboxFullHTML()

	// Default canvas = Nova night — not the old slate #0b1222 / light #fafbfe.
	if !strings.Contains(html, "#160b1f") {
		t.Error("sandbox HTML should use Nova night canvas #160b1f as default")
	}
	if strings.Contains(html, "#0b1222") {
		t.Error("sandbox HTML should not use legacy slate canvas #0b1222")
	}
	if strings.Contains(html, "#fafbfe") {
		t.Error("sandbox HTML should not flip to light canvas #fafbfe")
	}

	// App Canvas token utilities for generated apps.
	for _, token := range []string{
		"--color-app-canvas:",
		"--color-surface:",
		"--color-surface-2:",
		"--color-app:",
		"--color-app-muted:",
		"--color-app-border:",
		"--color-brand:",
	} {
		if !strings.Contains(html, token) {
			t.Errorf("sandbox @theme missing %s", token)
		}
	}

	// Runtime must apply pack tokens from parent postMessage (multi-theme ready).
	if !strings.Contains(html, "function applyAppCanvas") {
		t.Error("sandbox should define applyAppCanvas for brand pack tokens")
	}
	if !strings.Contains(html, "msg.tokens") {
		t.Error("theme handler should apply msg.tokens from parent")
	}
}

