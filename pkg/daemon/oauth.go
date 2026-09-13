package daemon

import (
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/oauthserver"
)

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
		Development:     builtinAuth && strings.HasPrefix(issuer, "http://"),
	}
}
