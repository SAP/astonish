package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/credential"
	"github.com/SAP/astonish/pkg/store"
)

const secretPrefix = "secret:"

// teamCredentialStore implements store.CredentialStore using the Ent team client.
type teamCredentialStore struct {
	client       *teament.Client
	credKey      []byte // per-org DEK for envelope encryption
	repairSchema func(context.Context) error
}

var _ store.CredentialStore = (*teamCredentialStore)(nil)

func (s *teamCredentialStore) Get(ctx context.Context, name string) *store.Credential {
	ent, err := s.client.Credential.Query().
		Where(credential.NameEQ(name)).
		Only(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "get", err) == nil {
		ent, err = s.client.Credential.Query().
			Where(credential.NameEQ(name)).
			Only(ctx)
	}
	if err != nil {
		slog.Warn("team credential query failed", "name", name, "error", err)
		return nil
	}

	// Decrypt using envelope encryption: try DEK first, then master key, then plaintext.
	plaintext, err := decryptCredentialData(ent.Encrypted, s.credKey)
	if err != nil {
		slog.Warn("team credential decrypt failed", "name", name, "error", err)
		return nil
	}

	var cred store.Credential
	if err := json.Unmarshal(plaintext, &cred); err != nil {
		slog.Warn("team credential unmarshal failed", "name", name, "encrypted_len", len(ent.Encrypted), "error", err)
		return nil
	}
	return &cred
}

func (s *teamCredentialStore) Set(ctx context.Context, name string, cred *store.Credential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("entstore: CredentialStore.Set: marshal: %w", err)
	}

	// Encrypt the JSON blob using the per-org DEK (envelope encryption).
	encrypted, err := encryptCredentialData(data, s.credKey)
	if err != nil {
		return fmt.Errorf("entstore: CredentialStore.Set: encrypt: %w", err)
	}

	credType := string(cred.Type)
	if credType == "" {
		credType = "unknown"
	}

	return s.setEncrypted(ctx, name, credType, encrypted, true)
}

func (s *teamCredentialStore) setEncrypted(ctx context.Context, name, credType string, encrypted []byte, allowRepair bool) error {
	// Try update first.
	n, updateErr := s.client.Credential.Update().
		Where(credential.NameEQ(name)).
		SetCredType(credType).
		SetEncrypted(encrypted).
		Save(ctx)
	if updateErr != nil {
		if allowRepair && isMissingCredentialTableError(updateErr) {
			if repairErr := s.repairCredentialsSchema(ctx, "set-update", updateErr); repairErr != nil {
				return fmt.Errorf("entstore: CredentialStore.Set: update: %w", updateErr)
			}
			return s.setEncrypted(ctx, name, credType, encrypted, false)
		}
		return fmt.Errorf("entstore: CredentialStore.Set: update: %w", updateErr)
	}
	if n == 0 {
		_, err := s.client.Credential.Create().
			SetName(name).
			SetCredType(credType).
			SetEncrypted(encrypted).
			Save(ctx)
		if err != nil {
			if allowRepair && isMissingCredentialTableError(err) {
				if repairErr := s.repairCredentialsSchema(ctx, "set-create", err); repairErr != nil {
					return fmt.Errorf("entstore: CredentialStore.Set: create: %w", err)
				}
				return s.setEncrypted(ctx, name, credType, encrypted, false)
			}
			return fmt.Errorf("entstore: CredentialStore.Set: create: %w", err)
		}
	}
	return nil
}

func (s *teamCredentialStore) Remove(ctx context.Context, name string) error {
	_, err := s.client.Credential.Delete().
		Where(credential.NameEQ(name)).
		Exec(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "remove", err) == nil {
		_, err = s.client.Credential.Delete().
			Where(credential.NameEQ(name)).
			Exec(ctx)
	}
	if err != nil {
		return fmt.Errorf("entstore: CredentialStore.Remove: %w", err)
	}
	return nil
}

func (s *teamCredentialStore) List(ctx context.Context) map[string]store.CredentialType {
	creds, err := s.client.Credential.Query().
		All(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "list", err) == nil {
		creds, err = s.client.Credential.Query().
			All(ctx)
	}
	if err != nil {
		slog.Warn("team credential list failed", "error", err)
		return nil
	}

	result := make(map[string]store.CredentialType, len(creds))
	for _, c := range creds {
		// Skip secrets from credential list.
		if strings.HasPrefix(c.Name, secretPrefix) {
			continue
		}
		result[c.Name] = c.CredType
	}
	return result
}

func (s *teamCredentialStore) Count(ctx context.Context) int {
	count, err := s.client.Credential.Query().
		Count(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "count", err) == nil {
		count, err = s.client.Credential.Query().
			Count(ctx)
	}
	if err != nil {
		slog.Warn("team credential count failed", "error", err)
		return 0
	}
	return count
}

func (s *teamCredentialStore) Resolve(ctx context.Context, name string) (string, string, error) {
	cred := s.Get(ctx, name)
	return store.ResolveCredentialHeader(name, cred, oauthFetcher(name))
}

func (s *teamCredentialStore) InvalidateToken(_ context.Context, name string) {
	globalOAuthCache.invalidate(name)
}

// --- Secret key-value store (stored as credentials with name prefix "secret:") ---

func (s *teamCredentialStore) SetSecret(ctx context.Context, key, value string) error {
	cred := &store.Credential{
		Type:  store.CredPassword,
		Value: value,
	}
	return s.Set(ctx, secretPrefix+key, cred)
}

func (s *teamCredentialStore) SetSecretBatch(ctx context.Context, secrets map[string]string) error {
	for k, v := range secrets {
		if err := s.SetSecret(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *teamCredentialStore) GetSecret(ctx context.Context, key string) string {
	cred := s.Get(ctx, secretPrefix+key)
	if cred == nil {
		return ""
	}
	return cred.Value
}

func (s *teamCredentialStore) RemoveSecret(ctx context.Context, key string) error {
	return s.Remove(ctx, secretPrefix+key)
}

func (s *teamCredentialStore) HasSecrets(ctx context.Context) bool {
	count, err := s.client.Credential.Query().
		Where(credential.NameHasPrefix(secretPrefix)).
		Count(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "has-secrets", err) == nil {
		count, err = s.client.Credential.Query().
			Where(credential.NameHasPrefix(secretPrefix)).
			Count(ctx)
	}
	if err != nil {
		slog.Warn("team credential secret count failed", "error", err)
		return false
	}
	return count > 0
}

func (s *teamCredentialStore) SecretCount(ctx context.Context) int {
	count, err := s.client.Credential.Query().
		Where(credential.NameHasPrefix(secretPrefix)).
		Count(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "secret-count", err) == nil {
		count, err = s.client.Credential.Query().
			Where(credential.NameHasPrefix(secretPrefix)).
			Count(ctx)
	}
	if err != nil {
		slog.Warn("team credential secret count failed", "error", err)
		return 0
	}
	return count
}

func (s *teamCredentialStore) ListSecrets(ctx context.Context) []string {
	creds, err := s.client.Credential.Query().
		Where(credential.NameHasPrefix(secretPrefix)).
		All(ctx)
	if err != nil && isMissingCredentialTableError(err) && s.repairCredentialsSchema(ctx, "list-secrets", err) == nil {
		creds, err = s.client.Credential.Query().
			Where(credential.NameHasPrefix(secretPrefix)).
			All(ctx)
	}
	if err != nil {
		slog.Warn("team credential list secrets failed", "error", err)
		return nil
	}

	keys := make([]string, 0, len(creds))
	for _, c := range creds {
		keys = append(keys, strings.TrimPrefix(c.Name, secretPrefix))
	}
	return keys
}

func (s *teamCredentialStore) Reload(ctx context.Context) error {
	return s.repairCredentialsSchema(ctx, "reload", nil)
}

func (s *teamCredentialStore) repairCredentialsSchema(ctx context.Context, operation string, cause error) error {
	if s.repairSchema == nil {
		return cause
	}
	if cause != nil {
		slog.Warn("team credential table missing; attempting schema repair", "operation", operation, "error", cause)
	} else {
		slog.Debug("checking team credential schema", "operation", operation)
	}
	if err := s.repairSchema(ctx); err != nil {
		slog.Warn("team credential schema repair failed", "operation", operation, "error", err)
		return err
	}
	return nil
}
