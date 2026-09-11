package oauthserver

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
)

// RegisterRoutes mounts only OAuth protocol paths, leaving Studio and API routes
// to the caller. The endpoints are public; /authorize validates the platform
// session itself so discovery can remain standards-compliant.
func RegisterRoutes(router *mux.Router, server *Server) {
	if server == nil {
		return
	}
	h := server.Handler()
	for _, path := range []string{
		"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server",
		"/.well-known/oauth-protected-resource", "/oauth/jwks", "/oauth/authorize",
		"/oauth/token", "/oauth/revoke", "/oauth/introspect",
	} {
		router.Handle(path, h)
	}
}

// NewSessionValidator adapts the platform's already-authenticated session to
// OAuth without trusting fields supplied by the OAuth client.
func NewSessionValidator(validate func(context.Context, *http.Request) (Subject, error)) SessionValidator {
	return validate
}
