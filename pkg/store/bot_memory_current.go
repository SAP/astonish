package store

import "strings"

// IsCurrentBotMemory selects factual recall, not evidence review or mutation
// authority. List/Get intentionally retain history. Only the legacy correction:
// protocol replaces content in place; all other supersession markers retire it.
// Memory rows have no expiry field: exception/lease expiry is authorization state,
// not a reason to erase or resurrect knowledge.
func IsCurrentBotMemory(row MemorySearchResult) bool {
	return row.Attribution == nil || row.Attribution.SupersededBy == "" || strings.HasPrefix(row.Attribution.SupersededBy, "correction:")
}
