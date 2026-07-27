package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	personalent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/credential"
	"github.com/SAP/astonish/pkg/store"
)

// personalCredentialStore implements store.CredentialStore for personal scope.
type personalCredentialStore struct {
	client       *personalent.Client
	credKey      []byte // per-org DEK for envelope encryption
	repairSchema func(context.Context) error
	mu           sync.RWMutex
}

var _ store.CredentialStore = (*personalCredentialStore)(nil)

func (cs *personalCredentialStore) Get(ctx context.Context, name string) *store.Credential {
	ent, err := cs.client.Credential.Query().
		Where(credential.NameEQ(name)).
		Only(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "get", err) == nil {
		ent, err = cs.client.Credential.Query().
			Where(credential.NameEQ(name)).
			Only(ctx)
	}
	if err != nil {
		slog.Warn("personal credential query failed", "name", name, "error", err)
		return nil
	}

	// Decrypt using envelope encryption: try DEK first, then master key, then plaintext.
	plaintext, err := decryptCredentialData(ent.Encrypted, cs.credKey)
	if err != nil {
		slog.Warn("personal credential decrypt failed", "name", name, "error", err)
		return nil
	}

	cred := &store.Credential{}
	if err := json.Unmarshal(plaintext, cred); err != nil {
		slog.Warn("personal credential unmarshal failed", "name", name, "encrypted_len", len(ent.Encrypted), "error", err)
		return nil
	}
	cred.Type = ent.CredType
	return cred
}

func (cs *personalCredentialStore) Set(ctx context.Context, name string, cred *store.Credential) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}

	// Encrypt the JSON blob using the per-org DEK (envelope encryption).
	encrypted, err := encryptCredentialData(data, cs.credKey)
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}

	return cs.setEncryptedLocked(ctx, name, cred.Type, encrypted, true)
}

func (cs *personalCredentialStore) setEncryptedLocked(ctx context.Context, name string, credType store.CredentialType, encrypted []byte, allowRepair bool) error {
	// Check if exists.
	existing, err := cs.client.Credential.Query().
		Where(credential.NameEQ(name)).
		Only(ctx)
	if err != nil && isMissingCredentialTableError(err) && allowRepair {
		if repairErr := cs.repairCredentialsSchema(ctx, "set-query", err); repairErr != nil {
			return err
		}
		return cs.setEncryptedLocked(ctx, name, credType, encrypted, false)
	}
	if err != nil && !personalent.IsNotFound(err) {
		return err
	}

	if existing != nil {
		err := existing.Update().
			SetCredType(credType).
			SetEncrypted(encrypted).
			Exec(ctx)
		if err != nil && isMissingCredentialTableError(err) && allowRepair {
			if repairErr := cs.repairCredentialsSchema(ctx, "set-update", err); repairErr != nil {
				return err
			}
			return cs.setEncryptedLocked(ctx, name, credType, encrypted, false)
		}
		return err
	}

	_, err = cs.client.Credential.Create().
		SetName(name).
		SetCredType(credType).
		SetEncrypted(encrypted).
		Save(ctx)
	if err != nil && isMissingCredentialTableError(err) && allowRepair {
		if repairErr := cs.repairCredentialsSchema(ctx, "set-create", err); repairErr != nil {
			return err
		}
		return cs.setEncryptedLocked(ctx, name, credType, encrypted, false)
	}
	return err
}

func (cs *personalCredentialStore) Remove(ctx context.Context, name string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	_, err := cs.client.Credential.Delete().
		Where(credential.NameEQ(name)).
		Exec(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "remove", err) == nil {
		_, err = cs.client.Credential.Delete().
			Where(credential.NameEQ(name)).
			Exec(ctx)
	}
	return err
}

func (cs *personalCredentialStore) List(ctx context.Context) map[string]store.CredentialType {
	ents, err := cs.client.Credential.Query().
		Order(credential.ByName()).
		All(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "list", err) == nil {
		ents, err = cs.client.Credential.Query().
			Order(credential.ByName()).
			All(ctx)
	}
	if err != nil {
		slog.Warn("personal credential list failed", "error", err)
		return nil
	}

	result := make(map[string]store.CredentialType, len(ents))
	for _, e := range ents {
		result[e.Name] = e.CredType
	}
	return result
}

func (cs *personalCredentialStore) Count(ctx context.Context) int {
	count, err := cs.client.Credential.Query().Count(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "count", err) == nil {
		count, err = cs.client.Credential.Query().Count(ctx)
	}
	if err != nil {
		slog.Warn("personal credential count failed", "error", err)
		return 0
	}
	return count
}

func (cs *personalCredentialStore) Resolve(ctx context.Context, name string) (string, string, error) {
	cred := cs.Get(ctx, name)
	return store.ResolveCredentialHeader(name, cred, oauthFetcher(name))
}

func (cs *personalCredentialStore) InvalidateToken(_ context.Context, name string) {
	globalOAuthCache.invalidate(name)
}

// Secret management - stored as credentials with special type prefix.

const secretCredType = "_secret"

func (cs *personalCredentialStore) SetSecret(ctx context.Context, key, value string) error {
	cred := &store.Credential{Type: secretCredType, Value: value}
	return cs.Set(ctx, "_secret:"+key, cred)
}

func (cs *personalCredentialStore) SetSecretBatch(ctx context.Context, secrets map[string]string) error {
	for k, v := range secrets {
		if err := cs.SetSecret(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (cs *personalCredentialStore) GetSecret(ctx context.Context, key string) string {
	cred := cs.Get(ctx, "_secret:"+key)
	if cred == nil {
		return ""
	}
	return cred.Value
}

func (cs *personalCredentialStore) RemoveSecret(ctx context.Context, key string) error {
	return cs.Remove(ctx, "_secret:"+key)
}

func (cs *personalCredentialStore) HasSecrets(ctx context.Context) bool {
	return cs.SecretCount(ctx) > 0
}

func (cs *personalCredentialStore) SecretCount(ctx context.Context) int {
	count, err := cs.client.Credential.Query().
		Where(credential.CredTypeEQ(secretCredType)).
		Count(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "secret-count", err) == nil {
		count, err = cs.client.Credential.Query().
			Where(credential.CredTypeEQ(secretCredType)).
			Count(ctx)
	}
	if err != nil {
		slog.Warn("personal credential secret count failed", "error", err)
		return 0
	}
	return count
}

func (cs *personalCredentialStore) ListSecrets(ctx context.Context) []string {
	ents, err := cs.client.Credential.Query().
		Where(credential.CredTypeEQ(secretCredType)).
		All(ctx)
	if err != nil && isMissingCredentialTableError(err) && cs.repairCredentialsSchema(ctx, "list-secrets", err) == nil {
		ents, err = cs.client.Credential.Query().
			Where(credential.CredTypeEQ(secretCredType)).
			All(ctx)
	}
	if err != nil {
		slog.Warn("personal credential list secrets failed", "error", err)
		return nil
	}

	keys := make([]string, 0, len(ents))
	for _, e := range ents {
		// Strip "_secret:" prefix.
		if len(e.Name) > 8 {
			keys = append(keys, e.Name[8:])
		}
	}
	return keys
}

func (cs *personalCredentialStore) Reload(ctx context.Context) error {
	return cs.repairCredentialsSchema(ctx, "reload", nil)
}

func (cs *personalCredentialStore) repairCredentialsSchema(ctx context.Context, operation string, cause error) error {
	if cs.repairSchema == nil {
		return cause
	}
	if cause != nil {
		slog.Warn("personal credential table missing; attempting schema repair", "operation", operation, "error", cause)
	} else {
		slog.Debug("checking personal credential schema", "operation", operation)
	}
	if err := cs.repairSchema(ctx); err != nil {
		slog.Warn("personal credential schema repair failed", "operation", operation, "error", err)
		return err
	}
	return nil
}
