package entstore

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Enumerate current candidates before either ranking path/top-K. List/Get remain
// history-preserving; no cache, metadata mutation or evidence deletion is needed.
func currentBotMemoryIDs(rows []store.MemorySearchResult) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if store.IsCurrentBotMemory(row) {
			if id, err := uuid.Parse(row.ID); err == nil {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
