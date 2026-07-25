package credentials

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

const (
	// minSignatureLen is the minimum length of a secret value to be tracked
	// for redaction. Shorter values would cause too many false positives.
	minSignatureLen = 8

	// redactedPrefix is the prefix used in redaction replacement text.
	redactedPrefix = "[REDACTED:"
	// redactedSuffix is the suffix used in redaction replacement text.
	redactedSuffix = "]"

	// storeKeyRedactName is the redaction label for the encryption key itself.
	storeKeyRedactName = "store-encryption-key"
)

// credRef identifies a credential field: which credential and which field
// the secret value belongs to. Used by Placeholderize to produce
// {{CREDENTIAL:name:field}} tokens.
type credRef struct {
	name  string // credential name (e.g., "proxmox")
	field string // field name (e.g., "value", "token", "password")
}

// Redactor scans text for known credential values and replaces them with
// redaction markers. It tracks multiple encodings of each secret (raw,
// base64, URL-encoded) to catch common transformation attempts.
type Redactor struct {
	mu sync.RWMutex
	// signatures maps secret value variants to their credential reference.
	// Multiple entries may map to the same credential (raw, base64, url-encoded).
	signatures map[string]credRef
}

// NewRedactor creates an empty Redactor.
func NewRedactor() *Redactor {
	return &Redactor{
		signatures: make(map[string]credRef),
	}
}

// UpdateFromCredentials rebuilds the signature list from all stored credentials.
// Called after any credential is added, updated, or removed.
func (r *Redactor) UpdateFromCredentials(creds map[string]*Credential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Preserve non-credential signatures (like the store key and OAuth tokens)
	preserved := make(map[string]credRef)
	for sig, ref := range r.signatures {
		if strings.HasSuffix(ref.name, "/token") || ref.name == storeKeyRedactName {
			preserved[sig] = ref
		}
	}

	r.signatures = preserved

	for name, cred := range creds {
		r.addSecretFieldsLocked(name, cred)
	}
}

// AddSecret adds a single secret value to the redaction list.
// Used for dynamic values like OAuth access tokens.
func (r *Redactor) AddSecret(name, value string) {
	if len(value) < minSignatureLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addVariantsLocked(name, "token", value)
}

// AddTransientSecret registers a secret value for redaction without a
// credential name. Used as a safety net for pending secrets extracted from
// user messages (<<<value>>> tags) before they are formally saved. The
// redaction label uses "pending-secret" as the name.
func (r *Redactor) AddTransientSecret(value string) {
	if len(value) < minSignatureLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addVariantsLocked("pending-secret", "value", value)
}

// RemoveByName removes all signatures associated with a credential name.
func (r *Redactor) RemoveByName(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for sig, ref := range r.signatures {
		if ref.name == name || strings.HasPrefix(ref.name, name+"/") {
			delete(r.signatures, sig)
		}
	}
}

// Redact scans text for any known credential values and replaces them
// with [REDACTED:credential-name] markers.
func (r *Redactor) Redact(text string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.signatures) == 0 {
		return text
	}

	for sig, ref := range r.signatures {
		if strings.Contains(text, sig) {
			text = strings.ReplaceAll(text, sig, fmt.Sprintf("%s%s%s", redactedPrefix, ref.name, redactedSuffix))
		}
	}
	return text
}

// Placeholderize scans text for any known credential values and replaces them
// with {{CREDENTIAL:name:field}} tokens. Unlike Redact() which produces opaque
// markers, this method produces actionable placeholders that document exactly
// how to use the credential. This is intended for memory notes and other
// contexts where the placeholder should be self-documenting.
func (r *Redactor) Placeholderize(text string) (result string, count int) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.signatures) == 0 {
		return text, 0
	}

	for sig, ref := range r.signatures {
		if strings.Contains(text, sig) {
			text = strings.ReplaceAll(text, sig, FormatPlaceholder(ref.name, ref.field))
			count++
		}
	}
	return text, count
}

// RedactMap deep-walks a map and redacts all string values. Returns a new map
// (the original is not modified). Used for tool output redaction.
func (r *Redactor) RedactMap(m map[string]any) map[string]any {
	r.mu.RLock()
	if len(r.signatures) == 0 {
		r.mu.RUnlock()
		return m
	}
	r.mu.RUnlock()

	return r.redactMapRecursive(m)
}

func (r *Redactor) redactMapRecursive(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = r.redactValue(v)
	}
	return result
}

func (r *Redactor) redactValue(v any) any {
	switch val := v.(type) {
	case string:
		return r.Redact(val)
	case map[string]any:
		return r.redactMapRecursive(val)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = r.redactValue(item)
		}
		return result
	default:
		return v
	}
}

// addSecretFieldsLocked extracts all secret-bearing fields from a credential
// and adds their variants to the signature list. Must be called with mu held.
func (r *Redactor) addSecretFieldsLocked(name string, cred *Credential) {
	switch cred.Type {
	case CredAPIKey:
		r.addVariantsLocked(name, "value", cred.Value)
	case CredBearer:
		r.addVariantsLocked(name, "token", cred.Token)
	case CredBasic:
		r.addVariantsLocked(name, "password", cred.Password)
		// Also track the base64-encoded "user:pass" since that's what goes on the wire
		basicAuth := cred.Username + ":" + cred.Password
		if len(basicAuth) >= minSignatureLen {
			b64 := base64.StdEncoding.EncodeToString([]byte(basicAuth))
			r.signatures[b64] = credRef{name: name, field: "password"}
		}
	case CredPassword:
		r.addVariantsLocked(name, "password", cred.Password)
		// Username in password-type credentials is often an app credential ID
		// or service account — treat it as sensitive.
		r.addVariantsLocked(name, "username", cred.Username)
	case CredOAuthClientCreds:
		r.addVariantsLocked(name, "client_secret", cred.ClientSecret)
		// Client ID is often semi-public but let's protect it too
		r.addVariantsLocked(name, "client_id", cred.ClientID)
	case CredOAuthAuthCode:
		r.addVariantsLocked(name, "client_secret", cred.ClientSecret)
		r.addVariantsLocked(name, "client_id", cred.ClientID)
		r.addVariantsLocked(name, "access_token", cred.AccessToken)
		r.addVariantsLocked(name, "refresh_token", cred.RefreshToken)
	case CredOpenStackKeystone:
		if keystoneUsesAppCred(cred) {
			r.addVariantsLocked(name, "application_credential_id", cred.ApplicationCredentialID)
			r.addVariantsLocked(name, "application_credential_secret", cred.ApplicationCredentialSecret)
		} else {
			r.addVariantsLocked(name, "password", cred.Password)
			r.addVariantsLocked(name, "username", cred.Username)
		}
	case CredRawContent:
		r.addVariantsLocked(name, "content", cred.Content)
	}
}

// addVariantsLocked adds a secret value and its common encodings to the
// signature list. Must be called with mu held.
func (r *Redactor) addVariantsLocked(name, field, value string) {
	if len(value) < minSignatureLen {
		return
	}

	ref := credRef{name: name, field: field}

	// Raw value
	r.signatures[value] = ref

	// Base64-encoded (common in headers, JSON payloads)
	b64 := base64.StdEncoding.EncodeToString([]byte(value))
	if b64 != value && len(b64) >= minSignatureLen {
		r.signatures[b64] = ref
	}

	// URL-encoded (common in query strings, form bodies)
	urlEnc := url.QueryEscape(value)
	if urlEnc != value && len(urlEnc) >= minSignatureLen {
		r.signatures[urlEnc] = ref
	}
}

// SignatureCount returns the number of tracked signatures (for testing).
func (r *Redactor) SignatureCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.signatures)
}
