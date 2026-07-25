package credentials

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
)

// StoreAdapter wraps a store.CredentialStore to satisfy CredentialResolver.
// It converts *store.Credential → *credentials.Credential so the substitution
// engine works identically for both file-based and PG-backed credential stores.
type StoreAdapter struct {
	inner store.CredentialStore
}

// NewStoreAdapter wraps a store.CredentialStore as a CredentialResolver.
func NewStoreAdapter(cs store.CredentialStore) CredentialResolver {
	if cs == nil {
		return nil
	}
	return &StoreAdapter{inner: cs}
}

func (a *StoreAdapter) Get(name string) *Credential {
	sc := a.inner.Get(context.Background(), name)
	if sc == nil {
		return nil
	}
	return storeCredToInternal(sc)
}

func (a *StoreAdapter) Resolve(name string) (headerKey, headerValue string, err error) {
	return a.inner.Resolve(context.Background(), name)
}

func (a *StoreAdapter) Reload() error {
	return a.inner.Reload(context.Background())
}

// Compile-time check.
var _ CredentialResolver = (*StoreAdapter)(nil)

// storeCredToInternal converts a store.Credential to the internal credentials.Credential type.
func storeCredToInternal(sc *store.Credential) *Credential {
	if sc == nil {
		return nil
	}
	return &Credential{
		Type:                        CredentialType(sc.Type),
		Header:                      sc.Header,
		Value:                       sc.Value,
		Token:                       sc.Token,
		Username:                    sc.Username,
		Password:                    sc.Password,
		AuthURL:                     sc.AuthURL,
		ClientID:                    sc.ClientID,
		ClientSecret:                sc.ClientSecret,
		Scope:                       sc.Scope,
		TokenURL:                    sc.TokenURL,
		AccessToken:                 sc.AccessToken,
		RefreshToken:                sc.RefreshToken,
		TokenExpiry:                 sc.TokenExpiry,
		UserDomain:                  sc.UserDomain,
		ProjectID:                   sc.ProjectID,
		ProjectName:                 sc.ProjectName,
		ProjectDomain:               sc.ProjectDomain,
		ApplicationCredentialID:     sc.ApplicationCredentialID,
		ApplicationCredentialSecret: sc.ApplicationCredentialSecret,
		Content:                     sc.Content,
		ContentType:                 sc.ContentType,
	}
}

// HydrateFromStore populates the Redactor's signature map from a store.CredentialStore.
// This is used in platform mode to ensure the Redactor knows about all credential
// values from the PG-backed store. It is additive — existing signatures are preserved.
// Safe to call on every request (idempotent for unchanged credentials).
func (r *Redactor) HydrateFromStore(cs store.CredentialStore) {
	if cs == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for name := range cs.List(context.Background()) {
		sc := cs.Get(context.Background(), name)
		if sc == nil {
			continue
		}
		cred := storeCredToInternal(sc)
		r.addSecretFieldsLocked(name, cred)
	}

	// Also register key-value secrets (provider keys, tokens, etc.)
	for _, key := range cs.ListSecrets(context.Background()) {
		val := cs.GetSecret(context.Background(), key)
		if len(val) >= minSignatureLen {
			r.addVariantsLocked("secret/"+key, "value", val)
		}
	}
}
