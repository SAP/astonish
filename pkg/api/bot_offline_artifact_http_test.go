package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

type offlineArtifactSessions struct {
	store.SessionStore
	meta   *store.SessionMeta
	events []*adksession.Event
}

func (s *offlineArtifactSessions) GetSessionMeta(_ context.Context, id string) (*store.SessionMeta, error) {
	if id != s.meta.ID {
		return nil, nil
	}
	return s.meta, nil
}
func (s *offlineArtifactSessions) ReadTranscriptEvents(context.Context, string, string, string) ([]*adksession.Event, error) {
	return s.events, nil
}

type offlineArtifactBackend struct {
	sandbox.Backend
	sandbox.BotHomeFilesBackend
	want                           sandbox.BotHomeBinding
	immutable                      []byte
	contentCalls, closes           int
	ref                            store.BotContentRef
	readFail, closeFail, nilStream bool
	data                           []byte
	calls                          int
	fail                           bool
}

func (b *offlineArtifactBackend) ReadBotHomeArtifact(_ context.Context, binding *sandbox.BotHomeBinding, path string, max int64) ([]byte, error) {
	b.calls++
	if *binding != b.want || path != "reports/result.bin" || max != sandbox.MaxBotHomeArtifactBytes || b.fail {
		return nil, fmt.Errorf("denied")
	}
	return b.data, nil
}

type offlineArtifactStream struct {
	io.Reader
	backend *offlineArtifactBackend
}

func (s *offlineArtifactStream) Read(p []byte) (int, error) {
	if s.backend.readFail {
		return 0, fmt.Errorf("terminal read failure")
	}
	return s.Reader.Read(p)
}
func (s *offlineArtifactStream) Close() error {
	s.backend.closes++
	if s.backend.closeFail {
		return fmt.Errorf("terminal close failure")
	}
	return nil
}
func (b *offlineArtifactBackend) OpenBotContent(_ context.Context, binding *sandbox.BotHomeBinding, ref store.BotContentRef, br store.BotFileRange) (*store.BotFileStream, error) {
	b.contentCalls++
	if *binding != b.want || ref != b.ref || br != (store.BotFileRange{}) || b.fail {
		return nil, fmt.Errorf("denied")
	}
	if b.nilStream {
		return nil, nil
	}
	return &store.BotFileStream{ReadCloser: &offlineArtifactStream{Reader: bytes.NewReader(b.immutable), backend: b}, Entry: store.BotFileEntry{SizeBytes: ref.SizeBytes, SHA256: ref.SHA256}}, nil
}

// Real HTTP requests use the production backend factory with an empty (destroyed
// run) registry. The backend has no compute methods: any recreation/resume panics.
func TestBotOfflineArtifactHTTP(t *testing.T)           { testBotOfflineArtifactHTTP(t, false) }
func TestBotOfflineArtifactContentRefHTTP(t *testing.T) { testBotOfflineArtifactHTTP(t, true) }
func testBotOfflineArtifactHTTP(t *testing.T, immutable bool) {
	dir := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(key, dir)
	}
	t.Setenv("SUDO_USER", "")
	enabled := true
	if err := config.SaveAppConfig(&config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled, Backend: "mock"}}); err != nil {
		t.Fatal(err)
	}
	actor, owner, bid, rid, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	data := []byte{0, 1, 2, 255, 128, 42}
	digest := sha256.Sum256(data)
	home := store.BotHomeBinding{ID: uuid.New(), BotID: bid, Scope: store.BotScopeTeam, Backend: "mock", StorageKey: "retained-home", Revision: 1}
	artifact := store.BotArtifact{ID: uuid.New(), HomeID: home.ID, RelativePath: "reports/result.bin", SHA256: hex.EncodeToString(digest[:])}
	repo := &botSessionFixtureStore{b: store.Bot{ID: bid, OwnerID: owner, Scope: store.BotScopeTeam, Home: home}, run: store.BotRun{ID: rid, BotID: bid, SessionID: sid, Status: store.RunSucceeded, Report: store.BotReport{Artifacts: []store.BotArtifact{artifact}}}}
	ss := &offlineArtifactSessions{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: bid.String(), BotRunID: rid.String(), Purpose: store.SessionPurposeBotRun, Retention: store.SessionRetentionBot}}
	svc := &store.Services{Sessions: ss, Bots: repo}
	backend := &offlineArtifactBackend{data: data, want: sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: owner.String(), BotID: bid.String()}, Backend: sandbox.BackendKindMock, Ref: home.StorageKey}}
	if immutable {
		ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: owner.String(), BotID: bid}, Kind: store.BotContentArtifact, Key: "published/result.bin", SHA256: artifact.SHA256, SizeBytes: int64(len(data)), Revision: 1}
		artifact.ContentRef = &ref
		artifact.SizeBytes = ref.SizeBytes
		repo.run.Report.Artifacts[0] = artifact
		backend.ref = ref
		backend.immutable = append([]byte{}, data...)
		backend.data = []byte("overwritten working file")
	}
	sandbox.RegisterBackendFactory(sandbox.BackendKindMock, func(sandbox.BackendFactoryConfig) (sandbox.Backend, error) { return backend, nil })
	t.Cleanup(func() {
		sandbox.RegisterBackendFactory(sandbox.BackendKindMock, func(sandbox.BackendFactoryConfig) (sandbox.Backend, error) { return mock.New(), nil })
	})
	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team := r.Header.Get("X-Team")
		if team == "" {
			team = "team"
		}
		ctx, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: team, Authenticated: true})
		if err != nil {
			http.Error(w, "fixture", 500)
			return
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor.String(), OrgSlug: "org", TeamSlug: team})
		ctx = WithPlatformUser(ctx, &PlatformUser{ID: actor.String(), OrgSlug: "org", TeamSlug: team, Role: "member"})
		r = withSandboxRegistry(r.WithContext(store.WithServices(ctx, svc)), registry)
		if r.URL.Path == "/content" {
			StudioArtifactContentHandler(w, r)
		} else {
			StudioArtifactDownloadHandler(w, r)
		}
	}))
	defer server.Close()
	get := func(route, path, team string, want int, body []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+route+"?session="+sid.String()+"&path="+url.QueryEscape(path), nil)
		req.Header.Set("X-Team", team)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want || (want == 200 && !bytes.Equal(got, body)) {
			t.Fatalf("%s %s: %d %q", route, path, res.StatusCode, got)
		}
	}
	for _, route := range []string{"/download", "/content"} {
		get(route, "reports/result.bin", "", 200, data)
		get(route, "/root/reports/result.bin", "", 200, data)
	}
	if immutable {
		if backend.calls != 0 || backend.contentCalls != 4 || backend.closes != 4 {
			t.Fatalf("immutable read lifecycle: %+v", backend)
		}
		backend.data = nil // Deleted working file must not affect either alias.
		for _, route := range []string{"/download", "/content"} {
			for _, alias := range []string{artifact.RelativePath, "/root/" + artifact.RelativePath} {
				get(route, alias, "", 200, data)
			}
		}
		for _, corrupt := range []func(*store.BotContentRef){
			func(r *store.BotContentRef) { r.Kind = store.BotContentMemory },
			func(r *store.BotContentRef) { r.Scope.BotID = uuid.New() },
			func(r *store.BotContentRef) { r.Scope.OwnerID = uuid.NewString() },
			func(r *store.BotContentRef) { r.Scope.OrgID = "other" },
			func(r *store.BotContentRef) { r.Scope.TeamID = "other" },
			func(r *store.BotContentRef) { r.SizeBytes++ },
			func(r *store.BotContentRef) { r.SizeBytes = sandbox.MaxBotHomeArtifactBytes + 1 },
			func(r *store.BotContentRef) { r.SHA256 = fmt.Sprintf("%064x", 1) },
		} {
			bad := backend.ref
			corrupt(&bad)
			repo.run.Report.Artifacts[0].ContentRef = &bad
			get("/download", artifact.RelativePath, "", 404, nil)
		}
		repo.run.Report.Artifacts[0] = artifact
		for _, flag := range []*bool{&backend.fail, &backend.readFail, &backend.closeFail, &backend.nilStream} {
			*flag = true
			get("/download", artifact.RelativePath, "", 404, nil)
			*flag = false
		}
		backend.immutable = []byte("bad snapshot")
		get("/content", artifact.RelativePath, "", 404, nil)
		backend.immutable = data
		if backend.calls != 0 {
			t.Fatal("immutable reference fell back to Home")
		}
	} else if backend.calls != 4 {
		t.Fatalf("offline backend not used: %d", backend.calls)
	}
	get("/download", "/root/secret.bin", "", 404, nil)
	get("/download", "../reports/result.bin", "", 400, nil)
	repo.denied = true
	get("/download", artifact.RelativePath, "", 404, nil)
	repo.denied = false
	repo.b.OwnerID = uuid.New()
	get("/download", artifact.RelativePath, "", 404, nil)
	repo.b.OwnerID = owner
	repo.b.Scope = store.BotScopePersonal
	get("/download", artifact.RelativePath, "", 404, nil)
	repo.b.Scope = store.BotScopeTeam
	get("/download", artifact.RelativePath, "other-team", 404, nil)
	repo.run.Report.Artifacts[0].HomeID = uuid.New()
	get("/download", artifact.RelativePath, "", 404, nil)
	repo.run.Report.Artifacts[0] = artifact
	repo.run.Report.Artifacts[0].SHA256 = "invalid"
	get("/download", artifact.RelativePath, "", 404, nil)
	repo.run.Report.Artifacts[0] = artifact
	repo.run.Report.Artifacts[0].RelativePath = "reports//result.bin"
	get("/download", "reports//result.bin", "", 404, nil)
	repo.run.Report.Artifacts[0] = artifact
	// A matching transcript must not mask retained-storage corruption.
	ss.events = []*adksession.Event{artifactToolCallEvent("inv", "write", "write_file", map[string]any{"path": "/root/reports/result.bin", "content": string(data)}), toolResponseEvent("inv", "write", "write_file", map[string]any{"success": true})}
	backend.data = []byte("wrong hash")
	if immutable {
		backend.immutable = backend.data
	}
	get("/download", artifact.RelativePath, "", 404, nil)
	backend.data = data
	backend.immutable = data
	backend.fail = true
	get("/download", artifact.RelativePath, "", 404, nil)
	backend.fail = false
	// An ordinary Chat owner still gets the transcript tier without home reads.
	before := backend.calls
	ss.meta.BotID = ""
	ss.meta.BotRunID = ""
	ss.meta.Retention = ""
	ss.meta.Purpose = store.SessionPurposeChat
	ss.meta.UserID = actor.String()
	ss.events = []*adksession.Event{artifactToolCallEvent("inv", "write", "write_file", map[string]any{"path": "/root/chat.txt", "content": "ordinary chat"}), toolResponseEvent("inv", "write", "write_file", map[string]any{"success": true})}
	// Disable sandbox so this checks the existing transcript fallback alone.
	enabled = false
	if err := config.SaveAppConfig(&config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled, Backend: "mock"}}); err != nil {
		t.Fatal(err)
	}
	get("/content", "/root/chat.txt", "", 200, []byte("ordinary chat"))
	if backend.calls != before {
		t.Fatal("ordinary Chat invoked Bot storage")
	}
}
