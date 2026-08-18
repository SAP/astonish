package a2a

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenExpired    = errors.New("a2a: token expired")
	ErrUntrustedIssuer = errors.New("a2a: untrusted issuer")
	ErrInvalidAudience = errors.New("a2a: invalid audience")
	ErrActorNotAllowed = errors.New("a2a: actor not allowed")
	ErrMissingActClaim = errors.New("a2a: missing required act claim")
	ErrTokenInvalid    = errors.New("a2a: token invalid")
)

// TrustedIssuer represents an IdP trusted for A2A token validation.
type TrustedIssuer struct {
	ID        string
	Name      string
	Issuer    string // must match JWT `iss` claim
	JWKSURL   string
	Audience  string // must match JWT `aud` claim
	UserClaim string // which claim identifies the user: "sub", "email", "preferred_username"
	OrgID     string // owning organization
}

// AllowedAgent represents an external service authorized to make A2A calls.
type AllowedAgent struct {
	ID        string
	Name      string
	ActorSub  string // must match JWT `act.sub` claim
	IssuerID  string
	OrgID     string
	RateLimit int
	MaxTasks  int
	Enabled   bool
}

// A2ATokenClaims holds the validated identity from an A2A JWT.
type A2ATokenClaims struct {
	UserIdentifier  string    // extracted from configured user_claim
	ActorIdentifier string    // from act.sub (empty if direct user token)
	Issuer          string
	IssuerID        string    // matched TrustedIssuer.ID
	OrgID           string
	ExpiresAt       time.Time
	RateLimit       int       // from matched AllowedAgent (0 = unlimited)
	MaxTasks        int       // from matched AllowedAgent (0 = unlimited)
}

// TokenValidatorConfig configures the token validator.
type TokenValidatorConfig struct {
	Issuers           []TrustedIssuer
	Agents            []AllowedAgent
	RequireActorClaim bool
}

// TokenValidator validates A2A JWT tokens against configured trusted issuers.
type TokenValidator struct {
	jwks              *JWKSClient
	issuers           []TrustedIssuer
	agents            []AllowedAgent
	requireActorClaim bool
}

// NewTokenValidator creates a new TokenValidator with the given configuration.
func NewTokenValidator(cfg TokenValidatorConfig) *TokenValidator {
	return &TokenValidator{
		jwks:              NewJWKSClient(),
		issuers:           cfg.Issuers,
		agents:            cfg.Agents,
		requireActorClaim: cfg.RequireActorClaim,
	}
}

// Validate validates an A2A JWT token string and returns the extracted claims.
func (v *TokenValidator) Validate(tokenStr string) (*A2ATokenClaims, error) {
	// Step 1: Parse JWT without verification to extract `iss` claim and `kid` from header.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("a2a: failed to parse token: %w", err)
	}

	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("a2a: unexpected claims type")
	}

	issuer, err := claims.GetIssuer()
	if err != nil || issuer == "" {
		return nil, fmt.Errorf("a2a: token missing iss claim")
	}

	kid, _ := unverified.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("a2a: token missing kid header")
	}

	// Step 2: Find matching TrustedIssuer by `iss` claim.
	var matchedIssuer *TrustedIssuer
	for i := range v.issuers {
		if v.issuers[i].Issuer == issuer {
			matchedIssuer = &v.issuers[i]
			break
		}
	}
	if matchedIssuer == nil {
		return nil, fmt.Errorf("%w: %q", ErrUntrustedIssuer, issuer)
	}

	// Step 3: Fetch signing key from JWKS using `kid` and the issuer's JWKS URL.
	pubKey, err := v.jwks.GetKey(matchedIssuer.JWKSURL, kid)
	if err != nil {
		return nil, fmt.Errorf("a2a: failed to fetch signing key: %w", err)
	}

	// Step 4: Verify signature, expiry, not-before using jwt.Parse with the key.
	verified, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		// Pin allowed algorithms to the key type for defense in depth
		switch pubKey.(type) {
		case *rsa.PublicKey:
			if t.Method.Alg() != "RS256" && t.Method.Alg() != "RS384" && t.Method.Alg() != "RS512" {
				return nil, fmt.Errorf("unexpected signing method %v for RSA key", t.Header["alg"])
			}
		case *ecdsa.PublicKey:
			if t.Method.Alg() != "ES256" && t.Method.Alg() != "ES384" && t.Method.Alg() != "ES512" {
				return nil, fmt.Errorf("unexpected signing method %v for EC key", t.Header["alg"])
			}
		default:
			return nil, fmt.Errorf("unsupported key type %T", pubKey)
		}
		return pubKey, nil
	}, jwt.WithExpirationRequired())
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %v", ErrTokenExpired, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	verifiedClaims, ok := verified.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("a2a: unexpected claims type after verification")
	}

	// Step 5: Verify `aud` claim contains the issuer's configured audience.
	audience, err := verifiedClaims.GetAudience()
	if err != nil {
		return nil, fmt.Errorf("a2a: failed to get audience claim: %w", err)
	}
	if !containsString(audience, matchedIssuer.Audience) {
		return nil, fmt.Errorf("%w: token audience %v does not contain %q", ErrInvalidAudience, audience, matchedIssuer.Audience)
	}

	// Step 6: Extract user identity from the configured `user_claim` field.
	userClaim := matchedIssuer.UserClaim
	if userClaim == "" {
		userClaim = "sub"
	}
	userIdentifier, _ := getStringClaim(verifiedClaims, userClaim)
	if userIdentifier == "" {
		return nil, fmt.Errorf("a2a: token missing user claim %q", userClaim)
	}

	// Extract expiry.
	exp, err := verifiedClaims.GetExpirationTime()
	if err != nil {
		return nil, fmt.Errorf("a2a: failed to get expiration: %w", err)
	}

	result := &A2ATokenClaims{
		UserIdentifier: userIdentifier,
		Issuer:         issuer,
		IssuerID:       matchedIssuer.ID,
		OrgID:          matchedIssuer.OrgID,
		ExpiresAt:      exp.Time,
	}

	// Step 7 & 8: Handle `act` claim.
	actorSub := extractActorSub(verifiedClaims)
	if actorSub != "" {
		// Verify the actor matches an AllowedAgent for this org.
		agent := v.findAllowedAgent(actorSub, matchedIssuer.ID, matchedIssuer.OrgID)
		if agent == nil {
			return nil, fmt.Errorf("%w: %q", ErrActorNotAllowed, actorSub)
		}
		if !agent.Enabled {
			return nil, fmt.Errorf("%w: %q (disabled)", ErrActorNotAllowed, actorSub)
		}
		result.ActorIdentifier = actorSub
		result.RateLimit = agent.RateLimit
		result.MaxTasks = agent.MaxTasks
	} else if v.requireActorClaim {
		return nil, ErrMissingActClaim
	}

	return result, nil
}

// findAllowedAgent finds an AllowedAgent matching the given actor subject, issuer ID, and org ID.
func (v *TokenValidator) findAllowedAgent(actorSub, issuerID, orgID string) *AllowedAgent {
	for i := range v.agents {
		a := &v.agents[i]
		if a.ActorSub == actorSub && a.IssuerID == issuerID && a.OrgID == orgID {
			return a
		}
	}
	return nil
}

// extractActorSub extracts the `act.sub` claim from JWT MapClaims.
// The `act` claim is a JSON object like {"sub": "service-id"}.
func extractActorSub(claims jwt.MapClaims) string {
	actRaw, ok := claims["act"]
	if !ok {
		return ""
	}
	actMap, ok := actRaw.(map[string]interface{})
	if !ok {
		return ""
	}
	sub, _ := actMap["sub"].(string)
	return sub
}

// getStringClaim extracts a string claim from MapClaims.
func getStringClaim(claims jwt.MapClaims, key string) (string, bool) {
	val, ok := claims[key]
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

// containsString checks if a string slice contains a target string.
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

