package chatruntime

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
)

// unavailableCredentials prevents nil-triggered host credential fallback.
type unavailableCredentials struct{}

func (unavailableCredentials) Get(context.Context, string) *store.Credential { return nil }
func (unavailableCredentials) Set(context.Context, string, *store.Credential) error {
	return fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) Remove(context.Context, string) error {
	return fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) List(context.Context) map[string]store.CredentialType {
	return map[string]store.CredentialType{}
}
func (unavailableCredentials) Count(context.Context) int { return 0 }
func (unavailableCredentials) Resolve(context.Context, string) (string, string, error) {
	return "", "", fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) InvalidateToken(context.Context, string) {}
func (unavailableCredentials) SetSecret(context.Context, string, string) error {
	return fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) SetSecretBatch(context.Context, map[string]string) error {
	return fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) GetSecret(context.Context, string) string { return "" }
func (unavailableCredentials) RemoveSecret(context.Context, string) error {
	return fmt.Errorf("scoped credentials unavailable")
}
func (unavailableCredentials) HasSecrets(context.Context) bool      { return false }
func (unavailableCredentials) SecretCount(context.Context) int      { return 0 }
func (unavailableCredentials) ListSecrets(context.Context) []string { return nil }
func (unavailableCredentials) Reload(context.Context) error         { return nil }
