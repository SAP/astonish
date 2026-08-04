package api

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
)

func TestPullArtifactFromSandboxBackendResumesStoppedSession(t *testing.T) {
	ctx := context.Background()
	backend := mock.New()
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: "sess-1", TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}
	if err := backend.PushFile(ctx, "sess-1", "/tmp/report.md", strings.NewReader("live 2026"), 0o644); err != nil {
		t.Fatalf("PushFile() err = %v", err)
	}
	if err := backend.StopSession(ctx, "sess-1"); err != nil {
		t.Fatalf("StopSession() err = %v", err)
	}

	content, ok := pullArtifactFromSandboxBackend(ctx, backend, "sess-1", "/tmp/report.md")
	if !ok {
		t.Fatal("expected sandbox content")
	}
	if string(content) != "live 2026" {
		t.Fatalf("content = %q, want %q", content, "live 2026")
	}
	if calls := backend.StartSessionCalls(); len(calls) != 1 || calls[0] != "sess-1" {
		t.Fatalf("StartSessionCalls() = %+v, want [sess-1]", calls)
	}
	if calls := backend.PullFileCalls(); len(calls) != 1 || calls[0].Path != "/tmp/report.md" {
		t.Fatalf("PullFileCalls() = %+v, want one pull for /tmp/report.md", calls)
	}
}

func TestPullArtifactFromSandboxBackendDoesNotFallbackBeforePull(t *testing.T) {
	ctx := context.Background()
	backend := mock.New()
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: "sess-1", TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatalf("CreateSession() err = %v", err)
	}
	backend.PullFileFn = func(sessionID, path string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("live content wins")), nil
	}

	content, ok := pullArtifactFromSandboxBackend(ctx, backend, "sess-1", "/tmp/report.md")
	if !ok {
		t.Fatal("expected sandbox content")
	}
	if string(content) != "live content wins" {
		t.Fatalf("content = %q, want live sandbox content", content)
	}
}
