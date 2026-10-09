package daemon

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/oauthserver"
)

func servesA2A(mode string) bool {
	return mode != config.DaemonModeWorker
}

func a2aServiceBaseURL(appCfg *config.AppConfig, port int) string {
	if appCfg != nil {
		issuer := appCfg.Storage.Auth.OAuthServer.EffectiveIssuer(port)
		if issuer != "" {
			return strings.TrimRight(issuer, "/")
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func isLoopbackHTTPIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

// newOAuthServerRuntimeConfig applies documented issuer/resource defaults so
// the built-in authorization server can start when oauth_server.issuer is
// omitted (local Studio loopback, Kubernetes until Helm sets a public URL).
func newOAuthServerRuntimeConfig(oauthCfg config.OAuthServerConfig, port int, builtinAuth bool) oauthserver.Config {
	issuer := oauthCfg.EffectiveIssuer(port)
	return oauthserver.Config{
		Issuer:          issuer,
		Resource:        oauthCfg.EffectiveResource(issuer),
		AccessTokenTTL:  time.Duration(oauthCfg.AccessTokenTTLMinutes) * time.Minute,
		RefreshTokenTTL: time.Duration(oauthCfg.RefreshTokenTTLDays) * 24 * time.Hour,
		// Loopback HTTP is safe for local SQLite deployments, including no-login
		// mode. Non-loopback issuers still require HTTPS unless builtin auth is
		// explicitly running in development mode.
		Development: isLoopbackHTTPIssuer(issuer) || (builtinAuth && strings.HasPrefix(issuer, "http://")),
	}
}
