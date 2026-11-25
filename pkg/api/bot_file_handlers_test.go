package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type fileDeliveryRepo struct {
	store.BotStore
	mu     sync.Mutex
	b      store.Bot
	pub    store.BotPublication
	denied bool
}

func (s *fileDeliveryRepo) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || s.denied || id != s.b.ID || p.TeamSlug != "team" || p.OrgSlug != "org" || (s.b.Scope == store.BotScopePersonal && p.Subject != s.b.OwnerID.String()) {
		return nil, store.ErrBotForbidden
	}
	b := s.b
	return &b, nil
}
func (s *fileDeliveryRepo) ReadBotPublication(ctx context.Context, b store.BotID, r store.RunID, seq int64) (*store.BotPublication, error) {
	if _, err := s.Get(ctx, b); err != nil {
		return nil, err
	}
	if r != s.pub.RunID || seq != s.pub.ReportSequence {
		return nil, store.ErrBotNotFound
	}
	p := s.pub
	return &p, nil
}

type fileDeliveryFiles struct {
	store.BotFiles
	mu        sync.Mutex
	data      map[string][]byte
	immutable []byte
	opens     atomic.Int64
	closed    atomic.Int64
	fail      bool
}

func fileDeliveryEntry(p string, data []byte) store.BotFileEntry {
	h := sha256.Sum256(data)
	return store.BotFileEntry{Path: p, Kind: store.BotFileRegular, SizeBytes: int64(len(data)), Freshness: hex.EncodeToString(h[:])}
}
func (f *fileDeliveryFiles) List(_ context.Context, _ store.BotID, p, c string, n int) (*store.BotFilePage, error) {
	if p != "" || c != "" || n != 100 {
		return nil, store.ErrBotInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &store.BotFilePage{Entries: []store.BotFileEntry{fileDeliveryEntry(".astonish/authority.json", []byte("secret")), fileDeliveryEntry(".notes.md", f.data[".notes.md"]), fileDeliveryEntry(".ssh/id_rsa", []byte("secret")), fileDeliveryEntry(".stage-incomplete", []byte("partial"))}}
	for _, p := range []string{"report.md", "large.bin", "page.html"} {
		out.Entries = append(out.Entries, fileDeliveryEntry(p, f.data[p]))
	}
	return out, nil
}
func (f *fileDeliveryFiles) Stat(_ context.Context, _ store.BotID, p string) (*store.BotFileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[p]
	if !ok {
		return nil, store.ErrBotNotFound
	}
	e := fileDeliveryEntry(p, d)
	return &e, nil
}
func (f *fileDeliveryFiles) Open(ctx context.Context, id store.BotID, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	f.mu.Lock()
	d, ok := f.data[p]
	f.mu.Unlock()
	if !ok {
		return nil, store.ErrBotNotFound
	}
	return f.stream(ctx, p, d, r)
}
func (f *fileDeliveryFiles) OpenContent(ctx context.Context, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	return f.stream(ctx, "private-object", f.immutable, r)
}

type fileDeliveryReader struct {
	io.Reader
	f    *fileDeliveryFiles
	once sync.Once
}

func (r *fileDeliveryReader) Close() error { r.once.Do(func() { r.f.closed.Add(1) }); return nil }
func (f *fileDeliveryFiles) stream(_ context.Context, p string, d []byte, r store.BotFileRange) (*store.BotFileStream, error) {
	f.opens.Add(1)
	e := fileDeliveryEntry(p, d)
	end := int64(len(d))
	if r.Length > 0 {
		end = r.Offset + r.Length
	}
	var reader io.Reader = bytes.NewReader(d[r.Offset:end])
	if f.fail {
		reader = &fileDeliveryFailure{Reader: reader}
	}
	return &store.BotFileStream{Entry: e, ReadCloser: &fileDeliveryReader{Reader: reader, f: f}}, nil
}

type fileDeliveryFailure struct{ io.Reader }

func (r *fileDeliveryFailure) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if err == io.EOF {
		return n, fmt.Errorf("terminal failure")
	}
	return n, err
}

func TestBotPublicFilePathVisibility(t *testing.T) {
	for _, p := range []string{".notes.md", ".editorconfig", ".hidden/draft.md", "project/.gitignore", ".config/editor/settings.json", "work/.notes/plan.md"} {
		if !botPublicFilePath(p, false) {
			t.Errorf("ordinary hidden user file denied: %s", p)
		}
	}
	for _, p := range []string{".ssh/key", "work/.aws/credentials", ".config/astonish/credentials.yaml", ".config/gcloud/token", ".local/share/astonish/sessions/s", ".local/state/astonish/runtime", ".cache/astonish/key", ".astonish/authority", ".astonish-workspace/receipt", "work/.stage-incomplete", "work/.tmp-partial", ".env.local", ".git/config", ".netrc", ".npmrc", "../secret", "a/../secret", "a\tname", "a\x7fname"} {
		if botPublicFilePath(p, false) {
			t.Errorf("protected or invalid file allowed: %s", p)
		}
	}
	if !botPublicFilePath("", true) || botPublicFilePath("", false) {
		t.Fatal("root path semantics")
	}
}

type filePreviewCredentialStore struct {
	mockCredentialStore
	value   string
	fail    bool
	reloads atomic.Int64
}

func (s *filePreviewCredentialStore) Reload(context.Context) error {
	s.reloads.Add(1)
	if s.fail {
		return store.ErrBotUnsupported
	}
	return nil
}
func (s *filePreviewCredentialStore) List(context.Context) map[string]store.CredentialType {
	return map[string]store.CredentialType{"report-access": store.CredAPIKey}
}
func (s *filePreviewCredentialStore) Get(context.Context, string) *store.Credential {
	return &store.Credential{Type: store.CredAPIKey, Value: s.value}
}

func TestBotFileDeliveryHTTP(t *testing.T) {
	owner, bid, rid := uuid.New(), uuid.New(), uuid.New()
	large := bytes.Repeat([]byte{0, 255, 18, 201, 7, 44, 63}, (17<<20)/7+1)
	files := &fileDeliveryFiles{data: map[string][]byte{"report.md": []byte("# Old report\n"), "large.bin": large, "page.html": []byte("<script>alert(1)</script>"), "invalid.txt": {255, 0}, "big.txt": bytes.Repeat([]byte("x"), 256<<10+1)}, immutable: []byte("# Old report\n")}
	h := sha256.Sum256(files.immutable)
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Kind: store.BotContentArtifact, Scope: store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: owner.String(), BotID: bid}, Key: "artifacts/object", SHA256: hex.EncodeToString(h[:]), SizeBytes: int64(len(files.immutable)), Revision: 1}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: bid, Scope: store.BotScopePersonal, Revision: 1}
	aid := uuid.New()
	repo := &fileDeliveryRepo{b: store.Bot{ID: bid, OwnerID: owner, Scope: store.BotScopePersonal, Home: home, HomeSessionID: uuid.New()}}
	repo.pub = store.BotPublication{BotID: bid, RunID: rid, ReportSequence: 7, HomeSessionID: repo.b.HomeSessionID, EventID: uuid.NewString(), Artifacts: []store.BotArtifact{{ID: aid, HomeID: home.ID, RelativePath: "report.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}}
	svc := &store.Services{Credentials: &mockCredentialStore{}, PersonalBots: repo, Bots: nil, BotFilesFactory: func(_ context.Context, _ *store.Services, scope store.BotScope) (store.BotFiles, error) {
		if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
			return nil, store.ErrBotForbidden
		}
		return files, nil
	}}
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-No-Auth") != "" {
			router.ServeHTTP(w, r)
			return
		}
		user := owner.String()
		if r.Header.Get("X-User") != "" {
			user = r.Header.Get("X-User")
		}
		team := "team"
		if r.Header.Get("X-Team") != "" {
			team = r.Header.Get("X-Team")
		}
		p := execution.Principal{Kind: execution.PrincipalKindUser, Subject: user, OrgSlug: "org", TeamSlug: team, Authenticated: true, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio}
		ctx, err := execution.WithPrincipal(r.Context(), p)
		if err != nil {
			t.Error(err)
			return
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: user, OrgSlug: "org", TeamSlug: team})
		router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
	}))
	defer server.Close()
	base := "/api/studio/bots/" + bid.String()
	artifact := base + "/runs/" + rid.String() + "/reports/7/artifacts/" + aid.String()
	request := func(method, target string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+target, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}
	check := func(method, target string, headers map[string]string, status int) []byte {
		t.Helper()
		res, body := request(method, target, headers)
		if res.StatusCode != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, target, res.StatusCode, status, body)
		}
		return body
	}
	t.Run("scoped preview redaction and raw authorized download", func(t *testing.T) {
		secret := uuid.NewString() + uuid.NewString()
		raw := []byte("# Report\nConfigured access: " + secret + "\n")
		cs := &filePreviewCredentialStore{value: secret}
		oldPub, oldImmutable := repo.pub, files.immutable
		oldReport := files.data["report.md"]
		defer func() {
			repo.pub, files.immutable, files.data["report.md"] = oldPub, oldImmutable, oldReport
			repo.b.Scope, repo.b.Home.Scope = store.BotScopePersonal, store.BotScopePersonal
			svc.Credentials, svc.PersonalCredentials, svc.Bots = &mockCredentialStore{}, nil, nil
		}()
		files.data["report.md"], files.immutable = raw, raw
		digest := sha256.Sum256(raw)
		updated := ref
		updated.SHA256, updated.SizeBytes = hex.EncodeToString(digest[:]), int64(len(raw))
		repo.pub.Artifacts = []store.BotArtifact{{ID: aid, HomeID: home.ID, RelativePath: "report.md", ContentRef: &updated, SHA256: updated.SHA256, SizeBytes: updated.SizeBytes}}
		svc.PersonalCredentials, svc.Credentials = cs, &mockCredentialStore{}
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			repo.b.Scope, repo.b.Home.Scope = scope, scope
			users := []string{owner.String()}
			if scope == store.BotScopeTeam {
				svc.Bots, svc.Credentials = repo, cs
				// A viewer's unrelated private store must not be opened.
				svc.PersonalCredentials = &filePreviewCredentialStore{fail: true}
				users = append(users, uuid.NewString())
			}
			for _, user := range users {
				headers := map[string]string{"X-User": user}
				before, reloads := files.opens.Load(), cs.reloads.Load()
				res, body := request("GET", base+"/files?scope="+string(scope), headers)
				if res.StatusCode != 200 || bytes.Contains(body, []byte(secret)) || files.opens.Load() != before || cs.reloads.Load() != reloads {
					t.Fatal("listing eagerly read content or credentials")
				}
				for _, target := range []string{base + "/files/preview?path=report.md&scope=" + string(scope), artifact + "/preview?scope=" + string(scope)} {
					res, body = request("GET", target, headers)
					var preview struct{ Content string }
					if res.StatusCode != 200 || json.Unmarshal(body, &preview) != nil || preview.Content != "# Report\nConfigured access: [REDACTED]\n" || bytes.Contains(body, []byte(secret)) {
						t.Fatal("preview did not apply scoped credential redaction")
					}
				}
				for _, target := range []string{base + "/files/download?path=report.md&scope=" + string(scope), artifact + "/download?scope=" + string(scope)} {
					res, body = request("GET", target, headers)
					if res.StatusCode != 200 || !bytes.Equal(body, raw) {
						t.Fatal("authorized download changed stored bytes")
					}
				}
			}
		}
		for _, csFailure := range []store.CredentialStore{nil, &filePreviewCredentialStore{fail: true}} {
			svc.Credentials = csFailure
			before := files.opens.Load()
			res, body := request("GET", base+"/files/preview?path=report.md&scope=team", nil)
			if res.StatusCode != 503 || bytes.Contains(body, []byte(secret)) || files.opens.Load() != before {
				t.Fatal("preview redactor acquisition did not fail closed")
			}
		}
		res, body := request("GET", artifact+"/download?scope=team", map[string]string{"X-Team": "other"})
		if res.StatusCode != 403 || bytes.Contains(body, []byte(secret)) {
			t.Fatal("unauthorized download exposed content")
		}
	})
	t.Run("explicit hidden visibility never grants private access", func(t *testing.T) {
		files.data[".notes.md"] = []byte("# Hidden notes")
		body := check("GET", base+"/files", nil, 200)
		if bytes.Contains(body, []byte(".notes.md")) {
			t.Fatal("hidden entry shown by default")
		}
		body = check("GET", base+"/files?show_hidden=true", nil, 200)
		if !bytes.Contains(body, []byte(".notes.md")) || bytes.Contains(body, []byte(".ssh")) || bytes.Contains(body, []byte(".stage-")) || bytes.Contains(body, []byte(".astonish")) {
			t.Fatalf("hidden filter: %s", body)
		}
		check("GET", base+"/files?show_hidden=invalid", nil, 400)
		check("GET", base+"/files/preview?path=.notes.md", nil, 200)
		for _, p := range []string{".ssh/id_rsa", ".aws/credentials", ".config/astonish/credentials.yaml", ".stage-incomplete", "work/.tmp-interrupted"} {
			for _, action := range []string{"stat", "preview", "download"} {
				check("GET", base+"/files/"+action+"?show_hidden=true&path="+url.QueryEscape(p), nil, 400)
			}
		}
	})
	t.Run("lazy metadata", func(t *testing.T) {
		opens := files.opens.Load()
		body := check("GET", base+"/files", nil, 200)
		if files.opens.Load() != opens || bytes.Contains(body, []byte("authority")) || bytes.Contains(body, []byte("Old report")) {
			t.Fatalf("eager/private listing %s", body)
		}
		check("GET", base+"/files/stat?path=large.bin", nil, 200)
		if files.opens.Load() != opens {
			t.Fatal("stat opened content")
		}
	})
	t.Run("stream greater than 16MiB", func(t *testing.T) {
		res, body := request("GET", base+"/files/download?path=large.bin", nil)
		if res.StatusCode != 200 || !bytes.Equal(body, large) || res.ContentLength != int64(len(large)) || res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment;") {
			t.Fatal("stream mismatch")
		}
	})
	t.Run("head and ranges", func(t *testing.T) {
		before := files.opens.Load()
		res, body := request("HEAD", base+"/files/download?path=large.bin", nil)
		if len(body) != 0 || res.ContentLength != int64(len(large)) || files.opens.Load() != before {
			t.Fatal("head read content")
		}
		for _, rg := range []string{"bytes=2-8", "bytes=-7", "bytes=17825790-"} {
			res, body = request("GET", base+"/files/download?path=large.bin", map[string]string{"Range": rg})
			if res.StatusCode != 206 || int64(len(body)) != res.ContentLength || res.Header.Get("Content-Range") == "" {
				t.Fatalf("range %s: %d", rg, res.StatusCode)
			}
		}
		for _, rg := range []string{"bytes=999999999-", "bytes=0-1,3-4", "bytes=-0", "bytes=0--1"} {
			check("GET", base+"/files/download?path=large.bin", map[string]string{"Range": rg}, 416)
		}
	})
	t.Run("source only bounded UTF8", func(t *testing.T) {
		body := check("GET", base+"/files/preview?path=page.html", nil, 200)
		if bytes.Contains(body, []byte("<script>")) {
			t.Fatal("unescaped HTML")
		}
		for _, tc := range []struct {
			p string
			s int
		}{{"large.bin", 415}, {"invalid.txt", 415}} {
			check("GET", base+"/files/preview?path="+tc.p, nil, tc.s)
		}
		for _, text := range []string{strings.Repeat("x", int(store.BotFilePreviewMaxBytes)+1), strings.Repeat("x", int(store.BotFilePreviewMaxBytes)-1) + "€more", strings.Repeat("x", int(store.BotFilePreviewMaxBytes)-2) + "€more"} {
			files.mu.Lock()
			files.data["big.txt"] = []byte(text)
			files.mu.Unlock()
			body := check("GET", base+"/files/preview?path=big.txt", nil, 200)
			var preview struct {
				Content   string
				Truncated bool
			}
			if json.Unmarshal(body, &preview) != nil || !preview.Truncated || len(preview.Content) > int(store.BotFilePreviewMaxBytes) || !strings.HasPrefix(text, preview.Content) || len(preview.Content) < int(store.BotFilePreviewMaxBytes)-3 {
				t.Fatal("invalid bounded UTF8 preview")
			}
		}
	})
	t.Run("immutable old report", func(t *testing.T) {
		files.mu.Lock()
		files.data["report.md"] = []byte("new working content")
		files.mu.Unlock()
		body := check("GET", artifact+"/download", nil, 200)
		if !bytes.Equal(body, files.immutable) {
			t.Fatal("immutable snapshot changed")
		}
		check("GET", strings.Replace(artifact, "/reports/7/", "/reports/8/", 1)+"/download", nil, 404)
	})
	t.Run("scope and private paths", func(t *testing.T) {
		for _, p := range []string{"../secret", ".astonish/authority.json", "reports/.ssh/key", "/etc/passwd"} {
			check("GET", base+"/files/download?path="+url.QueryEscape(p), nil, 400)
		}
		check("GET", base+"/files", map[string]string{"X-No-Auth": "1"}, 401)
		check("GET", base+"/files", map[string]string{"X-User": uuid.NewString()}, 403)
		check("GET", artifact+"/download", map[string]string{"X-Team": "other"}, 403)
		check("GET", base+"/files?scope=team", nil, 503)
		repo.mu.Lock()
		repo.b.PermanentDelete = true
		repo.mu.Unlock()
		check("GET", artifact+"/download", nil, 410)
		repo.mu.Lock()
		repo.b.PermanentDelete = false
		repo.mu.Unlock()
	})
	t.Run("native ticket lifecycle", func(t *testing.T) {
		check("GET", artifact+"/download", map[string]string{"Sec-Fetch-Mode": "navigate"}, 403)
		res, body := request("POST", artifact+"/download-ticket", nil)
		if res.StatusCode != 200 {
			t.Fatalf("ticket %d %s", res.StatusCode, body)
		}
		var dto struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(body, &dto) != nil || dto.URL == "" || strings.Contains(dto.URL, "token") {
			t.Fatal("unsafe ticket DTO")
		}
		cookies := res.Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != artifact+"/download" || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatal("unsafe cookie")
		}
		headers := map[string]string{"Sec-Fetch-Mode": "navigate", "Cookie": cookies[0].Name + "=" + cookies[0].Value}
		check("GET", dto.URL, headers, 200)
		check("GET", base+"/files/download?path=large.bin&team=team", headers, 403)
		repo.mu.Lock()
		repo.denied = true
		repo.mu.Unlock()
		check("GET", dto.URL, headers, 403)
		repo.mu.Lock()
		repo.denied = false
		repo.mu.Unlock()
		check("POST", artifact+"/download-ticket", map[string]string{"Origin": "https://evil.invalid"}, 403)
	})
	t.Run("team member reads without owner impersonation", func(t *testing.T) {
		repo.mu.Lock()
		repo.b.Scope = store.BotScopeTeam
		repo.b.Home.Scope = store.BotScopeTeam
		repo.mu.Unlock()
		svc.Bots = repo
		check("GET", base+"/files?scope=team", map[string]string{"X-User": uuid.NewString()}, 200)
		check("GET", artifact+"/download?scope=team", map[string]string{"X-User": uuid.NewString()}, 200)
		repo.mu.Lock()
		repo.b.Scope = store.BotScopePersonal
		repo.b.Home.Scope = store.BotScopePersonal
		repo.mu.Unlock()
		svc.Bots = nil
	})
	if files.opens.Load() != files.closed.Load() {
		t.Fatalf("stream leak: %d opens %d closes", files.opens.Load(), files.closed.Load())
	}
	t.Log("Real isolated HTTP routes: metadata-only listing, >16MiB byte integrity, 206/416/HEAD, bounded source preview, immutable publication, scoped tickets; no model/compute methods installed")
}

func TestBotFileDeliveryHTTPTerminalFailure(t *testing.T) {
	f := &fileDeliveryFiles{fail: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream, _ := f.stream(r.Context(), "x.txt", bytes.Repeat([]byte("x"), 8192), store.BotFileRange{})
		defer botOwnFileStream(r.Context(), stream)()
		w.Header().Set("Content-Length", "8192")
		if err := botCopyDownload(w, stream, 8192); err != nil {
			panic(http.ErrAbortHandler)
		}
	}))
	defer server.Close()
	res, err := server.Client().Get(server.URL)
	if err == nil {
		body, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr == nil || len(body) >= 8192 {
			t.Fatalf("terminal error accepted: bytes=%d err=%v", len(body), readErr)
		}
	}
	if f.closed.Load() != 1 {
		t.Fatal("failed transfer leaked reader")
	}
}

type fileDeliveryBlockingReader struct {
	closed chan struct{}
	once   sync.Once
}

func (r *fileDeliveryBlockingReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, context.Canceled
}
func (r *fileDeliveryBlockingReader) Close() error { r.once.Do(func() { close(r.closed) }); return nil }
func TestBotFileDeliveryHTTPCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	reader := &fileDeliveryBlockingReader{closed: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream := &store.BotFileStream{ReadCloser: reader}
		defer close(finished)
		defer botOwnFileStream(r.Context(), stream)()
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		if err := botCopyDownload(w, stream, 5); err != nil {
			panic(http.ErrAbortHandler)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	res.Body.Close()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close/join reader")
	}
}
