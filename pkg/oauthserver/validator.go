package oauthserver

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/SAP/astonish/pkg/execution"
)

// ValidateBearer validates an Astonish-issued OAuth access token and maps its
// claims to the canonical execution principal. It deliberately trusts only the
// current signing key or active, published Astonish signing keys; it never
// fetches or accepts keys from an external identity provider.
func (s *Server) ValidateBearer(ctx context.Context, bearer, resource string, requiredScopes []string, surface execution.Surface) (execution.Principal, error) {
	if s == nil || s.store == nil || s.signing == nil {
		return execution.Principal{}, errors.New("oauth bearer validator is not initialized")
	}
	raw, err := bearerToken(bearer)
	if err != nil {
		return execution.Principal{}, err
	}
	if resource == "" {
		resource = s.config.Resource
	}

	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(s.config.Issuer),
		jwt.WithAudience(resource),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	token, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("oauth access token must use RS256")
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("oauth access token kid is required")
		}
		return s.publicKeyForKID(ctx, kid)
	})
	if err != nil || token == nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid oauth access token")
		}
		return execution.Principal{}, fmt.Errorf("validate oauth access token: %w", err)
	}

	scopes := strings.Fields(claimString(claims, "scope"))
	for _, required := range requiredScopes {
		if required == "" || !contains(scopes, required) {
			return execution.Principal{}, fmt.Errorf("oauth access token lacks required scope %q", required)
		}
	}

	principal := execution.Principal{
		Authentication: execution.AuthMethodOAuth,
		Surface:        surface,
		ClientID:       claimString(claims, "client_id"),
		Issuer:         s.config.Issuer,
		OrgSlug:        claimString(claims, "org_id"),
		TeamSlug:       claimString(claims, "team_id"),
		Scopes:         scopes,
		Authenticated:  true,
	}
	principal.Subject = claimString(claims, "sub")
	principal.Actor = actorSubject(claims["act"])
	if principal.Subject == "" {
		principal.Kind = execution.PrincipalKindService
	} else {
		principal.Kind = execution.PrincipalKindUser
	}
	if err := principal.Validate(); err != nil {
		return execution.Principal{}, fmt.Errorf("validate oauth principal: %w", err)
	}
	return principal, nil
}

func bearerToken(value string) (string, error) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", errors.New("oauth bearer token is required")
	}
	return parts[1], nil
}

func (s *Server) publicKeyForKID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == s.signing.id {
		return &s.signing.key.PublicKey, nil
	}
	keys, err := s.store.ListOAuthSigningKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list oauth signing keys: %w", err)
	}
	now := time.Now().UTC()
	for _, key := range keys {
		if key.KeyID != kid || key.Algorithm != jwt.SigningMethodRS256.Alg() || key.Status != "active" || (!key.NotAfter.IsZero() && !key.NotAfter.After(now)) {
			continue
		}
		return publicKeyFromJWK(key.PublicJWK)
	}
	return nil, errors.New("oauth access token signing key is not trusted")
}

func publicKeyFromJWK(jwk map[string]any) (*rsa.PublicKey, error) {
	if jwk["kty"] != "RSA" || jwk["alg"] != jwt.SigningMethodRS256.Alg() {
		return nil, errors.New("invalid oauth signing JWK")
	}
	n, ok := jwk["n"].(string)
	if !ok || n == "" {
		return nil, errors.New("invalid oauth signing JWK modulus")
	}
	e, ok := jwk["e"].(string)
	if !ok || e == "" {
		return nil, errors.New("invalid oauth signing JWK exponent")
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, fmt.Errorf("decode oauth signing JWK modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, fmt.Errorf("decode oauth signing JWK exponent: %w", err)
	}
	exponent := new(big.Int).SetBytes(eBytes)
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > int64(^uint(0)>>1) {
		return nil, errors.New("invalid oauth signing JWK exponent")
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(exponent.Int64())}
	if key.N.Sign() <= 0 || key.E < 3 || key.E%2 == 0 {
		return nil, errors.New("invalid oauth signing JWK")
	}
	return key, nil
}

func claimString(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}

func actorSubject(value any) string {
	actor, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	subject, _ := actor["sub"].(string)
	return subject
}
