package scheduler

import (
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/genai"
)

func TestResolveAdaptiveReportBody(t *testing.T) {
	marker := "Fallback\n```astonish-report\npath: reports/check.md\n```"
	if got := ResolveAdaptiveReportBody(marker, map[string]string{"reports/check.md": "Observed report"}); got != "Observed report" {
		t.Fatal(got)
	}
	if got := ResolveAdaptiveReportBody(marker, nil); got != "Fallback" {
		t.Fatal(got)
	}
}

func TestExtractUserFacingText_SkipsThoughtAndTools(t *testing.T) {
	parts := []*genai.Part{
		{Thought: true, Text: "secret chain of thought"},
		{Text: "Hello "},
		{FunctionCall: &genai.FunctionCall{Name: "write_file"}},
		{Text: "world"},
	}
	got := extractUserFacingText(parts)
	if got != "Hello world" {
		t.Fatalf("got %q, want %q", got, "Hello world")
	}
}

func TestApplyLastWinsTurn(t *testing.T) {
	prior := "I need to paginate…"
	next := "```astonish-report\npath: /sandbox/r.md\n```"
	if got := applyLastWinsTurn(prior, next); got != next {
		t.Fatalf("last-wins = %q, want final turn", got)
	}
	if got := applyLastWinsTurn(prior, "   "); got != prior {
		t.Fatalf("empty turn should keep prior, got %q", got)
	}
}

func TestCaptureWriteFileContent(t *testing.T) {
	written := map[string]string{}
	captureWriteFileContent(written, map[string]any{
		"file_path": "/sandbox/openstack-vm-status-report.md",
		"content":   "# VMs\n\nok",
	})
	if written["/sandbox/openstack-vm-status-report.md"] != "# VMs\n\nok" {
		t.Fatalf("missing full path content: %+v", written)
	}
	if written["openstack-vm-status-report.md"] != "# VMs\n\nok" {
		t.Fatalf("missing basename content: %+v", written)
	}
}

func TestPreferDeliveryBody_ReportFromWriteFile(t *testing.T) {
	lastWins := "Done.\n\n```astonish-report\npath: /sandbox/openstack-vm-status-report.md\ntitle: OpenStack VM Status\n```\n"
	written := map[string]string{
		"/sandbox/openstack-vm-status-report.md": "# OpenStack VM Status\n\n| Name | State |\n|---|---|\n| a | ACTIVE |\n",
	}
	got := preferDeliveryBody(lastWins, written, nil, nil)
	if !strings.Contains(got, "| a | ACTIVE |") {
		t.Fatalf("expected report body, got %q", got)
	}
	if strings.Contains(got, "astonish-report") {
		t.Fatalf("delivery body should not include fence: %q", got)
	}
}

func TestPreferDeliveryBody_StripsFenceWhenNoFile(t *testing.T) {
	lastWins := "Summary of results.\n\n```astonish-report\npath: /sandbox/missing.md\n```\n"
	got := preferDeliveryBody(lastWins, nil, nil, nil)
	if got != "Summary of results." {
		t.Fatalf("got %q, want stripped prose", got)
	}
}

func TestPreferDeliveryBody_DrainedMarkdownWhenFenceOnly(t *testing.T) {
	lastWins := "```astonish-report\npath: /sandbox/r.md\ntitle: T\n```"
	written := map[string]string{"/sandbox/r.md": "# Report\n\nbody"}
	drained := []agent.FileArtifact{{Path: "/sandbox/r.md", ToolName: "write_file"}}
	got := preferDeliveryBody(lastWins, written, drained, nil)
	if got != "# Report\n\nbody" {
		t.Fatalf("got %q", got)
	}
}

func TestPreferDeliveryBody_ReadFileFallback(t *testing.T) {
	lastWins := "```astonish-report\npath: /sandbox/r.md\n```"
	got := preferDeliveryBody(lastWins, nil, nil, func(path string) ([]byte, error) {
		if path != "/sandbox/r.md" {
			t.Fatalf("path = %q", path)
		}
		return []byte("# from disk"), nil
	})
	if got != "# from disk" {
		t.Fatalf("got %q", got)
	}
}

// TestPreferDeliveryBody_ReadFileDeniedNoLeak proves that when the ReadSessionFile
// callback denies the host read (the platform-mode ART containment guard), a
// model-supplied report path pointing at a host file does NOT leak its contents
// into the delivery body. With no captured write_file body, delivery falls back
// to the fence-stripped prose instead of the denied file.
func TestPreferDeliveryBody_ReadFileDeniedNoLeak(t *testing.T) {
	lastWins := "Scheduled report.\n\n```astonish-report\npath: /proc/self/environ\n```\n"
	denied := func(path string) ([]byte, error) {
		return nil, errors.New("adaptive report host-file read is disabled in platform mode")
	}
	got := preferDeliveryBody(lastWins, nil, nil, denied)
	if got != "Scheduled report." {
		t.Fatalf("denied host read must fall back to stripped prose, got %q", got)
	}
	if strings.Contains(got, "environ") || strings.Contains(got, "/proc/") {
		t.Fatalf("delivery body leaked host path content: %q", got)
	}
}

func TestTruncateResult_KeepsSuffix(t *testing.T) {
	// Temporarily exercise with a small string by building over maxDeliveryLen.
	prefix := strings.Repeat("EARLY-", 700) // 4200 chars
	suffix := "FINAL-REPORT-CONTENT"
	s := prefix + suffix
	got := truncateResult(s)
	if !strings.HasPrefix(got, "... (truncated)") {
		t.Fatalf("expected truncated prefix marker, got start %q", got[:40])
	}
	if !strings.HasSuffix(got, suffix) {
		t.Fatalf("expected suffix preserved, got end %q", got[len(got)-40:])
	}
	if strings.Contains(got, "EARLY-EARLY-EARLY-EARLY") && strings.HasPrefix(got, "EARLY-") {
		t.Fatal("should not keep the original prefix as the message start")
	}
}
