package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
	"github.com/SAP/astonish/pkg/store"
)

type artifactOwnerStore struct{ store.SessionStore }

func (artifactOwnerStore) GetSessionMeta(ctx context.Context, id string) (*store.SessionMeta, error) {
	if id != "owned" {
		return nil, nil
	}
	return &store.SessionMeta{UserID: "owner"}, nil
}
func (artifactOwnerStore) ReadTranscriptEvents(context.Context, string, string, string) ([]*adksession.Event, error) {
	return nil, nil
}
func TestArtifactPlatformNeverReadsHost(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(secret, []byte("HOST_SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.html")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	for _, path := range []string{secret, link} {
		for _, handler := range []http.HandlerFunc{StudioArtifactContentHandler, StudioArtifactDownloadHandler, StudioArtifactPDFHandler} {
			for _, sessionID := range []string{"", "missing", "owned"} {
				req := httptest.NewRequest("GET", "/?path="+url.QueryEscape(path)+"&session="+sessionID, nil)
				req = req.WithContext(WithPlatformUser(store.WithServices(req.Context(), &store.Services{PersonalSessions: artifactOwnerStore{}}), &PlatformUser{ID: "owner"}))
				req = withSandboxRegistry(req, registry)
				w := httptest.NewRecorder()
				handler(w, req)
				if w.Code != 404 || strings.Contains(w.Body.String(), "HOST_SECRET") {
					t.Fatalf("session=%q status=%d body=%s", sessionID, w.Code, w.Body.String())
				}
			}
		}
	}

}

func TestArtifactTrustedLocalDownload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(path, []byte("<h1>export</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/?path="+url.QueryEscape(path), nil)
	w := httptest.NewRecorder()
	StudioArtifactDownloadHandler(w, req)
	if w.Code != 200 || w.Body.String() != "<h1>export</h1>" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("local download: %d %s", w.Code, w.Body.String())
	}
}

func TestArtifactSafetyHeaders(t *testing.T) {
	for _, name := range []string{"active.html", "active.js", "active.svg"} {
		w := httptest.NewRecorder()
		serveArtifactDownload(w, name, []byte("<script>alert(1)</script>"))
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
			t.Fatal("missing safety headers")
		}
	}
}

func TestArtifactDownloadDisposition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(StudioArtifactDownloadHandler))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name        string
		content     string
		disposition string
	}{
		{"active.html", "<script>alert(1)</script>", "attachment"},
		{"active.js", "alert(1)", "attachment"},
		{"active.svg", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`, "attachment"},
		{"active.SVG", `<svg xmlns="http://www.w3.org/2000/svg"/>`, "attachment"},
		{"active.xhtml", "<html/>", "attachment"},
		{"unknown", "<html><script>alert(1)</script></html>", "attachment"},
		{"photo.png", "raster fixture", "inline"},
		{"photo.jpg", "raster fixture", "inline"},
		{"animation.gif", "raster fixture", "inline"},
		{"photo.webp", "raster fixture", "inline"},
		{"video.mp4", "video fixture", "inline"},
		{"audio.mp3", "audio fixture", "inline"},
		{"report.md", "# Report", "attachment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			resp, err := server.Client().Get(server.URL + "/?path=" + url.QueryEscape(path))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != tc.content {
				t.Fatalf("download failed: status=%d", resp.StatusCode)
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), tc.disposition+";") {
				t.Fatalf("disposition=%q, want %s", resp.Header.Get("Content-Disposition"), tc.disposition)
			}
			if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
				t.Fatal("download missing safety headers")
			}
		})
	}
}

type artifactTranscriptStore struct {
	artifactOwnerStore
	path    string
	content string
	events  []*adksession.Event
}

func (s artifactTranscriptStore) ReadTranscriptEvents(_ context.Context, _, userID, sessionID string) ([]*adksession.Event, error) {
	if userID != "owner" || sessionID != "owned" {
		return nil, fmt.Errorf("unauthorized transcript")
	}
	if s.events != nil {
		return s.events, nil
	}
	path, content := s.path, s.content
	if path == "" {
		path, content = "/tmp/transcript-artifact.md", "authorized transcript artifact"
	}
	return []*adksession.Event{{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
		FunctionCall: &genai.FunctionCall{Name: "write_file", Args: map[string]any{
			"file_path": path, "content": content,
		}},
	}}}}}}, nil
}

func TestArtifactWriteEditContentSurvivesPlatformRestore(t *testing.T) {
	const path = "/tmp/final-report.md"
	const initial = "# Durable report\n\n<!-- next -->\n"
	const finalContent = "# Durable report\n\nFirst section.\n\nSecond section.\n\n<!-- next -->\n"
	parts := []*genai.Part{
		{FunctionCall: &genai.FunctionCall{ID: "write", Name: "write_file", Args: map[string]any{"file_path": path, "content": initial}}},
		{FunctionResponse: &genai.FunctionResponse{ID: "write", Name: "write_file", Response: map[string]any{"path": path}}},
		{FunctionCall: &genai.FunctionCall{ID: "edit-1", Name: "edit_file", Args: map[string]any{"path": path, "old_string": "<!-- next -->", "new_string": "First section.\n\n<!-- next -->"}}},
		{FunctionResponse: &genai.FunctionResponse{ID: "edit-1", Name: "edit_file", Response: map[string]any{"path": path}}},
		{FunctionCall: &genai.FunctionCall{ID: "edit-2", Name: "edit_file", Args: map[string]any{"path": path, "old_string": "<!-- next -->", "new_string": "Second section.\n\n<!-- next -->"}}},
		{FunctionResponse: &genai.FunctionResponse{ID: "edit-2", Name: "edit_file", Response: map[string]any{"path": path}}},
	}
	events := []*adksession.Event{{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: parts}}}}
	if got, ok := readArtifactContentFromEventSlice(events, path); !ok || got != finalContent {
		t.Fatalf("write/edit reconstruction = %q, %v; want=%q", got, ok, finalContent)
	}

	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	for _, handler := range []http.HandlerFunc{StudioArtifactContentHandler, StudioArtifactDownloadHandler} {
		r := httptest.NewRequest(http.MethodGet, "/?path="+url.QueryEscape(path)+"&session=owned", nil)
		r = r.WithContext(WithPlatformUser(store.WithServices(r.Context(), &store.Services{
			PersonalSessions: artifactTranscriptStore{events: events},
		}), &PlatformUser{ID: "owner"}))
		r = withSandboxRegistry(r, registry)
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusOK || w.Body.String() != finalContent {
			t.Fatalf("write/edit artifact unavailable after restore: status=%d body=%q", w.Code, w.Body.String())
		}
	}
}

func TestArtifactPlatformTranscriptAvailableDuringContainment(t *testing.T) {
	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	for _, handler := range []http.HandlerFunc{StudioArtifactContentHandler, StudioArtifactDownloadHandler} {
		for _, userID := range []string{"owner", "teammate"} {
			r := httptest.NewRequest(http.MethodGet, "/?path=/tmp/transcript-artifact.md&session=owned", nil)
			r = r.WithContext(WithPlatformUser(store.WithServices(r.Context(), &store.Services{PersonalSessions: artifactTranscriptStore{}}), &PlatformUser{ID: userID}))
			r = withSandboxRegistry(r, registry)
			w := httptest.NewRecorder()
			handler(w, r)
			if userID == "owner" {
				if w.Code != http.StatusOK || w.Body.String() != "authorized transcript artifact" {
					t.Fatalf("authorized transcript unavailable: status=%d", w.Code)
				}
			} else if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "authorized transcript artifact") {
				t.Fatalf("foreign transcript exposed: status=%d", w.Code)
			}
		}
	}
}

func TestArtifactPlatformActiveTranscriptDelivery(t *testing.T) {
	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	const payload = `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`
	for _, ext := range []string{"svg", "html", "js"} {
		path := "/tmp/active." + ext
		for _, endpoint := range []string{"download", "content"} {
			for _, user := range []string{"owner", "teammate"} {
				t.Run(ext+"/"+endpoint+"/"+user, func(t *testing.T) {
					r := httptest.NewRequest(http.MethodGet, "/?path="+url.QueryEscape(path)+"&session=owned", nil)
					r = r.WithContext(WithPlatformUser(store.WithServices(r.Context(), &store.Services{
						PersonalSessions: artifactTranscriptStore{path: path, content: payload},
					}), &PlatformUser{ID: user}))
					r = withSandboxRegistry(r, registry)
					w := httptest.NewRecorder()
					if endpoint == "download" {
						StudioArtifactDownloadHandler(w, r)
					} else {
						StudioArtifactContentHandler(w, r)
					}
					if user != "owner" {
						if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), payload) {
							t.Fatalf("foreign artifact exposed: status=%d", w.Code)
						}
						return
					}
					if w.Code != http.StatusOK || w.Body.String() != payload {
						t.Fatalf("owner artifact unavailable: status=%d", w.Code)
					}
					if endpoint == "download" && !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
						t.Fatal("active transcript served inline")
					}
					if endpoint == "content" && w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
						t.Fatal("active transcript preview is not inert text")
					}
					if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
						t.Fatal("active transcript missing safety headers")
					}
				})
			}
		}
	}
}

type artifactConfinedBackend struct {
	*mock.MockBackend
	directArtifact string
}

func (b *artifactConfinedBackend) ArtifactReadsConfined() bool { return true }
func (b *artifactConfinedBackend) PullArtifact(ctx context.Context, sessionID, path string) (io.ReadCloser, error) {
	if b.directArtifact != "" {
		return io.NopCloser(strings.NewReader(b.directArtifact)), nil
	}
	return b.MockBackend.PullFile(ctx, sessionID, path)
}

func TestPullArtifactPlatformAllowsConfinedBackend(t *testing.T) {
	ctx := WithPlatformUser(context.Background(), &PlatformUser{ID: "owner"})
	backend := &artifactConfinedBackend{MockBackend: mock.New()}
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: "owned", TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatal(err)
	}
	if err := backend.PushFile(ctx, "owned", "/root/shell-created.md", strings.NewReader("visible live artifact"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, ok := pullArtifactFromSandboxBackend(ctx, backend, "owned", "/root/shell-created.md")
	if !ok || string(content) != "visible live artifact" {
		t.Fatalf("confined platform artifact unavailable: ok=%v content=%q", ok, content)
	}
}

func TestPullArtifactPlatformFallsBackFromMissingScopedRegistry(t *testing.T) {
	ctx := WithPlatformUser(context.Background(), &PlatformUser{ID: "owner"})
	missingScoped := &artifactConfinedBackend{MockBackend: mock.New(), directArtifact: "visible live artifact"}

	content, ok := pullAuthorizedArtifactFromSandboxBackend(ctx, missingScoped, "owned", "/root/shell-created.md")
	if !ok || string(content) != "visible live artifact" {
		t.Fatalf("authorized read without sandbox registry row unavailable: ok=%v content=%q", ok, content)
	}
}

func TestPullArtifactPlatformRejectsUnconfinedBackendBeforeEffects(t *testing.T) {
	for _, withUser := range []bool{false, true} {
		t.Run(fmt.Sprintf("platform-user=%v", withUser), func(t *testing.T) {
			ctx := context.Background()
			if withUser {
				ctx = WithPlatformUser(ctx, &PlatformUser{ID: "owner"})
			} else {
				ctx = store.WithServices(ctx, &store.Services{Mode: store.ModePlatform})
			}
			backend := &artifactUnconfinedBackend{}
			content, ok := pullArtifactFromSandboxBackend(ctx, backend, "owned", "/tmp/report.md")
			if ok || content != nil || backend.calls != 0 {
				t.Fatalf("unconfined platform transfer: ok=%v backend calls=%d", ok, backend.calls)
			}
		})
	}
}

// Deliberately returns readable bytes for any path. Platform artifact callers
// must reject this broad interface before state queries, resume or file access.
type artifactUnconfinedBackend struct {
	sandbox.Backend
	calls int
}

func (b *artifactUnconfinedBackend) SessionState(context.Context, string) (sandbox.SessionState, error) {
	b.calls++
	return sandbox.SessionStateRunning, nil
}

func (b *artifactUnconfinedBackend) PullFile(context.Context, string, string) (io.ReadCloser, error) {
	b.calls++
	return io.NopCloser(strings.NewReader("sandbox-secret-canary")), nil
}

func TestPullArtifactRejectsStreamCloseFailure(t *testing.T) {
	ctx := context.Background()
	backend := mock.New()
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: "sess-1", TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatal(err)
	}
	reader := &artifactFailedCloseReader{Reader: strings.NewReader("incomplete artifact")}
	backend.PullFileFn = func(string, string) (io.ReadCloser, error) { return reader, nil }
	content, ok := pullArtifactFromSandboxBackend(ctx, backend, "sess-1", "/tmp/report.md")
	if ok || content != nil {
		t.Fatal("accepted bytes from a failed sandbox transfer")
	}
	if reader.closes != 1 {
		t.Fatalf("reader closed %d times, want once", reader.closes)
	}
}

type artifactFailedCloseReader struct {
	io.Reader
	closes int
}

func (r *artifactFailedCloseReader) Close() error {
	r.closes++
	return io.ErrUnexpectedEOF
}

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
