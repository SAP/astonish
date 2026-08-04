package api

import (
	"net/http"
	"strings"
)

// cspPolicy defines the Content-Security-Policy header for the Studio UI.
// The policy restricts resource loading to the same origin with targeted
// exceptions for Tailwind CSS inline styles and WebSocket connections.
//
// frame-src 'self' allows the Studio UI to embed same-origin iframes
// (e.g. the KasmVNC browser view during human-in-the-loop handoff sessions
// and the generative UI sandbox served from /api/app-preview/sandbox-full).
//
// The sha256 hash in script-src allows the inline <script> in the device
// authorization page (auth.go authPageHTML). If that script changes, the
// hash must be recomputed:
//
//	printf '%s' '<script content>' | openssl dgst -sha256 -binary | base64
//
// wasm-unsafe-eval allows WebAssembly compilation without allowing general
// JavaScript eval. The client-side DOCX exporter uses @m2d/remark-docx, whose
// image preprocessing path instantiates a WASM module even for markdown reports
// without user-authored images.
//
// media-src allows blob: so Studio can play video/audio artifacts (e.g.
// browser session recordings) loaded via createObjectURL after a credentialed
// fetch — a bare <video src="/api/..."> cannot send team auth headers.
//
// fonts.googleapis.com/fonts.gstatic.com are used by the production index.html
// font link. api.github.com is used by the client-side release update check.
const cspPolicy = "default-src 'self'; " +
	"script-src 'self' 'wasm-unsafe-eval' 'sha256-QPIelXUbpkDESZsTgggSaMGNOA/Le9qMm+4Wa+lXIvs='; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"connect-src 'self' ws: wss: https://api.github.com; " +
	"frame-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// sandboxCSP is the Content-Security-Policy for the generative UI sandbox
// iframe served at /api/app-preview/sandbox-full. It is intentionally
// permissive (allows inline scripts, eval) because security isolation is
// enforced by the iframe sandbox="allow-scripts" attribute WITHOUT
// allow-same-origin — the document runs on an opaque origin and cannot
// access the parent's DOM, cookies, localStorage, or make credentialed
// requests. connect-src 'none' ensures no direct network access; all data
// flows through postMessage to the parent.
const sandboxCSP = "default-src 'none'; " +
	"script-src 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'unsafe-inline'; " +
	"img-src data: blob:; " +
	"font-src data:; " +
	"connect-src 'none'"

// CSPMiddleware adds Content-Security-Policy and other security headers
// to all responses. It should be applied inside auth but outside the
// subdomain proxy (proxied container apps have their own CSP).
//
// The browser VNC proxy path (/api/browser/vnc/) is exempted because
// KasmVNC's web client needs to load its own scripts and styles which
// don't comply with the Studio CSP. The VNC proxy is same-origin with
// Studio so iframe embedding is safe.
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CSP for VNC proxy — KasmVNC serves its own web client with
		// inline scripts/styles that conflict with the Studio CSP.
		// Also skip X-Content-Type-Options because KasmVNC serves JS files
		// as text/plain. With "nosniff" browsers refuse to execute scripts
		// that don't have a JavaScript MIME type.
		if strings.HasPrefix(r.URL.Path, "/api/browser/vnc/") {
			next.ServeHTTP(w, r)
			return
		}

		// The generative UI sandbox iframe gets a permissive CSP because
		// it needs inline scripts and eval (Sucrase transpilation). Origin
		// isolation is enforced by the iframe sandbox attribute — see
		// sandboxCSP comment above.
		if r.URL.Path == "/api/app-preview/sandbox-full" {
			w.Header().Set("Content-Security-Policy", sandboxCSP)
		} else {
			w.Header().Set("Content-Security-Policy", cspPolicy)
		}
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
