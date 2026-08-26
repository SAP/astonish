package api

import (
	"net/http"
	"strings"
)

// defaultMaxBody is the default body size limit for most endpoints (1 MB).
const defaultMaxBody int64 = 1 << 20 // 1 MB

// largeMaxBody is the body size limit for endpoints that accept larger payloads (10 MB).
// This covers file uploads, visual app saves, sandbox templates, etc.
const largeMaxBody int64 = 10 << 20 // 10 MB

// pptxImportMaxBody is the body size limit for the slides .pptx template import
// endpoint. Corporate templates with embedded fonts/media routinely exceed the
// 10 MB largeMaxBody, so this path gets a dedicated cap that matches the
// handler's own maxImportPPTXBytes budget (plus multipart overhead). The
// handler re-checks the exact decoded .pptx size, so this only guards memory.
const pptxImportMaxBody int64 = maxImportPPTXBytes + (1 << 20)

// MaxBodySizeMiddleware limits the size of request bodies to prevent
// denial-of-service attacks via oversized payloads. Most endpoints get a 1 MB
// limit; upload-heavy endpoints get 10 MB.
//
// When the limit is exceeded, http.MaxBytesReader causes the json.Decoder or
// io.ReadAll to return an error, and the handler's existing error-handling
// returns 400/413 to the client.
func MaxBodySizeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}

		limit := limitForPath(r.URL.Path, r.Method)
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// limitForPath returns the appropriate body size limit for a given path/method.
func limitForPath(path, method string) int64 {
	// GET/DELETE/HEAD requests rarely have bodies — use default limit.
	if method == http.MethodGet || method == http.MethodDelete || method == http.MethodHead {
		return defaultMaxBody
	}

	// The slides .pptx template import accepts multi-tens-of-MB corporate
	// decks; it enforces its own precise size cap and re-check internally.
	if strings.HasPrefix(path, "/api/docs/slides/import") {
		return pptxImportMaxBody
	}

	// Large-payload endpoints (file uploads, app saves, sandbox templates).
	largeEndpoints := []string{
		"/api/studio/apps",        // Visual app YAML can be large
		"/api/sandbox/templates",  // Sandbox template creation
		"/api/ai/chat",            // Chat messages with images/attachments
		"/api/studio/chat",        // Studio chat with file attachments
		"/api/fleet/sessions",     // Fleet session payloads
		"/api/studio/sessions",    // Session import
	}
	for _, ep := range largeEndpoints {
		if strings.HasPrefix(path, ep) {
			return largeMaxBody
		}
	}

	// SSE/streaming endpoints don't have meaningful request bodies but
	// are long-lived connections — skip limiting them would break streaming.
	// MaxBytesReader only limits reading, not writing, so it's safe.

	return defaultMaxBody
}
