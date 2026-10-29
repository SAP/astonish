package api

// Opt-in behavioral acceptance (no daemon or LLM required):
// ASTONISH_FILE_TOOL_API_LIVE=1 go test ./pkg/api -run '^TestFileToolAPILiveDeliveryRestore$' -count=1 -v -timeout=15m
// Uses an already-local ASTONISH_FILE_TOOL_LIVE_IMAGE, defaulting to the sandbox
// base. The current checkout node is rebuilt. Docker exec transport and the
// identity-to-store binding are test-owned; HTTP handlers, cookie authentication,
// load_file, and disk session restoration are production code. This does NOT
// exercise production OverlayFS provisioning, platform login, SSE, or a browser.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	adksession "google.golang.org/adk/session"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
)

// Adapt only the read operations used by these handlers; all data comes from
// the real disk store, never a hand-authored successful load_file response.
type fileAPILiveStore struct {
	store.SessionStore
	fs *persistentsession.FileStore
}

func (s fileAPILiveStore) Get(ctx context.Context, r *adksession.GetRequest) (*adksession.GetResponse, error) {
	return s.fs.Get(ctx, r)
}
func (s fileAPILiveStore) GetSessionMeta(_ context.Context, id string) (*store.SessionMeta, error) {
	m, err := s.fs.GetSessionMeta(id)
	if err != nil || m == nil {
		return nil, err
	}
	return &store.SessionMeta{ID: m.ID, AppName: m.AppName, UserID: m.UserID}, nil
}
func (s fileAPILiveStore) ReadTranscriptEvents(_ context.Context, app, user, id string) ([]*adksession.Event, error) {
	return s.fs.ReadTranscriptEvents(app, user, id)
}

func TestFileToolAPILiveDeliveryRestore(t *testing.T) {
	if os.Getenv("ASTONISH_FILE_TOOL_API_LIVE") != "1" {
		t.Skip("opt in with ASTONISH_FILE_TOOL_API_LIVE=1; requires Docker and Go")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	image := os.Getenv("ASTONISH_FILE_TOOL_LIVE_IMAGE")
	if image == "" {
		image = "ghcr.io/sap/astonish-sandbox-base:latest"
	}
	arch := strings.TrimSpace(command("docker", "image", "inspect", "--format", "{{.Architecture}}", image))
	if arch != "amd64" && arch != "arm64" {
		t.Fatalf("unsupported image architecture %q", arch)
	}
	binary := filepath.Join(t.TempDir(), "astonish")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	t.Logf("building current Linux/%s node", arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	id := uuid.NewString()
	name := "astonish-api-file-live-" + id
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		out, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup %s: %v: %s", name, err, out)
		} else {
			t.Logf("removed disposable container %s", name)
		}
	})
	command("docker", "run", "--pull=never", "-d", "--name", name,
		"--label", "astonish.acceptance=api-file-live", "--network", "none",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev", "--tmpfs", "/root:rw,nosuid,nodev",
		"--tmpfs", "/live-bin:rw,exec,nosuid,nodev", "--workdir", "/tmp",
		"--entrypoint", "/bin/sh", image, "-c", "exec sleep 900")
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	copyCmd := exec.CommandContext(ctx, "docker", "exec", "-i", name, "/bin/sh", "-c", "cat > /live-bin/astonish && chmod 0555 /live-bin/astonish")
	copyCmd.Stdin = f
	out, copyErr := copyCmd.CombinedOutput()
	f.Close()
	if copyErr != nil {
		t.Fatalf("stream binary to tmpfs: %v: %s", copyErr, out)
	}
	path := "/tmp/astonish-api-file-live-" + id + ".txt"
	const payload = "real sandbox HTTP publication\nrestored download exact bytes\n"
	assertHostAbsent := func() {
		t.Helper()
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("container fixture unexpectedly exists on host: %s: %v", path, err)
		}
	}
	assertHostAbsent()
	t.Cleanup(assertHostAbsent)
	command("docker", "exec", name, "/bin/sh", "-c", `printf '%s' "$1" > "$2"`, "fixture", payload, path)
	args := map[string]any{"path": path, "title": "Live HTTP artifact"}
	request, err := json.Marshal(map[string]any{"id": "load", "tool": "load_file", "args": args})
	if err != nil {
		t.Fatal(err)
	}
	node := exec.CommandContext(ctx, "docker", "exec", "-i", name, "/live-bin/astonish", "node")
	node.Stdin = bytes.NewReader(append(request, '\n'))
	var stderr bytes.Buffer
	node.Stderr = &stderr
	out, err = node.Output()
	if err != nil {
		t.Fatalf("node: %v: %s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := decoder.Decode(&ready); err != nil || !ready.Ready {
		t.Fatalf("node not ready: %s", out)
	}
	var response struct {
		ID     string         `json:"id"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil || response.ID != "load" || response.Error != "" {
		t.Fatalf("node response: %s", out)
	}
	if response.Result["path"] != path || response.Result["size_bytes"] != float64(len(payload)) || response.Result["content"] != nil || bytes.Contains(out, []byte(payload)) {
		t.Fatalf("load_file not metadata-only: %s", out)
	}

	// Isolate config only after Docker and Go have used the real client settings.
	// Docker commands retain DOCKER_CONFIG to work with remote contexts.
	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dockerConfig = filepath.Join(home, ".docker")
	}
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	dir := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(key, dir)
	}
	t.Setenv("SUDO_USER", "")
	enabled := true
	if err := config.SaveAppConfig(&config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled, Backend: "mock"}}); err != nil {
		t.Fatal(err)
	}
	fsDir := t.TempDir()
	fs, err := persistentsession.NewFileStore(fsDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := fs.Create(ctx, &adksession.CreateRequest{AppName: studioChatAppName, UserID: "owner", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []*adksession.Event{
		artifactToolCallEvent("inv", "load", "load_file", args),
		toolResponseEvent("inv", "load", "load_file", response.Result),
	} {
		if err := fs.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	backend := &artifactConfinedBackend{MockBackend: mock.New()}
	if _, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatal(err)
	}
	backend.PullFileFn = func(sessionID, filePath string) (io.ReadCloser, error) {
		if sessionID != id || filePath != path {
			return nil, fmt.Errorf("outside test-owned fixture")
		}
		out, err := exec.CommandContext(ctx, "docker", "exec", name, "cat", filePath).Output()
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(out)), nil
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
	auth, err := NewAuthStore(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.CreateSession("live-test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	startServer := func(disk *persistentsession.FileStore) *httptest.Server {
		router := mux.NewRouter()
		router.HandleFunc("/api/studio/artifacts", StudioArtifactDownloadHandler)
		router.HandleFunc("/api/studio/artifacts/content", StudioArtifactContentHandler)
		router.HandleFunc("/api/studio/sessions/{id}", StudioSessionHandler)
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(WithPlatformUser(store.WithServices(r.Context(), &store.Services{PersonalSessions: fileAPILiveStore{fs: disk}}), &PlatformUser{ID: "owner"}))
			router.ServeHTTP(w, withSandboxRegistry(r, registry))
		})
		authenticated := AuthMiddleware(NewAuthManager(auth), inner)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Exercise the remote-cookie branch over a safe loopback listener.
			// AuthMiddleware deliberately bypasses auth for real loopback peers.
			r.RemoteAddr = "192.0.2.1:12345"
			authenticated.ServeHTTP(w, r)
		}))
	}
	get := func(server *httptest.Server, route string, authenticated bool, wantStatus int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+route, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != wantStatus {
			t.Fatalf("GET %s: status=%d body=%s", route, resp.StatusCode, body)
		}
		return body, resp.Header
	}
	for _, restored := range []bool{false, true} {
		if restored {
			fs, err = persistentsession.NewFileStore(fsDir)
			if err != nil {
				t.Fatal(err)
			}
		}
		server := startServer(fs)
		t.Cleanup(server.Close)
		for _, route := range []string{"/api/studio/artifacts", "/api/studio/artifacts/content"} {
			query := "?path=" + url.QueryEscape(path) + "&session=" + id
			before := len(backend.PullFileCalls())
			get(server, route+query, false, http.StatusUnauthorized)
			get(server, route+"?path="+url.QueryEscape(path)+"&session=foreign", true, http.StatusNotFound)
			if len(backend.PullFileCalls()) != before {
				t.Fatal("rejected request reached sandbox")
			}
			body, headers := get(server, route+query, true, http.StatusOK)
			if string(body) != payload {
				t.Fatalf("wrong sandbox bytes: %q", body)
			}
			if route == "/api/studio/artifacts" && (!strings.HasPrefix(headers.Get("Content-Disposition"), "attachment;") || headers.Get("X-Content-Type-Options") != "nosniff") {
				t.Fatal("missing download safety headers")
			}
		}
		body, _ := get(server, "/api/studio/sessions/"+id, true, http.StatusOK)
		var detail StudioSessionDetailResponse
		if err := json.Unmarshal(body, &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail.Artifacts) != 1 || detail.Artifacts[0].Path != path || detail.Artifacts[0].ToolName != "load_file" || bytes.Contains(body, []byte("real sandbox HTTP publication")) {
			t.Fatalf("restored metadata: %s", body)
		}
		server.Close()
		t.Logf("PASS restored=%v: auth rejection, foreign-session rejection, exact container content/download bytes, one metadata-only load_file artifact", restored)
	}
	if len(backend.PullFileCalls()) != 4 {
		t.Fatalf("expected four real container pulls, got %d", len(backend.PullFileCalls()))
	}
	assertHostAbsent()
}
