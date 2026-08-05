package tools

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCacheFilePath = "/tmp/.astonish_read_cache.json"
	maxCacheSize         = 100
)

// cacheFilePath is the path to the read cache file. Tests can override this.
var cacheFilePath = defaultCacheFilePath

// currentCaller identifies which agent is making the current tool call.
// Set by ExecuteTool before dispatch. Safe because astonish node processes
// tool calls sequentially (one at a time). Used to scope cache dedup so
// one agent's read doesn't produce "unchanged" for a different agent.
var currentCaller string

// CacheEntry represents a single entry in the file read cache.
type CacheEntry struct {
	MtimeNs    int64  `json:"mtime_ns"`
	TotalLines int    `json:"total_lines"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	Source     string `json:"source"` // "read", "edit", or "write"
	Verified   bool   `json:"verified"`
	AccessTime int64  `json:"access_time"` // unix nano for LRU eviction
}

// FileReadCache is the disk-persisted mtime-based read cache.
type FileReadCache struct {
	mu      sync.Mutex
	Version int                   `json:"version"`
	Entries map[string]CacheEntry `json:"entries"`
}

// buildCacheKey creates a cache key from caller, path, offset, and limit.
// Format: "caller|path:offset:limit" — the pipe separates caller from the
// path-based key. If caller is empty, defaults to "_".
func buildCacheKey(path string, offset, limit int) string {
	caller := currentCaller
	if caller == "" {
		caller = "_"
	}
	return caller + "|" + path + ":" + strconv.Itoa(offset) + ":" + strconv.Itoa(limit)
}

// parseCacheKeyPath extracts the path from a cache key.
func parseCacheKeyPath(key string) string {
	// Key format: "caller|/path/to/file:offset:limit"
	// Strip caller prefix
	if pipeIdx := strings.IndexByte(key, '|'); pipeIdx >= 0 {
		key = key[pipeIdx+1:]
	}
	// Now key is "/path/to/file:offset:limit"
	// Find the last two colons that are followed by numbers
	lastColon := strings.LastIndex(key, ":")
	if lastColon <= 0 {
		return key
	}
	secondLastColon := strings.LastIndex(key[:lastColon], ":")
	if secondLastColon <= 0 {
		return key
	}
	return key[:secondLastColon]
}

// LoadFileReadCache loads the cache from disk. Returns nil if the cache file
// doesn't exist or is corrupt (graceful degradation).
func LoadFileReadCache() *FileReadCache {
	data, err := os.ReadFile(cacheFilePath)
	if err != nil {
		// No cache file — create a new empty cache
		return &FileReadCache{
			Version: 1,
			Entries: make(map[string]CacheEntry),
		}
	}

	var cache FileReadCache
	if err := json.Unmarshal(data, &cache); err != nil {
		slog.Warn("file_read_cache: corrupt cache file, starting fresh", "error", err)
		return &FileReadCache{
			Version: 1,
			Entries: make(map[string]CacheEntry),
		}
	}
	if cache.Entries == nil {
		cache.Entries = make(map[string]CacheEntry)
	}
	return &cache
}

// Save persists the cache to disk atomically (write to temp + rename).
func (c *FileReadCache) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// LRU eviction if over limit
	if len(c.Entries) > maxCacheSize {
		c.evictLocked()
	}

	data, err := json.Marshal(c)
	if err != nil {
		slog.Warn("file_read_cache: failed to marshal cache", "error", err)
		return
	}

	tmpPath := cacheFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		slog.Warn("file_read_cache: failed to write temp cache file", "error", err)
		return
	}
	if err := os.Rename(tmpPath, cacheFilePath); err != nil {
		slog.Warn("file_read_cache: failed to rename cache file", "error", err)
		_ = os.Remove(tmpPath)
	}
}

// Get retrieves a cache entry by key.
func (c *FileReadCache) Get(key string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Entries[key]
	return entry, ok
}

// Set stores or updates a cache entry.
func (c *FileReadCache) Set(key string, entry CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Update access time for LRU
	entry.AccessTime = timeNowUnixNano()
	c.Entries[key] = entry
}

// HasSeenEntry reports whether the agent has demonstrably observed the current
// contents of the given path this session — i.e. there is a cache entry whose
// source is "read", "edit", or "write". Used by the must-read-before-edit
// guard: a successful edit or write proves the agent knows the file's current
// content just as much as a read does, so a follow-up edit must not be blocked.
func (c *FileReadCache) HasSeenEntry(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.Entries {
		if parseCacheKeyPath(key) != path {
			continue
		}
		switch entry.Source {
		case "read", "edit", "write":
			return true
		}
	}
	return false
}

// HasAnyReadEntries checks if the cache has any entries with source="read".
// Used to determine if the cache is actively being used in an agent session.
// If no read entries exist, the must-read-before-edit guard is lenient.
func (c *FileReadCache) HasAnyReadEntries() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.Entries {
		if entry.Source == "read" {
			return true
		}
	}
	return false
}

// InvalidatePath removes all cache entries for the given path.
func (c *FileReadCache) InvalidatePath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.Entries {
		if parseCacheKeyPath(key) == path {
			delete(c.Entries, key)
		}
	}
}

// MarkAllUnverified marks all entries as unverified (used after shell_command).
// On next read, unverified entries trigger a stat() check before returning cached.
func (c *FileReadCache) MarkAllUnverified() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.Entries {
		entry.Verified = false
		c.Entries[key] = entry
	}
}

// evictLocked removes oldest entries until we're at maxCacheSize.
// Must be called with c.mu held.
func (c *FileReadCache) evictLocked() {
	if len(c.Entries) <= maxCacheSize {
		return
	}

	type kv struct {
		key        string
		accessTime int64
	}
	pairs := make([]kv, 0, len(c.Entries))
	for k, v := range c.Entries {
		pairs = append(pairs, kv{k, v.AccessTime})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].accessTime < pairs[j].accessTime
	})

	toRemove := len(c.Entries) - maxCacheSize
	for i := 0; i < toRemove; i++ {
		delete(c.Entries, pairs[i].key)
	}
}

// timeNowUnixNano returns the current time in unix nanoseconds.
// Extracted as a variable to allow testing.
var timeNowUnixNano = func() int64 {
	return time.Now().UnixNano()
}
