package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// MemoryHealth is a non-owning on-demand input adapter for the existing memory
// health evaluator. It never searches across tiers, caches authority, or mutates
// source evidence. Every page is authorized by BotMemoryService before pagination.
type MemoryHealth struct{ memory *store.BotMemoryService }

func NewMemoryHealth(memory *store.BotMemoryService) (*MemoryHealth, error) {
	if memory == nil {
		return nil, store.ErrBotUnsupported
	}
	return &MemoryHealth{memory: memory}, nil
}

type MemorySnapshot struct {
	Fingerprints map[string]string          `json:"fingerprints"`
	Records      []store.MemorySearchResult `json:"records"`
	Fingerprint  string                     `json:"fingerprint"`
	Truncated    bool                       `json:"truncated"`
	EvaluatedAt  time.Time                  `json:"evaluated_at"`
}

// Snapshot is a bounded, current view, not a transactional or historical snapshot.
// The fingerprint is a content version, NOT a persistence CAS token. Credential
// categories are excluded from projections and health suggestions; credential
// stores are never consulted. Unlabelled secrets in arbitrary prose cannot be
// reliably classified by this adapter.
func (h *MemoryHealth) Snapshot(ctx context.Context, limit int) (*MemorySnapshot, error) {
	if limit < 1 || limit > 2000 {
		return nil, store.ErrBotInvalid
	}
	rows, err := h.memory.List(ctx, "", limit+1, 0)
	if err != nil {
		return nil, err
	}
	result := &MemorySnapshot{Fingerprints: map[string]string{}, Records: []store.MemorySearchResult{}, EvaluatedAt: time.Now().UTC(), Truncated: len(rows) > limit}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" || seen[row.ID] {
			return nil, store.ErrBotConflict
		}
		seen[row.ID] = true
		if credentialCategory(row.Category) {
			continue
		}
		result.Fingerprints[row.ID] = store.MemoryContentFingerprint(row)
		result.Records = append(result.Records, row)
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].ID < result.Records[j].ID })
	data, err := json.Marshal(result.Records)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, nil
}

func credentialCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "credential", "credentials", "secret", "secrets", "token", "tokens", "password", "passwords":
		return true
	}
	return false
}
