package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
)

type signingKey struct {
	id  string
	key *rsa.PrivateKey
}

func newSigningKey() (*signingKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, err
	}
	idBytes := make([]byte, 18)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	return &signingKey{id: base64.RawURLEncoding.EncodeToString(idBytes), key: key}, nil
}

func loadOrCreateSigningKey(ctx context.Context, oauthStore store.OAuthServerStore) (*signingKey, error) {
	keys, err := oauthStore.ListOAuthSigningKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list OAuth signing keys: %w", err)
	}
	for _, persisted := range keys {
		if persisted.Status != "active" || (!persisted.NotAfter.IsZero() && !persisted.NotAfter.After(time.Now().UTC())) {
			continue
		}
		key, err := decodeSigningKey(persisted.EncryptedPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt active OAuth signing key: %w", err)
		}
		return &signingKey{id: persisted.KeyID, key: key}, nil
	}
	key, err := newSigningKey()
	if err != nil {
		return nil, fmt.Errorf("generate OAuth signing key: %w", err)
	}
	encoded, err := encodeSigningKey(key.key)
	if err != nil {
		return nil, err
	}
	if err := oauthStore.SaveOAuthSigningKey(ctx, store.OAuthSigningKey{KeyID: key.id, Algorithm: "RS256", Status: "active", PublicJWK: key.publicJWK(), EncryptedPrivateKey: encoded, CreatedAt: time.Now().UTC()}); err != nil {
		return nil, fmt.Errorf("persist OAuth signing key: %w", err)
	}
	return key, nil
}

func encodeSigningKey(key *rsa.PrivateKey) ([]byte, error) {
	der := x509.MarshalPKCS1PrivateKey(key)
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, fmt.Errorf("get config directory: %w", err)
	}
	masterKey, err := credentials.LoadOrCreateMasterKey(configDir)
	if err != nil {
		return nil, fmt.Errorf("load master key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	ciphertext, err := credentials.Encrypt(encoded, masterKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt signing key: %w", err)
	}
	return ciphertext, nil
}

func decodeSigningKey(ciphertext []byte) (*rsa.PrivateKey, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, fmt.Errorf("get config directory: %w", err)
	}
	masterKey, err := credentials.LoadOrCreateMasterKey(configDir)
	if err != nil {
		return nil, fmt.Errorf("load master key: %w", err)
	}
	encoded, err := credentials.Decrypt(ciphertext, masterKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt signing key: %w", err)
	}
	block, _ := pem.Decode(encoded)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, fmt.Errorf("decode RSA signing key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA signing key: %w", err)
	}
	return key, nil
}

func (k *signingKey) publicJWK() map[string]any {
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": k.id,
		"n":   base64.RawURLEncoding.EncodeToString(k.key.PublicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.key.PublicKey.E)).Bytes()),
	}
}
