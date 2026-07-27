package tui

import (
	"context"
	"strings"
	"testing"
)

func TestRenderSlashCompletionPaintsEveryRowBlack(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.theme.NoColor = false
	m.slash = slashCompletion{active: true, matches: builtInSlashCommands, cursor: 0}

	out := m.renderSlashCompletion()
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected popup with multiple lines, got %d: %q", len(lines), stripANSI(out))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, ansiTrueBlackBG) {
			t.Fatalf("line %d is not explicitly painted black: %q", i, line)
		}
	}
}
