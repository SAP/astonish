package launcher

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/SAP/astonish/web"
	"github.com/gorilla/mux"
)

// studioBackend is the minimal interface that StudioServer needs from the
// platform database backend. It is a subset of daemon.platformDB and is
// satisfied by both pgstore.PGStore and sqlitestore.SQLiteStore.
type studioBackend interface {
	store.PlatformBackend

	// NewToolVectorStore creates a ToolVectorStore for semantic tool discovery.
	NewToolVectorStore(ctx context.Context) (agent.ToolVectorStore, error)
}

func studioChatComponentsFromFactoryResult(result *ChatFactoryResult, sandboxEnabled bool) *api.StudioChatComponents {
	if result == nil {
		return nil
	}
	return &api.StudioChatComponents{
		ChatAgent:         result.ChatAgent,
		LLM:               result.LLM,
		SwappableLLM:      result.SwappableLLM,
		SessionService:    result.SessionService,
		ProviderName:      result.ProviderName,
		ModelName:         result.ModelName,
		Compactor:         result.Compactor,
		InternalToolCount: len(result.InternalTools),
		MemoryActive:      result.MemorySearchAvailable,
		SandboxEnabled:    sandboxEnabled,
		StartupNotices:    result.StartupNotices,
		ShutdownSandbox:   result.ShutdownSandbox,
		Cleanup:           result.Cleanup,
		SandboxPool:       result.SandboxPool,
		FilesystemSkills:  append([]skills.Skill(nil), result.FilesystemSkills...),
	}
}

// StudioServer wraps the HTTP server with lifecycle management.
type StudioServer struct {
	server       *http.Server
	listener     net.Listener
	port         int
	platformAuth *api.PlatformAuth               // non-nil in platform mode
	backend      studioBackend                   // non-nil in platform mode
	tenantMW     func(http.Handler) http.Handler // tenant resolution middleware
	services     *store.Services
}

// StudioOption configures optional StudioServer behavior.
type StudioOption func(*StudioServer)

// WithServices injects the store.Services dependency container into the Studio server.
// When set, every HTTP request will have Services available via store.FromRequest(r),
// enabling handlers to access stores through the context rather than package-level globals.
func WithServices(svc *store.Services) StudioOption {
	return func(s *StudioServer) { s.services = svc }
}

// WithPlatformAuth enables JWT-based authentication for platform (multi-tenant) mode.
// When set, the platform auth middleware replaces the device auth middleware,
// and platform-specific routes (register, login, teams) are registered.
// The backend parameter provides backend access for ToolVectorStore and other methods.
func WithPlatformAuth(pa *api.PlatformAuth, backend studioBackend) StudioOption {
	return func(s *StudioServer) {
		s.platformAuth = pa
		if backend != nil {
			s.backend = backend
		}
	}
}

// WithBackend sets the studioBackend for the Studio server.
// Used in SQLite mode where pgStore is nil but we still need backend access
// for ToolVectorStore creation and other interface methods.
func WithBackend(b studioBackend) StudioOption {
	return func(s *StudioServer) {
		s.backend = b
	}
}

// WithTenantMiddleware sets a custom tenant resolution middleware.
// Used when the backend is not pgstore (e.g., sqlitestore).
func WithTenantMiddleware(mw func(http.Handler) http.Handler) StudioOption {
	return func(s *StudioServer) {
		s.tenantMW = mw
	}
}

// NewStudioServer creates a configured Studio server without starting it.
func NewStudioServer(port int, opts ...StudioOption) (*StudioServer, error) {
	s := &StudioServer{port: port}
	for _, opt := range opts {
		opt(s)
	}

	// Wire Studio Chat initialization (lazy, runs on first chat request)
	isPlatform := s.platformAuth != nil // capture for closure
	backendRef := s.backend             // capture for closure
	api.SetStudioChatInitFunc(func(ctx context.Context) (*api.StudioChatComponents, error) {
		appCfg := api.EffectiveAppConfigFromContext(ctx, isPlatform)

		// A daemon pre-warm has no authenticated user/session from which to
		// resolve a personal model pin. Use the effective platform defaults when
		// they exist, but keep reusable agent infrastructure warm when this
		// installation has no deployable unaffiliated default. Request-time
		// resolution remains authoritative and swaps the caller's effective LLM
		// before a turn runs.
		providerName := appCfg.General.DefaultProvider
		modelName := appCfg.General.DefaultModel
		deferLLMBinding := providerName == "" || modelName == ""
		if deferLLMBinding {
			slog.Info("studio pre-warm deferring LLM binding: no deployable default", "component", "studio-chat")
		}
		factoryCfg := &ChatFactoryConfig{
			AppConfig:            appCfg,
			ProviderName:         providerName,
			ModelName:            modelName,
			AllowMissingProvider: deferLLMBinding,
			DebugMode:            false,
			AutoApprove:          false,
			WorkspaceDir:         "",
			IsDaemon:             false,
			PlatformMode:         isPlatform,
		}

		// In platform mode, create ToolIndex for dynamic tool discovery.
		if isPlatform && backendRef != nil {
			if embedFunc := backendRef.GetEmbedFunc(); embedFunc != nil {
				vs, vsErr := backendRef.NewToolVectorStore(ctx)
				if vsErr == nil && vs != nil {
					factoryCfg.PlatformToolVectorStore = vs
					factoryCfg.PlatformEmbedFunc = agent.EmbedFunc(embedFunc)
				}
			}
		}

		result, err := NewWiredChatAgent(ctx, factoryCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize chat agent: %w", err)
		}

		// Wire scheduler access via daemon HTTP API (personal mode only).
		// In platform mode, the RunJobFunc injected into the chat runner context
		// handles test execution directly — the HTTP bridge cannot authenticate
		// against the PlatformAuthMiddleware and would produce a 401.
		if !isPlatform {
			daemonPort := appCfg.Daemon.GetPort()
			tools.SetSchedulerAccess(&tools.SchedulerHTTPAccess{
				BaseURL: fmt.Sprintf("http://localhost:%d", daemonPort),
			})
		}

		return studioChatComponentsFromFactoryResult(result, sandbox.IsSandboxEnabled(&appCfg.Sandbox)), nil
	})

	// Wire a pre-warm context builder for auto-PreWarm on Reset().
	// In non-daemon (personal) mode, a simple background context suffices.
	// The daemon overrides this with a richer context that includes platform stores.
	if s.platformAuth == nil {
		api.SetPreWarmContextFunc(func() context.Context {
			return context.Background()
		})
	}

	router := mux.NewRouter()

	// Register auth endpoints first (they are always accessible)
	if s.platformAuth != nil {
		// Platform mode: JWT-based auth with register/login/refresh
		api.RegisterPlatformAuthRoutes(router, s.platformAuth)
		api.RegisterTeamRoutes(router, s.platformAuth)
		api.RegisterUserRoutes(router, s.platformAuth)

		// SSO/OIDC endpoints (device flow for CLI, browser redirect for Studio)
		// When a backend is available, use DB-backed device sessions for horizontal scaling.
		var ssoHandler *api.SSOHandler
		if s.backend != nil {
			if dsb := api.DeviceSessionBackendFromStore(s.backend); dsb != nil {
				ssoHandler = api.NewSSOHandlerWithPG(s.platformAuth, dsb)
			}
		}
		if ssoHandler == nil {
			ssoHandler = api.NewSSOHandler(s.platformAuth)
		}
		api.SetPlatformSSOHandler(ssoHandler)
		api.RegisterSSORoutes(router, ssoHandler)
	}

	// Register API routes (passes tenantMW for platform-mode TenantMiddleware)
	api.RegisterRoutes(router, s.services, s.backend, s.tenantMW)

	// Try to get web assets (embedded or filesystem)
	webFS := getWebAssets()

	var handler http.Handler

	if webFS != nil {
		// Wrap router + SPA into a single handler
		spaHandler := spaFileServer(http.FS(webFS))
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Let mux handle /api/* and /.well-known/* routes
			if (len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api") ||
				strings.HasPrefix(r.URL.Path, "/.well-known/") {
				router.ServeHTTP(w, r)
				return
			}
			// Everything else is SPA
			spaHandler.ServeHTTP(w, r)
		})
	} else {
		slog.Warn("no web assets found, run 'npm run build' in the web directory first")
		fallback := noAssetsHandler()
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api") ||
				strings.HasPrefix(r.URL.Path, "/.well-known/") {
				router.ServeHTTP(w, r)
				return
			}
			fallback.ServeHTTP(w, r)
		})
	}

	// Apply auth middleware if enabled (wraps both API and SPA)
	if s.platformAuth != nil {
		// Platform mode: JWT auth (TenantMiddleware is inside the router via RegisterRoutes)
		handler = api.PlatformAuthMiddleware(s.platformAuth, handler)
	}

	// Apply rate limiting for remote (non-loopback) requests.
	// Auth endpoints get stricter limits (brute-force protection);
	// general API endpoints get a generous budget.
	handler = api.RateLimitMiddleware(api.NewDefaultRateLimitConfig(), handler)

	// Apply security headers (CSP, X-Frame-Options, etc.) for Studio responses.
	// Placed inside auth/rate-limit but outside the subdomain proxy so that
	// proxied container apps are not affected.
	handler = api.CSPMiddleware(handler)

	// Wrap with subdomain proxy check OUTSIDE auth — subdomain-proxied
	// requests serve the container's app, not Studio, so they bypass
	// Studio authentication. The auth cookie is scoped to the Studio
	// domain and won't be sent on subdomain requests anyway.
	studioHandler := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if containerName, port, ok := api.GetSubdomainRouter().Lookup(r.Host); ok {
			api.ServeSubdomainProxy(w, r, containerName, port)
			return
		}
		studioHandler.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	s.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  0, // SSE streaming needs no read timeout
		WriteTimeout: 0, // SSE streaming needs no write timeout
		IdleTimeout:  120 * time.Second,
	}
	s.listener = listener

	return s, nil
}

// Port returns the port the server is listening on.
// When port 0 was requested, this returns the actual port assigned by the OS.
func (s *StudioServer) Port() int {
	if s.port == 0 && s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.port
}

// Serve starts serving HTTP requests. Blocks until the server is shut down.
// Returns http.ErrServerClosed on graceful shutdown.
func (s *StudioServer) Serve() error {
	return s.server.Serve(s.listener)
}

// Shutdown gracefully shuts down the server with a timeout.
func (s *StudioServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// getWebAssets returns the web assets filesystem
// Priority: 1. Filesystem (for dev), 2. Embedded (for production)
func getWebAssets() fs.FS {
	// First, try filesystem (for development)
	if dir := findWebDir(); dir != "" {
		slog.Info("serving web assets from filesystem", "dir", dir)
		return os.DirFS(dir)
	}

	// Fall back to embedded assets (for production binary)
	if embeddedFS := web.GetDistFS(); embeddedFS != nil {
		slog.Info("serving web assets from embedded binary")
		return embeddedFS
	}

	return nil
}

// findWebDir looks for the web/dist directory on filesystem
func findWebDir() string {
	// Check relative to current directory
	paths := []string{
		"web/dist",
		"../web/dist",
		"../../web/dist",
	}

	// Also check relative to executable
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		paths = append(paths,
			filepath.Join(exeDir, "web/dist"),
			filepath.Join(exeDir, "../web/dist"),
		)
	}

	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			// Check if index.html exists
			if _, err := os.Stat(filepath.Join(path, "index.html")); err == nil {
				absPath, _ := filepath.Abs(path)
				return absPath
			}
		}
	}

	return ""
}

// noAssetsHandler returns a handler for when web assets are not found.
func noAssetsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Astonish Studio</title></head>
<body style="font-family: sans-serif; padding: 40px; background: #0d121f; color: white;">
<h1>Astonish Studio</h1>
<p>Web assets not found. Please build the frontend first:</p>
<pre style="background: #1a1a2e; padding: 20px; border-radius: 8px;">
cd web
npm install
npm run build
</pre>
<p>Then restart the studio server.</p>
</body>
</html>`)
			return
		}
		http.NotFound(w, r)
	})
}

// spaFileServer returns a handler that serves SPA files with fallback to index.html
func spaFileServer(fs http.FileSystem) http.Handler {
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip API routes - let them 404 naturally so mux routes work
		if len(path) >= 4 && path[:4] == "/api" {
			http.NotFound(w, r)
			return
		}

		// Check if file exists
		f, err := fs.Open(path)
		if err != nil {
			// File doesn't exist, serve index.html for SPA routing
			r.URL.Path = "/"
		} else {
			f.Close()
		}

		// Content-hashed assets (Vite build) are immutable — cache them
		// aggressively so browsers don't re-fetch on every page navigation.
		// This also eliminates concurrent connection storms on reverse proxies.
		if strings.HasPrefix(path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

		fileServer.ServeHTTP(w, r)
	})
}
