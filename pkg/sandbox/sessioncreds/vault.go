// Package sessioncreds materializes a tenant CredentialStore into a sandbox
// session file so in-sandbox tools (astonish node → http_request) can Resolve
// credentials — including Keystone/OAuth token fetch — without host-side HTTP.
package sessioncreds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
)

// VaultPath is the fixed path inside the sandbox for the session credential vault.
// Under /tmp so OpenShell's sandbox user can mkdir/write (unlike /run).
const VaultPath = "/tmp/astonish/session-credentials.json"

// VaultFileMode is the permission applied when pushing the vault into a sandbox.
const VaultFileMode = 0o600

const (
	oauthExpiryBuffer = 30 * time.Second
	secretNamePrefix  = "_secret:"
	secretCredType    = "_secret"
)

// vaultFile is the on-disk JSON shape.
type vaultFile struct {
	Credentials map[string]*store.Credential `json:"credentials"`
}

// Serialize builds vault JSON from a live CredentialStore (decrypted fields only).
// Secrets (_secret:*) are omitted — http_request only needs named credentials.
func Serialize(ctx context.Context, cs store.CredentialStore) ([]byte, error) {
	if cs == nil {
		return json.Marshal(vaultFile{Credentials: map[string]*store.Credential{}})
	}
	listed := cs.List(ctx)
	out := make(map[string]*store.Credential, len(listed))
	for name, typ := range listed {
		if typ == secretCredType || strings.HasPrefix(name, secretNamePrefix) {
			continue
		}
		cred := cs.Get(ctx, name)
		if cred == nil {
			continue
		}
		cp := *cred
		out[name] = &cp
	}
	return json.Marshal(vaultFile{Credentials: out})
}

// Load reads a vault file and returns an in-memory CredentialStore.
// Missing files yield an empty store (not an error) so node startup is resilient.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewStore(nil), nil
		}
		return nil, fmt.Errorf("read session credential vault: %w", err)
	}
	return Parse(data)
}

// Parse unmarshals vault JSON into an in-memory Store.
func Parse(data []byte) (*Store, error) {
	var vf vaultFile
	if err := json.Unmarshal(data, &vf); err != nil {
		return nil, fmt.Errorf("parse session credential vault: %w", err)
	}
	if vf.Credentials == nil {
		vf.Credentials = map[string]*store.Credential{}
	}
	return NewStore(vf.Credentials), nil
}

// Store is an in-memory CredentialStore backed by a session vault file.
// Resolve performs Keystone/OAuth fetches using the sandbox process HTTP stack.
type Store struct {
	mu     sync.RWMutex
	creds  map[string]*store.Credential
	tokens map[string]*cachedToken
}

type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// NewStore creates a Store from a credential map (copied).
func NewStore(creds map[string]*store.Credential) *Store {
	cp := make(map[string]*store.Credential, len(creds))
	for k, v := range creds {
		if v == nil {
			continue
		}
		c := *v
		cp[k] = &c
	}
	return &Store{
		creds:  cp,
		tokens: make(map[string]*cachedToken),
	}
}

var _ store.CredentialStore = (*Store)(nil)

func (s *Store) Get(_ context.Context, name string) *store.Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cred, ok := s.creds[name]
	if !ok {
		return nil
	}
	cp := *cred
	return &cp
}

func (s *Store) Set(_ context.Context, name string, cred *store.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cred == nil {
		delete(s.creds, name)
		return nil
	}
	cp := *cred
	s.creds[name] = &cp
	return nil
}

func (s *Store) Remove(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, name)
	delete(s.tokens, name)
	return nil
}

func (s *Store) List(_ context.Context) map[string]store.CredentialType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]store.CredentialType, len(s.creds))
	for k, v := range s.creds {
		out[k] = v.Type
	}
	return out
}

func (s *Store) Count(_ context.Context) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.creds)
}

func (s *Store) Resolve(_ context.Context, name string) (string, string, error) {
	cred := s.Get(context.Background(), name)
	return store.ResolveCredentialHeader(name, cred, s.tokenFetcher(name))
}

func (s *Store) InvalidateToken(_ context.Context, name string) {
	s.mu.Lock()
	delete(s.tokens, name)
	s.mu.Unlock()
}

func (s *Store) SetSecret(context.Context, string, string) error { return nil }
func (s *Store) SetSecretBatch(context.Context, map[string]string) error {
	return nil
}
func (s *Store) GetSecret(context.Context, string) string   { return "" }
func (s *Store) RemoveSecret(context.Context, string) error { return nil }
func (s *Store) HasSecrets(context.Context) bool            { return false }
func (s *Store) SecretCount(context.Context) int            { return 0 }
func (s *Store) ListSecrets(context.Context) []string       { return nil }
func (s *Store) Reload(context.Context) error               { return nil }

func (s *Store) tokenFetcher(name string) store.OAuthTokenFetcher {
	return func(cred *store.Credential) (string, error) {
		return s.getOrFetchToken(name, cred)
	}
}

func (s *Store) getOrFetchToken(name string, cred *store.Credential) (string, error) {
	s.mu.RLock()
	if cached, ok := s.tokens[name]; ok && time.Now().Before(cached.expiresAt) {
		tok := cached.accessToken
		s.mu.RUnlock()
		return tok, nil
	}
	s.mu.RUnlock()

	internal := toInternalCred(cred)
	var token string
	var expiresAt time.Time

	switch cred.Type {
	case store.CredOpenStackKeystone:
		tok, exp, err := credentials.FetchKeystoneToken(internal)
		if err != nil {
			return "", err
		}
		token = tok
		expiresAt = exp.Add(-oauthExpiryBuffer)
	case store.CredOAuthClientCreds:
		tok, expiresIn, err := credentials.FetchOAuthToken(internal)
		if err != nil {
			return "", err
		}
		token = tok
		expiresAt = time.Now().Add(time.Duration(expiresIn)*time.Second - oauthExpiryBuffer)
	default:
		return "", fmt.Errorf("credential %q: no token fetch for type %q", name, cred.Type)
	}

	if expiresAt.Before(time.Now()) {
		expiresAt = time.Now().Add(10 * time.Second)
	}

	s.mu.Lock()
	s.tokens[name] = &cachedToken{accessToken: token, expiresAt: expiresAt}
	s.mu.Unlock()
	return token, nil
}

func toInternalCred(cred *store.Credential) *credentials.Credential {
	return &credentials.Credential{
		Type:                        credentials.CredentialType(cred.Type),
		Header:                      cred.Header,
		Value:                       cred.Value,
		Token:                       cred.Token,
		Username:                    cred.Username,
		Password:                    cred.Password,
		AuthURL:                     cred.AuthURL,
		ClientID:                    cred.ClientID,
		ClientSecret:                cred.ClientSecret,
		Scope:                       cred.Scope,
		TokenURL:                    cred.TokenURL,
		AccessToken:                 cred.AccessToken,
		RefreshToken:                cred.RefreshToken,
		TokenExpiry:                 cred.TokenExpiry,
		UserDomain:                  cred.UserDomain,
		ProjectID:                   cred.ProjectID,
		ProjectName:                 cred.ProjectName,
		ProjectDomain:               cred.ProjectDomain,
		ApplicationCredentialID:     cred.ApplicationCredentialID,
		ApplicationCredentialSecret: cred.ApplicationCredentialSecret,
	}
}
