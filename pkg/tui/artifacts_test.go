package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

type artifactContentBackend struct {
	staticBackend
	content       map[string]string
	seenSessionID string
	seenPath      string
}

func (b *artifactContentBackend) ReadArtifactContent(_ context.Context, sessionID, path string) (backend.ArtifactContent, error) {
	b.seenSessionID = sessionID
	b.seenPath = path
	return backend.ArtifactContent{Path: path, Content: b.content[path]}, nil
}

func TestArtifactListRendersGeneratedFiles(t *testing.T) {
	tr := events.NewTranscript()
	tr.Apply(events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{
		Path: "/tmp/report.md", FileName: "report.md", FileType: "Markdown", ToolName: "write_file", IsReport: true,
	}})
	tr.Apply(events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{
		Path: "/tmp/data.json", FileName: "data.json", FileType: "JSON", ToolName: "write_file",
	}})

	m := model{theme: plainTheme(), tr: tr, width: 100, height: 30, ready: true}
	rendered, _, artifactHits := m.renderTranscript()

	for _, want := range []string{"Files generated (2)", "report.md", "data.json", "click to open"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered artifact list missing %q:\n%s", want, rendered)
		}
	}
	if len(artifactHits) != 2 {
		t.Fatalf("artifact hit count=%d want 2", len(artifactHits))
	}
}

func TestArtifactClickOpensViewerAndEscCloses(t *testing.T) {
	b := &artifactContentBackend{
		staticBackend: staticBackend{info: backend.Info{SessionID: "sess-1"}},
		content:       map[string]string{"/tmp/report.md": "# Report\n\nhello"},
	}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{
		Path: "/tmp/report.md", FileName: "report.md", FileType: "Markdown", IsReport: true,
	}})
	m.refreshViewport()

	if len(m.artifactHits) != 1 {
		t.Fatalf("artifact hit count=%d want 1", len(m.artifactHits))
	}
	y := m.viewportTopY() + m.artifactHits[0].start - m.vp.YOffset()
	next, _ := m.handleMouse(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	next, cmd := m.handleMouse(tea.MouseReleaseMsg{X: 4, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	if !m.fileViewer.open || !m.fileViewer.loading {
		t.Fatalf("viewer not opened/loading: %+v", m.fileViewer)
	}
	if cmd == nil {
		t.Fatal("expected artifact load command")
	}
	msg := cmd()
	loaded, ok := msg.(artifactContentLoadedMsg)
	if !ok {
		t.Fatalf("load cmd returned %T", msg)
	}
	next, _ = m.Update(loaded)
	m = next.(model)
	if m.fileViewer.loading || !strings.Contains(m.fileViewer.content, "# Report") {
		t.Fatalf("viewer content not loaded: %+v", m.fileViewer)
	}
	if b.seenSessionID != "sess-1" || b.seenPath != "/tmp/report.md" {
		t.Fatalf("backend called with session=%q path=%q", b.seenSessionID, b.seenPath)
	}
	if out := m.renderFileViewerContent(80); !strings.Contains(out, "Report") || !strings.Contains(out, "hello") {
		t.Fatalf("markdown viewer did not render content:\n%s", out)
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.fileViewer.open {
		t.Fatal("expected esc to close file viewer")
	}
}

func TestReportMarkerUpdatesArtifactList(t *testing.T) {
	tr := events.NewTranscript()
	tr.Apply(events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{Path: "/tmp/report.md", FileName: "report.md", FileType: "Markdown"}})
	tr.Apply(events.Event{Kind: events.KindReportMarker, Text: "/tmp/report.md", Artifact: &events.Artifact{Path: "/tmp/report.md", IsReport: true, ReportTitle: "Quarterly Report"}})

	artifact := tr.Items[0].Artifacts[0]
	if !artifact.IsReport || artifact.ReportTitle != "Quarterly Report" {
		t.Fatalf("artifact not marked as report: %+v", artifact)
	}
	m := model{theme: plainTheme(), tr: tr, width: 100, height: 30, ready: true}
	rendered, _, _ := m.renderTranscript()
	if !strings.Contains(rendered, "Quarterly Report") || !strings.Contains(rendered, "report") {
		t.Fatalf("rendered report marker missing title/report label:\n%s", rendered)
	}
}

func TestArtifactViewerRendersNonMarkdownAsCodeContent(t *testing.T) {
	m := model{theme: plainTheme(), width: 100, height: 30}
	m.fileViewer = fileViewerState{
		open:     true,
		artifact: events.Artifact{Path: "/tmp/data.json", FileName: "data.json", FileType: "JSON"},
		content:  `{"ok": true}`,
	}
	out := m.renderFileViewerContent(80)
	if !strings.Contains(out, "json") || !strings.Contains(out, `"ok"`) {
		t.Fatalf("expected raw/code content for JSON artifact:\n%s", out)
	}
}
