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

const (
	responsesBodyLimit      = 8 << 20
	responsesTimeout        = 3 * time.Second
	responsesSuccessTTL     = time.Hour
	responsesUnsupportedTTL = time.Minute
)

type responsesCacheEntry struct {
	supported bool
	expires   time.Time
}

var responsesCache = struct {
	sync.Mutex
	entries map[[32]byte]responsesCacheEntry
}{entries: make(map[[32]byte]responsesCacheEntry)}

type responsesProbeStatus uint8

const (
	responsesTransient responsesProbeStatus = iota
	responsesUnsupported
	responsesSupported
)

type responsesModels struct {
	Data *[]struct {
		ID string `json:"id"`
	} `json:"data"`
}

// DetectResponsesBaseURL detects the narrowly defined LiteLLM Responses API
// sibling. It fails closed and never uses a generation request as a probe.
func DetectResponsesBaseURL(ctx context.Context, apiKey, configuredBaseURL, modelName string) (string, bool) {
	if ctx.Err() != nil || modelName == "" {
		return "", false
	}
	configured, derived, ok := normalizeResponsesURLs(configuredBaseURL)
	if !ok {
		return "", false
	}
	key := responsesCacheKey(configured, derived, apiKey, modelName)
	now := time.Now()
	responsesCache.Lock()
	entry, found := responsesCache.entries[key]
	if found && !now.Before(entry.expires) {
		delete(responsesCache.entries, key)
		found = false
	}
	responsesCache.Unlock()
	if found {
		if entry.supported {
			return derived, true
		}
		return "", false
	}

	probeCtx, cancel := context.WithTimeout(ctx, responsesTimeout)
	defer cancel()
	client := httpool.Client(0)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status := probeResponsesModels(probeCtx, client, apiKey, derived+"/models", modelName)
	if probeCtx.Err() != nil {
		return "", false
	}
	switch status {
	case responsesSupported:
		storeResponses(key, true, responsesSuccessTTL)
		return derived, true
	case responsesUnsupported:
		storeResponses(key, false, responsesUnsupportedTTL)
	}
	return "", false
}

// normalizeResponsesURLs accepts only a normalized LiteLLM API path and maps
// it to the same-origin OpenAI sibling. Query, fragment, and userinfo are
// rejected rather than silently discarded.
func normalizeResponsesURLs(configured string) (string, string, bool) {
	if configured == "" || strings.Contains(configured, "#") {
		return "", "", false
	}
	u, err := url.Parse(configured)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/litellm") {
		path += "/v1"
	}
	if path != "/litellm/v1" && !strings.HasSuffix(path, "/litellm/v1") {
		return "", "", false
	}
	u.Path = path
	u.RawPath = ""
	configured = u.String()
	u.Path = strings.TrimSuffix(path, "/litellm/v1") + "/openai/v1"
	derived := u.String()
	return configured, derived, true
}

func responsesCacheKey(configured, derived, apiKey, modelName string) [32]byte {
	h := sha256.New()
	var length [8]byte
	for _, value := range []string{configured, derived, apiKey, modelName} {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func storeResponses(key [32]byte, supported bool, ttl time.Duration) {
	responsesCache.Lock()
	defer responsesCache.Unlock()
	now := time.Now()
	for k, entry := range responsesCache.entries {
		if !now.Before(entry.expires) {
			delete(responsesCache.entries, k)
		}
	}
	responsesCache.entries[key] = responsesCacheEntry{supported: supported, expires: now.Add(ttl)}
}

func probeResponsesModels(ctx context.Context, client *http.Client, apiKey, endpoint, modelName string) responsesProbeStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return responsesTransient
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return responsesTransient
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		return responsesUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responsesTransient
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, responsesBodyLimit+1))
	if err != nil || len(body) > responsesBodyLimit {
		return responsesTransient
	}
	var result responsesModels
	if err := json.Unmarshal(body, &result); err != nil {
		return responsesTransient
	}
	if result.Data == nil {
		return responsesTransient
	}
	for _, model := range *result.Data {
		if model.ID == modelName {
			return responsesSupported
		}
	}
	// A valid model list without the configured exact ID is definitive for this
	// cache identity, but never establishes capability.
	return responsesUnsupported
}
