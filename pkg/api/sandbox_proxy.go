package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
)

// ipCacheEntry holds a cached container IP with an expiry time.
type ipCacheEntry struct {
	ip     string
	expiry time.Time
}

// ipCache maps container names to their bridge IPs with a TTL.
var ipCache sync.Map

const ipCacheTTL = 30 * time.Second

// getCachedIP returns the cached IP for a container, or resolves and caches it.
func getCachedIP(client *incus.IncusClient, containerName string) (string, error) {
	if entry, ok := ipCache.Load(containerName); ok {
		cached := entry.(*ipCacheEntry)
		if time.Now().Before(cached.expiry) {
			return cached.ip, nil
		}
		ipCache.Delete(containerName)
	}

	// Use a single-attempt IP resolution for the proxy path.
	// GetContainerIPv4 polls for up to 10s, but if the container is running
	// and healthy, the IP should be available immediately.
	ip, err := client.GetContainerIPv4(containerName)
	if err != nil {
		return "", err
	}

	ipCache.Store(containerName, &ipCacheEntry{
		ip:     ip,
		expiry: time.Now().Add(ipCacheTTL),
	})

	return ip, nil
}

// InvalidateIPCache removes a container's IP from the cache.
// Called when a container is destroyed.
func InvalidateIPCache(containerName string) {
	ipCache.Delete(containerName)
}

// SandboxProxyHandler handles /api/sandbox/proxy/{container}/{port}/{path...}
// It reverse-proxies HTTP (and WebSocket) requests to a service running inside
// a sandbox container. The port must be explicitly exposed via the session
// registry before the proxy will forward traffic.
//
// NOTE: This path-based proxy works well for API-only services but breaks SPAs
// that use absolute asset paths (e.g., /assets/main.js). For SPAs, use the
// per-port proxy managed by PortProxyManager instead.
func SandboxProxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	containerName := vars["container"]
	portStr := vars["port"]

	if containerName == "" || portStr == "" {
		respondError(w, http.StatusBadRequest, `{"error":"missing container or port"}`)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		respondError(w, http.StatusBadRequest, `{"error":"invalid port number"}`)
		return
	}

	// Check that the port is explicitly exposed.
	// SECURITY: use the tenant-scoped registry so a caller can only proxy
	// into containers owned by their own team. sandbox.NewSessionRegistry()
	// would return the unscoped personal-mode registry and allow cross-tenant
	// proxying by container name on a platform deployment.
	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, `{"error":"failed to load session registry"}`)
		return
	}

	if !sessRegistry.IsPortExposed(containerName, port) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":"port %d is not exposed on container %q. Use 'astonish sandbox expose %s %d' first."}`,
			port, containerName, containerName, port)
		return
	}

	// Verify container exists and is running
	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, `{"error":"sandbox unavailable"}`)
		return
	}

	if !client.IsRunning(containerName) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"container %q is not running"}`, containerName)
		return
	}

	// Resolve container IP (used for logging/diagnostics, not for dialing)
	if _, err := getCachedIP(client, containerName); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"cannot resolve container IP: %s"}`, err.Error())
		return
	}

	// Extract the downstream path (everything after /api/sandbox/proxy/{container}/{port})
	prefix := fmt.Sprintf("/api/sandbox/proxy/%s/%s", containerName, portStr)
	downstreamPath := strings.TrimPrefix(r.URL.Path, prefix)
	if downstreamPath == "" {
		downstreamPath = "/"
	}

	// WebSocket upgrade
	if isWebSocketUpgrade(r) {
		proxyWebSocket(w, r, func() (net.Conn, error) {
			dialer := &incus.ContainerDialer{Client: client}
			return dialer.Dial(containerName, port)
		}, downstreamPath)
		return
	}

	// HTTP reverse proxy
	dialer := &incus.ContainerDialer{Client: client}
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("invalid proxy target URL: %s", err))
		return
	}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = downstreamPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = target.Host

			// Forward headers
			if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				if existing := req.Header.Get("X-Forwarded-For"); existing != "" {
					req.Header.Set("X-Forwarded-For", existing+", "+clientIP)
				} else {
					req.Header.Set("X-Forwarded-For", clientIP)
				}
			}
			req.Header.Set("X-Forwarded-Host", r.Host)
			req.Header.Set("X-Forwarded-Proto", "http")
		},
		Transport: dialer.HTTPTransport(containerName, port),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"proxy error: %s"}`, err.Error())
		},
	}

	proxy.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Per-port proxy manager
// ---------------------------------------------------------------------------
// Instead of proxying through a path prefix (which breaks SPAs), we allocate
// a dedicated host port for each exposed container port. The browser connects
// directly to http://{host}:{hostPort}/ and all requests are reverse-proxied
// to http://{containerIP}:{containerPort}/. Since there is no path prefix,
// absolute asset paths like /assets/main.js resolve correctly.

const (
	// portRangeStart is the first host port we try to allocate.
	portRangeStart = 19000
	// portRangeEnd is one past the last host port we try.
	portRangeEnd = 19200
)

// portProxyEntry tracks one per-port listener.
type portProxyEntry struct {
	containerName string
	containerPort int
	hostPort      int
	server        *http.Server
	cancel        context.CancelFunc
}

// PortProxyManager manages per-port reverse proxy listeners.
type PortProxyManager struct {
	mu      sync.Mutex
	entries map[string]*portProxyEntry // key: "containerName:containerPort"
	used    map[int]bool               // host ports currently in use
}

// portProxyMgr is the singleton manager. Initialized lazily.
var (
	portProxyMgr     *PortProxyManager
	portProxyMgrOnce sync.Once
)

// GetPortProxyManager returns the singleton PortProxyManager.
func GetPortProxyManager() *PortProxyManager {
	portProxyMgrOnce.Do(func() {
		portProxyMgr = &PortProxyManager{
			entries: make(map[string]*portProxyEntry),
			used:    make(map[int]bool),
		}
	})
	return portProxyMgr
}

// proxyKey builds the map key for a container+port combo.
func proxyKey(containerName string, containerPort int) string {
	return fmt.Sprintf("%s:%d", containerName, containerPort)
}

// allocatePort finds the next free host port in the range.
func (m *PortProxyManager) allocatePort() (int, error) {
	for p := portRangeStart; p < portRangeEnd; p++ {
		if m.used[p] {
			continue
		}
		// Try to bind to verify it's actually free
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // port in use by something else
		}
		ln.Close()
		return p, nil
	}
	return 0, fmt.Errorf("no free host ports in range %d-%d", portRangeStart, portRangeEnd-1)
}

// StartProxy starts a per-port reverse proxy listener for a container port.
// Returns the allocated host port. If already running, returns the existing port.
func (m *PortProxyManager) StartProxy(containerName string, containerPort int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := proxyKey(containerName, containerPort)

	// Already running?
	if entry, ok := m.entries[key]; ok {
		return entry.hostPort, nil
	}

	// Allocate a host port
	hostPort, err := m.allocatePort()
	if err != nil {
		return 0, err
	}

	// Verify container connectivity
	client, err := sandboxConnect()
	if err != nil {
		return 0, fmt.Errorf("sandbox unavailable: %w", err)
	}
	if _, err := getCachedIP(client, containerName); err != nil {
		return 0, fmt.Errorf("cannot resolve container IP: %w", err)
	}

	dialer := &incus.ContainerDialer{Client: client}
	tunnelTarget := fmt.Sprintf("http://127.0.0.1:%d", containerPort)

	// Build the handler: reverse proxy + WebSocket support
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			proxyWebSocket(w, r, func() (net.Conn, error) {
				return dialer.Dial(containerName, containerPort)
			}, r.URL.Path)
			return
		}

		currentTarget, err := url.Parse(tunnelTarget)
		if err != nil {
			respondError(w, http.StatusBadGateway, fmt.Sprintf("invalid proxy target URL: %s", err))
			return
		}
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = currentTarget.Scheme
				req.URL.Host = currentTarget.Host
				// Pass path and query through unchanged — no prefix stripping needed
				req.URL.Path = r.URL.Path
				req.URL.RawQuery = r.URL.RawQuery
				req.Host = currentTarget.Host

				if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
					if existing := req.Header.Get("X-Forwarded-For"); existing != "" {
						req.Header.Set("X-Forwarded-For", existing+", "+clientIP)
					} else {
						req.Header.Set("X-Forwarded-For", clientIP)
					}
				}
				req.Header.Set("X-Forwarded-Host", r.Host)
				req.Header.Set("X-Forwarded-Proto", "http")
			},
			Transport: dialer.HTTPTransport(containerName, containerPort),
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				respondError(w, http.StatusBadGateway, fmt.Sprintf("proxy error: %s", err))
			},
		}
		proxy.ServeHTTP(w, r)
	})

	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", hostPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	slog.Info("starting listener", "component", "sandbox-proxy", "host_port", hostPort, "container_port", containerPort, "container", containerName)

	// Start listener in background
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listener error", "component", "sandbox-proxy", "host_port", hostPort, "error", err)
		}
	}()

	// Shutdown goroutine
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	entry := &portProxyEntry{
		containerName: containerName,
		containerPort: containerPort,
		hostPort:      hostPort,
		server:        srv,
		cancel:        cancel,
	}

	m.entries[key] = entry
	m.used[hostPort] = true

	return hostPort, nil
}

// StopProxy stops the per-port proxy listener for a container port.
// Returns true if a listener was stopped.
func (m *PortProxyManager) StopProxy(containerName string, containerPort int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := proxyKey(containerName, containerPort)
	entry, ok := m.entries[key]
	if !ok {
		return false
	}

	entry.cancel()
	delete(m.entries, key)
	delete(m.used, entry.hostPort)

	slog.Info("stopped listener", "component", "sandbox-proxy", "host_port", entry.hostPort, "container", containerName, "container_port", containerPort)
	return true
}

// StopAllForContainer stops all per-port proxy listeners for a container.
func (m *PortProxyManager) StopAllForContainer(containerName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var toDelete []string
	for key, entry := range m.entries {
		if entry.containerName == containerName {
			entry.cancel()
			delete(m.used, entry.hostPort)
			toDelete = append(toDelete, key)
			slog.Info("stopped listener", "component", "sandbox-proxy", "host_port", entry.hostPort, "container", containerName, "container_port", entry.containerPort)
		}
	}

	for _, key := range toDelete {
		delete(m.entries, key)
	}

	return len(toDelete)
}

// GetHostPort returns the allocated host port for a container port, or 0 if not running.
func (m *PortProxyManager) GetHostPort(containerName string, containerPort int) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := proxyKey(containerName, containerPort)
	if entry, ok := m.entries[key]; ok {
		return entry.hostPort
	}
	return 0
}

// ListForContainer returns a map of containerPort → hostPort for all active
// proxies for the given container.
func (m *PortProxyManager) ListForContainer(containerName string) map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make(map[int]int)
	for _, entry := range m.entries {
		if entry.containerName == containerName {
			result[entry.containerPort] = entry.hostPort
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Subdomain-based proxy router
// ---------------------------------------------------------------------------
// Maps full hostnames (e.g., "astn-sess-abc-3000.example.com") to container
// targets. When Studio receives a request whose Host header matches a
// registered hostname, it reverse-proxies the request to the container
// instead of serving the Studio UI or API.

// subdomainTarget describes where to proxy a matched subdomain request.
type subdomainTarget struct {
	containerName string
	containerPort int
}

// SubdomainRouter maps hostnames to container proxy targets.
type SubdomainRouter struct {
	mu      sync.RWMutex
	hostMap map[string]*subdomainTarget // hostname → target

	clientMu   sync.Mutex
	client     *incus.IncusClient
	clientInit bool
}

var (
	subdomainRouter     *SubdomainRouter
	subdomainRouterOnce sync.Once
)

// GetSubdomainRouter returns the singleton SubdomainRouter.
func GetSubdomainRouter() *SubdomainRouter {
	subdomainRouterOnce.Do(func() {
		subdomainRouter = &SubdomainRouter{
			hostMap: make(map[string]*subdomainTarget),
		}
	})
	return subdomainRouter
}

// SubdomainHostname constructs the subdomain hostname for a container+port.
// Format: {containerName}-{port}.{baseDomain}
func SubdomainHostname(containerName string, port int, baseDomain string) string {
	return fmt.Sprintf("%s-%d.%s", containerName, port, baseDomain)
}

// RegisterHost adds a hostname → container mapping.
func (sr *SubdomainRouter) RegisterHost(hostname, containerName string, port int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.hostMap[hostname] = &subdomainTarget{
		containerName: containerName,
		containerPort: port,
	}
	slog.Info("registered subdomain route", "component", "sandbox-proxy", "hostname", hostname, "container", containerName, "port", port)
}

// UnregisterHost removes a hostname mapping.
func (sr *SubdomainRouter) UnregisterHost(hostname string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if _, ok := sr.hostMap[hostname]; ok {
		delete(sr.hostMap, hostname)
		slog.Info("unregistered subdomain route", "component", "sandbox-proxy", "hostname", hostname)
	}
}

// UnregisterAllForContainer removes all hostname mappings for a container.
func (sr *SubdomainRouter) UnregisterAllForContainer(containerName string) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	var toDelete []string
	for host, target := range sr.hostMap {
		if target.containerName == containerName {
			toDelete = append(toDelete, host)
		}
	}
	for _, host := range toDelete {
		delete(sr.hostMap, host)
		slog.Info("unregistered subdomain route", "component", "sandbox-proxy", "hostname", host)
	}
	return len(toDelete)
}

// Lookup checks if a host matches a registered subdomain proxy.
// The host may include a port (e.g., "foo.example.com:9393"), which is stripped.
func (sr *SubdomainRouter) Lookup(host string) (containerName string, port int, ok bool) {
	// Strip port from host if present
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	sr.mu.RLock()
	defer sr.mu.RUnlock()
	target, found := sr.hostMap[hostname]
	if !found {
		return "", 0, false
	}
	return target.containerName, target.containerPort, true
}

// ListForContainer returns a map of containerPort → hostname for all active
// subdomain routes for the given container.
func (sr *SubdomainRouter) ListForContainer(containerName string) map[int]string {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	result := make(map[int]string)
	for host, target := range sr.hostMap {
		if target.containerName == containerName {
			result[target.containerPort] = host
		}
	}
	return result
}

// getClient returns a cached Incus client, creating one on first call.
// The client is reused across all subdomain proxy requests to avoid
// the overhead of sandboxConnect() (platform detection + Incus dial)
// on every request.
func (sr *SubdomainRouter) getClient() (*incus.IncusClient, error) {
	sr.clientMu.Lock()
	defer sr.clientMu.Unlock()

	if sr.clientInit && sr.client != nil {
		return sr.client, nil
	}

	client, err := sandboxConnect()
	if err != nil {
		return nil, err
	}
	sr.client = client
	sr.clientInit = true
	return client, nil
}

// ServeSubdomainProxy handles an HTTP request by proxying it to the matched
// container. Called from the Studio main handler when a subdomain match is found.
func ServeSubdomainProxy(w http.ResponseWriter, r *http.Request, containerName string, containerPort int) {
	sr := GetSubdomainRouter()
	client, err := sr.getClient()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Sprintf("sandbox unavailable: %s", err))
		return
	}

	// Verify container is reachable
	if _, err := getCachedIP(client, containerName); err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("cannot resolve container IP: %s", err))
		return
	}

	dialer := &incus.ContainerDialer{Client: client}

	if isWebSocketUpgrade(r) {
		proxyWebSocket(w, r, func() (net.Conn, error) {
			return dialer.Dial(containerName, containerPort)
		}, r.URL.Path)
		return
	}

	currentTarget, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", containerPort))
	if err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("invalid proxy target URL: %s", err))
		return
	}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = currentTarget.Scheme
			req.URL.Host = currentTarget.Host
			req.URL.Path = r.URL.Path
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = currentTarget.Host

			if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				if existing := req.Header.Get("X-Forwarded-For"); existing != "" {
					req.Header.Set("X-Forwarded-For", existing+", "+clientIP)
				} else {
					req.Header.Set("X-Forwarded-For", clientIP)
				}
			}
			req.Header.Set("X-Forwarded-Host", r.Host)
			req.Header.Set("X-Forwarded-Proto", "http")
		},
		Transport: dialer.HTTPTransport(containerName, containerPort),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			respondError(w, http.StatusBadGateway, fmt.Sprintf("proxy error: %s", err))
		},
	}
	proxy.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// WebSocket and utility functions
// ---------------------------------------------------------------------------

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request.
func isWebSocketUpgrade(r *http.Request) bool {
	for _, v := range r.Header["Connection"] {
		for _, s := range strings.Split(v, ",") {
			if strings.TrimSpace(strings.ToLower(s)) == "upgrade" {
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					return true
				}
			}
		}
	}
	return false
}

// proxyWebSocket forwards a WebSocket connection to the container using raw
// TCP hijacking. This avoids message-level copying and works transparently
// with any WebSocket application (including Vite HMR, Socket.IO, etc.).
//
// The dialFunc creates the backend TCP connection (typically via exec tunnel).
//
// The approach:
//  1. Dial a connection to the backend container via dialFunc
//  2. Write the original HTTP upgrade request to the backend
//  3. Read the backend's 101 Switching Protocols response
//  4. Hijack the client connection from the HTTP server
//  5. Forward the 101 response to the client
//  6. Bidirectionally pipe raw bytes between the two connections
func proxyWebSocket(w http.ResponseWriter, r *http.Request, dialFunc func() (net.Conn, error), path string) {
	// Dial the backend via the provided dial function (exec tunnel)
	backendConn, err := dialFunc()
	if err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("WebSocket backend dial failed: %s", err))
		return
	}

	// Build the request URI
	reqURI := path
	if reqURI == "" {
		reqURI = "/"
	}
	if r.URL.RawQuery != "" {
		reqURI += "?" + r.URL.RawQuery
	}

	// Write the HTTP upgrade request to the backend.
	// Set Host to the remote address so the upstream server (e.g. Vite)
	// doesn't reject based on host mismatch.
	backendHost := backendConn.RemoteAddr().String()
	var reqBuf strings.Builder
	fmt.Fprintf(&reqBuf, "%s %s HTTP/1.1\r\n", r.Method, reqURI)
	fmt.Fprintf(&reqBuf, "Host: %s\r\n", backendHost)

	// Forward all headers except Host (already set above)
	for key, vals := range r.Header {
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(&reqBuf, "%s: %s\r\n", key, v)
		}
	}
	reqBuf.WriteString("\r\n")

	if _, err := backendConn.Write([]byte(reqBuf.String())); err != nil {
		backendConn.Close()
		respondError(w, http.StatusBadGateway, fmt.Sprintf("WebSocket backend write failed: %s", err))
		return
	}

	// Read the backend's response
	backendBuf := bufio.NewReader(backendConn)
	resp, err := http.ReadResponse(backendBuf, r)
	if err != nil {
		backendConn.Close()
		respondError(w, http.StatusBadGateway, fmt.Sprintf("WebSocket backend response read failed: %s", err))
		return
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		backendConn.Close()
		respondError(w, http.StatusBadGateway, fmt.Sprintf("WebSocket backend returned %d, expected 101", resp.StatusCode))
		return
	}

	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		backendConn.Close()
		respondError(w, http.StatusInternalServerError, "WebSocket hijack not supported")
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		backendConn.Close()
		slog.Error("WebSocket hijack failed", "component", "sandbox-proxy", "error", err)
		return
	}

	// Write the 101 response to the client.
	// resp.Status is the full status text (e.g., "101 Switching Protocols"),
	// so we only prepend the HTTP version, not the status code again.
	var respBuf strings.Builder
	fmt.Fprintf(&respBuf, "HTTP/%d.%d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status)
	for key, vals := range resp.Header {
		for _, v := range vals {
			fmt.Fprintf(&respBuf, "%s: %s\r\n", key, v)
		}
	}
	respBuf.WriteString("\r\n")

	if _, err := clientConn.Write([]byte(respBuf.String())); err != nil {
		clientConn.Close()
		backendConn.Close()
		slog.Error("WebSocket client write failed", "component", "sandbox-proxy", "error", err)
		return
	}

	// If there's buffered data from the backend (read past the HTTP response),
	// flush it to the client first.
	if backendBuf.Buffered() > 0 {
		buffered := make([]byte, backendBuf.Buffered())
		n, _ := backendBuf.Read(buffered)
		if n > 0 {
			clientConn.Write(buffered[:n])
		}
	}

	// If there's buffered data from the client, flush it to the backend.
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		n, _ := clientBuf.Reader.Read(buffered)
		if n > 0 {
			backendConn.Write(buffered[:n])
		}
	}

	slog.Info("WebSocket connected", "component", "sandbox-proxy", "remote_addr", r.RemoteAddr, "backend", backendHost, "path", path)

	// Bidirectional raw byte forwarding
	done := make(chan struct{})
	go func() {
		io.Copy(backendConn, clientConn)
		backendConn.Close()
		close(done)
	}()
	io.Copy(clientConn, backendConn)
	clientConn.Close()
	<-done
}
