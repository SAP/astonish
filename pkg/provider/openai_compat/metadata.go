package openai_compat

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

// ModelMetadata describes only explicitly reported limits for an exact model name.
// ContextLength preserves an explicit total context window; when no total is
// reported it contains max_input_tokens as a conservative input budget.
type ModelMetadata struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextLength   int    `json:"context_length,omitempty"`
	MaxInputTokens  int    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int    `json:"max_completion_tokens,omitempty"`
}

const (
	metadataBodyLimit      = 8 << 20
	metadataTimeout        = 3 * time.Second
	metadataSuccessTTL     = time.Hour
	metadataUnsupportedTTL = time.Minute
)

type metadataCacheEntry struct {
	models  map[string]ModelMetadata
	expires time.Time
}

var metadataCache = struct {
	sync.Mutex
	entries map[[32]byte]metadataCacheEntry
}{entries: make(map[[32]byte]metadataCacheEntry)}

type metadataProbeStatus uint8

const (
	metadataTransient metadataProbeStatus = iota
	metadataUnsupported
	metadataSuccess
	metadataUnauthorized
)

type metadataInfoResponse struct {
	Data *[]struct {
		ModelName string `json:"model_name"`
		ModelInfo *struct {
			ContextLength   int `json:"context_length"`
			ContextWindow   int `json:"context_window"`
			MaxInputTokens  int `json:"max_input_tokens"`
			MaxOutputTokens int `json:"max_output_tokens"`
		} `json:"model_info"`
	} `json:"data"`
}

// FetchModelMetadata optionally probes LiteLLM model-info endpoints. Failures
// return no metadata and never affect ordinary model listing or generation.
// Responses are bounded, never logged, and cached independently per URL/key.
func FetchModelMetadata(ctx context.Context, apiKey, baseURL string) map[string]ModelMetadata {
	if ctx.Err() != nil {
		return nil
	}
	normalized, ok := normalizeMetadataURL(baseURL)
	if !ok {
		return nil
	}
	key := metadataCacheKey(normalized, apiKey)
	metadataCache.Lock()
	entry, found := metadataCache.entries[key]
	if found && !time.Now().Before(entry.expires) {
		delete(metadataCache.entries, key)
		found = false
	}
	var cached map[string]ModelMetadata
	if found {
		cached = copyMetadata(entry.models)
	}
	metadataCache.Unlock()
	if found {
		return cached
	}

	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	client := httpool.Client(0)
	// Reject even same-host redirects: a Location must never receive credentials.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base, _ := url.Parse(normalized)
	paths := []string{base.Path + "/model/info", strings.TrimSuffix(base.Path, "/v1") + "/model/info"}
	allUnsupported := true
	for _, path := range paths {
		endpoint := *base
		endpoint.Path = path
		endpoint.RawPath = ""
		models, status := probeModelMetadata(ctx, client, apiKey, endpoint.String())
		if ctx.Err() != nil {
			return nil
		}
		if status == metadataSuccess {
			storeMetadata(key, models, metadataSuccessTTL)
			return copyMetadata(models)
		}
		if status == metadataUnauthorized {
			// Stop on denied credentials rather than probing another path. Cache
			// briefly only when no earlier transient failure could mask support.
			if allUnsupported {
				storeMetadata(key, nil, metadataUnsupportedTTL)
			}
			return nil
		}
		if status != metadataUnsupported {
			allUnsupported = false
		}
	}
	if allUnsupported && ctx.Err() == nil {
		storeMetadata(key, nil, metadataUnsupportedTTL)
	}
	return nil
}

func normalizeMetadataURL(baseURL string) (string, bool) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(baseURL, "#") {
		return "", false
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1"
	}
	u.RawPath = ""
	return u.String(), true
}

// Length framing prevents ambiguous URL/key concatenations, and only the hash
// (not the credential) is retained in the cache identity.
func metadataCacheKey(baseURL, apiKey string) [32]byte {
	h := sha256.New()
	var length [8]byte
	for _, value := range []string{baseURL, apiKey} {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func storeMetadata(key [32]byte, models map[string]ModelMetadata, ttl time.Duration) {
	metadataCache.Lock()
	defer metadataCache.Unlock()
	// Drop expired entries on writes so obsolete credentials do not accumulate.
	now := time.Now()
	for k, entry := range metadataCache.entries {
		if !now.Before(entry.expires) {
			delete(metadataCache.entries, k)
		}
	}
	metadataCache.entries[key] = metadataCacheEntry{models: copyMetadata(models), expires: now.Add(ttl)}
}

func copyMetadata(models map[string]ModelMetadata) map[string]ModelMetadata {
	if models == nil {
		return nil
	}
	result := make(map[string]ModelMetadata, len(models))
	for key, value := range models {
		result[key] = value
	}
	return result
}

func probeModelMetadata(ctx context.Context, client *http.Client, apiKey, endpoint string) (map[string]ModelMetadata, metadataProbeStatus) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, metadataTransient
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, metadataTransient
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, metadataUnauthorized
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, metadataUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, metadataTransient
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, metadataBodyLimit+1))
	if err != nil || len(body) > metadataBodyLimit {
		return nil, metadataTransient
	}
	var response metadataInfoResponse
	if err := json.Unmarshal(body, &response); err != nil {
		// Malformed/truncated JSON can be an outage; a valid but incompatible
		// typed schema is unsupported and gets a short negative cache.
		if json.Valid(body) {
			return nil, metadataUnsupported
		}
		return nil, metadataTransient
	}
	if response.Data == nil {
		return nil, metadataUnsupported
	}
	models := make(map[string]ModelMetadata)
	seen := make(map[string]bool)
	for _, row := range *response.Data {
		if row.ModelName == "" || row.ModelInfo == nil {
			return nil, metadataUnsupported
		}
		if seen[row.ModelName] {
			delete(models, row.ModelName)
			continue
		}
		seen[row.ModelName] = true
		info := row.ModelInfo
		m := ModelMetadata{ID: row.ModelName, Name: row.ModelName}
		if info.MaxInputTokens > 0 {
			m.MaxInputTokens = info.MaxInputTokens
		}
		if info.MaxOutputTokens > 0 {
			m.MaxOutputTokens = info.MaxOutputTokens
		}
		switch {
		case info.ContextLength > 0:
			m.ContextLength = info.ContextLength
		case info.ContextWindow > 0:
			m.ContextLength = info.ContextWindow
		default:
			m.ContextLength = m.MaxInputTokens
		}
		models[row.ModelName] = m
	}
	return models, metadataSuccess
}
