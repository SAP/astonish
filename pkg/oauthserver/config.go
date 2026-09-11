package oauthserver

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	GrantAuthorizationCode = "authorization_code"
	GrantRefreshToken      = "refresh_token"
	GrantClientCredentials = "client_credentials"
)

// Config controls Astonish's OAuth issuer. Issuer and Resource must be stable,
// externally reachable HTTPS URLs outside explicit development environments.
type Config struct {
	Issuer          string
	Resource        string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	CodeTTL         time.Duration
	Development     bool
}

func (c Config) normalized() (Config, error) {
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	c.Resource = strings.TrimRight(strings.TrimSpace(c.Resource), "/")
	if c.Issuer == "" || c.Resource == "" {
		return c, errors.New("oauth issuer and resource are required")
	}
	for name, raw := range map[string]string{"issuer": c.Issuer, "resource": c.Resource} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return c, errors.New("oauth " + name + " must be an absolute URL")
		}
		if !c.Development && u.Scheme != "https" {
			return c, errors.New("oauth " + name + " must use HTTPS")
		}
	}
	if c.AccessTokenTTL <= 0 {
		c.AccessTokenTTL = 15 * time.Minute
	}
	if c.RefreshTokenTTL <= 0 {
		c.RefreshTokenTTL = 30 * 24 * time.Hour
	}
	if c.CodeTTL <= 0 {
		c.CodeTTL = 5 * time.Minute
	}
	return c, nil
}
