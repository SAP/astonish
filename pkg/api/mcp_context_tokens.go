package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/execution"
)

const mcpContextTokenTTL = 10 * time.Minute

type mcpContextToken struct {
	principalKey string
	snapshotKey  string
	expiresAt    time.Time
	runtime      *MCPProgressiveRuntime
}

type mcpContextTokens struct {
	mu     sync.Mutex
	tokens map[string]mcpContextToken
}

func newMCPContextTokens() *mcpContextTokens {
	return &mcpContextTokens{tokens: make(map[string]mcpContextToken)}
}

func mcpPrincipalKey(p execution.Principal) string {
	return string(p.Kind) + "|" + p.Subject + "|" + p.ClientID + "|" + p.OrgSlug + "|" + p.TeamSlug
}

func (m *mcpContextTokens) issue(principal execution.Principal, runtime *MCPProgressiveRuntime) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for value, entry := range m.tokens {
		if now.After(entry.expiresAt) {
			delete(m.tokens, value)
		}
	}
	m.tokens[token] = mcpContextToken{principalKey: mcpPrincipalKey(principal), snapshotKey: runtime.snapshotKey(), expiresAt: now.Add(mcpContextTokenTTL), runtime: runtime}
	return token, nil
}

func (m *mcpContextTokens) validate(token string, principal execution.Principal) (mcpContextToken, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.tokens[token]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(m.tokens, token)
		return mcpContextToken{}, false
	}
	if entry.principalKey != mcpPrincipalKey(principal) || entry.runtime == nil || entry.snapshotKey != entry.runtime.snapshotKey() {
		return mcpContextToken{}, false
	}
	return entry, true
}
