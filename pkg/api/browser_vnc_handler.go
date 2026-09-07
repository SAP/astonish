package api

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/gorilla/mux"
)

// vncDialerPort is the KasmVNC websocket port inside the container.
const vncDialerPort = 6901

// registeredVNCDialFunc is a package-level dial function registered by the
// chat factory when it wires the browser Manager's ContainerDialFunc. This
// allows the VNC proxy handler to tunnel connections through the same exec
// tunnel (OpenShell gRPC or equivalent) that the browser tools use.
//
// Without this, the VNC proxy handler would only know about the global
// GetBrowserManager() singleton, which may not have ContainerDialFunc set
// (e.g., when wireSandboxBrowserCallbacks exits early because no local
// Chrome is detected on the worker pod).
var (
	registeredVNCDialFunc func(containerName string, port int) (net.Conn, error)
	registeredVNCDialMu   sync.RWMutex
)

// SetVNCContainerDialFunc registers a dial function for the VNC proxy handler
// to use when tunneling connections to KasmVNC inside a sandbox container.
// Called from the launcher's chat_factory after wiring ContainerDialFunc.
func SetVNCContainerDialFunc(fn func(containerName string, port int) (net.Conn, error)) {
	registeredVNCDialMu.Lock()
	registeredVNCDialFunc = fn
	registeredVNCDialMu.Unlock()
}

// getVNCDialFunc returns a dial function and HTTP transport for connecting to
// KasmVNC inside the named container. Priority order:
//  1. Registered VNC dial func (set by chat_factory for OpenShell/exec-tunnel)
//  2. Global browser Manager's ContainerDialFunc (legacy/Incus with tunnel)
//  3. Incus sandbox direct connection (fallback)
func getVNCDialFunc(containerName string) (dialFn func() (net.Conn, error), httpTransport *http.Transport, err error) {
	// Priority 1: Registered dial func from chat_factory (OpenShell path).
	registeredVNCDialMu.RLock()
	dialFunc := registeredVNCDialFunc
	registeredVNCDialMu.RUnlock()

	if dialFunc != nil {
		dialFn = func() (net.Conn, error) {
			return dialFunc(containerName, vncDialerPort)
		}
		httpTransport = &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return dialFunc(containerName, vncDialerPort)
			},
			MaxIdleConnsPerHost: 2,
		}
		return dialFn, httpTransport, nil
	}

	// Priority 2: Global browser Manager's ContainerDialFunc.
	mgr := GetBrowserManager()
	if mgr != nil && mgr.ContainerDialFunc != nil {
		dialFn = func() (net.Conn, error) {
			return mgr.ContainerDialFunc(containerName, vncDialerPort)
		}
		httpTransport = &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return mgr.ContainerDialFunc(containerName, vncDialerPort)
			},
			MaxIdleConnsPerHost: 2,
		}
		return dialFn, httpTransport, nil
	}

	// Priority 3: Backend exec tunnel (Docker/K8s OverlayFS).
	port := 6901
	if err := ensureProxySessionRunning(containerName); err != nil {
		return nil, nil, fmt.Errorf("failed to reach sandbox: %w", err)
	}
	dialFn = func() (net.Conn, error) {
		return studioBackendDial(containerName, port)
	}
	httpTransport = studioProxyTransport(containerName, port)
	return dialFn, httpTransport, nil
}

// BrowserVNCProxyHandler proxies HTTP and WebSocket requests to the KasmVNC
// web client running inside a browser container. This provides visual access
// to the browser for human-in-the-loop tasks like CAPTCHA solving.
//
// Route: /api/browser/vnc/{container}/{path...}
//
// KasmVNC is launched with -DisableBasicAuth so no credentials are needed.
// The Studio reverse proxy is the only path into the container, providing
// its own access control layer.
//
// The KasmVNC web client serves static HTML/JS/CSS on HTTP and uses WebSocket
// for the VNC stream. This handler transparently proxies both.
//
// Supports two backends:
//   - OpenShell: tunnels via the Manager's ContainerDialFunc (gRPC exec tunnel)
//   - Incus: tunnels via the Incus exec API (socat STDIO)
func BrowserVNCProxyHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	containerName := vars["container"]
	proxyPath := "/" + vars["path"]

	if containerName == "" {
		respondError(w, http.StatusBadRequest, "container name is required")
		return
	}

	// Resolve the dialer for this container (OpenShell exec tunnel or Incus).
	dialFn, httpTransport, err := getVNCDialFunc(containerName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Strip headers that confuse KasmVNC's auth layer. KasmVNC inspects
	// several headers for authentication decisions (SSO/auth-proxy pattern)
	// and returns 401 even with -DisableBasicAuth when they contain
	// unexpected values:
	//
	// - X-Original-URL / X-Original-Method: injected by upstream reverse
	//   proxies (muxpie). KasmVNC checks X-Original-URL and rejects
	//   requests where the path doesn't match its expectations.
	//
	// - Referer: the browser sets Referer on sub-resource requests to the
	//   proxy URL (e.g. /api/browser/vnc/{container}/...). KasmVNC sees a
	//   Referer containing /api/ — which doesn't match its own root — and
	//   returns 401 on every JS, CSS, and image load.
	//
	// NOTE: Origin is intentionally NOT stripped. KasmVNC requires the
	// Origin header for WebSocket upgrade negotiation on /websockify.
	// Without it, the WebSocket path returns 404 and the VNC connection
	// fails entirely (UI.rfb never initializes → lastActiveAt TypeError).
	r.Header.Del("X-Original-URL")
	r.Header.Del("X-Original-Method")
	r.Header.Del("Referer")

	// WebSocket upgrade — use tunnel proxy for the VNC stream
	if isWebSocketUpgrade(r) {
		proxyWebSocket(w, r, dialFn, proxyPath)
		return
	}

	// Regular HTTP — reverse proxy to KasmVNC's web interface
	target := &url.URL{
		Scheme: "http",
		Host:   "127.0.0.1:" + strconv.Itoa(vncDialerPort),
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = proxyPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = target.Host
		},
		Transport: httpTransport,
		ModifyResponse: func(resp *http.Response) error {
			// Strip headers that block iframe embedding. KasmVNC sets
			// Cross-Origin-Embedder-Policy and Cross-Origin-Opener-Policy
			// which prevent the web client from loading inside the Studio
			// iframe. These are safe to remove because the proxy is
			// same-origin with the Studio UI.
			resp.Header.Del("Cross-Origin-Embedder-Policy")
			resp.Header.Del("Cross-Origin-Opener-Policy")
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("Content-Security-Policy")

			// Fix Content-Type for static assets. KasmVNC's built-in HTTP
			// server serves all files as text/plain (including JS, CSS,
			// and HTML). Browsers with X-Content-Type-Options: nosniff
			// refuse to execute scripts that don't have a JS MIME type.
			// For the root path ("/"), force text/html since KasmVNC
			// serves its web client HTML there.
			if ct := resp.Header.Get("Content-Type"); ct == "text/plain" {
				if proxyPath == "/" {
					resp.Header.Set("Content-Type", "text/html; charset=utf-8")
				} else if mimeType := mime.TypeByExtension(filepath.Ext(proxyPath)); mimeType != "" {
					resp.Header.Set("Content-Type", mimeType)
				}
			}

			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			slog.Error("browser VNC proxy error",
				"container", containerName,
				"error", err,
			)
			rw.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(rw, "Browser VNC proxy error: %s", err)
		},
	}

	proxy.ServeHTTP(w, r)
}

// BrowserVNCInfoHandler returns the KasmVNC connection details for a browser
// container. Used by the Studio UI to determine where to connect.
//
// Route: GET /api/browser/vnc-info/{container}
func BrowserVNCInfoHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	containerName := vars["container"]

	if containerName == "" {
		respondError(w, http.StatusBadRequest, "container name is required")
		return
	}

	// Check if the Manager knows about this container (backend-agnostic).
	mgr := GetBrowserManager()
	if mgr != nil && mgr.ContainerDialFunc != nil && mgr.ContainerName() == containerName {
		// OpenShell path: container exists and is managed by the Manager.
		respondJSON(w, http.StatusOK, map[string]any{
			"container": containerName,
			"ip":        "tunnel", // Not a real IP — connections are tunneled.
			"vnc_port":  vncDialerPort,
			"proxy_url": fmt.Sprintf("/api/browser/vnc/%s/", containerName),
		})
		return
	}

	if err := ensureProxySessionRunning(containerName); err != nil {
		respondError(w, http.StatusBadGateway, "sandbox is not running: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"container": containerName,
		"ip":        "tunnel",
		"vnc_port":  vncDialerPort,
		"proxy_url": fmt.Sprintf("/api/browser/vnc/%s/", containerName),
	})
}

// BrowserHandoffDoneHandler is called when the user clicks "Done" in the
// browser panel. This revokes the VNC handoff token (blocking further proxy
// access) and stops any active CDP handoff server. The browser itself
// continues running — only the visual sharing session ends.
//
// Route: POST /api/browser/handoff-done
func BrowserHandoffDoneHandler(w http.ResponseWriter, r *http.Request) {
	mgr := GetBrowserManager()
	if mgr != nil {
		// Revoke the VNC handoff token for the container (if any).
		// This causes the auth middleware to reject subsequent VNC proxy
		// requests, effectively ending the visual sharing session.
		containerName := mgr.ContainerName()
		if containerName != "" {
			browser.GetHandoffTokenRegistry().Revoke(containerName)
		}

		// Stop any active CDP handoff server (used in host mode).
		mgr.StopHandoff()
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "browser sharing session ended",
	})
}
