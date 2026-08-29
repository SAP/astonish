package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type requestFingerprints struct {
	system string
	tools  string
	count  int
}

type requestFingerprintTracker struct {
	mu       sync.Mutex
	previous requestFingerprints
	round    int
}

var sessionRequestFingerprints sync.Map // map[string]requestFingerprints

func (t *requestFingerprintTracker) callback(sessionID string, cacheStable bool) llmagent.BeforeModelCallback {
	return func(_ agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		current := fingerprintRequest(req)
		t.mu.Lock()
		t.round++
		round := t.round
		previous := t.previous
		t.previous = current
		t.mu.Unlock()
		previousSession, _ := sessionRequestFingerprints.Load(sessionID)
		sessionPrevious, _ := previousSession.(requestFingerprints)
		sessionRequestFingerprints.Store(sessionID, current)
		slog.Debug("model request fingerprints",
			"component", "chat-cache",
			"session", sessionID,
			"round", round,
			"cache_stable_path", cacheStable,
			"system_hash", current.system,
			"system_changed", previous.system != "" && previous.system != current.system,
			"system_changed_session", sessionPrevious.system != "" && sessionPrevious.system != current.system,
			"tool_hash", current.tools,
			"tool_count", current.count,
			"tools_changed", previous.tools != "" && previous.tools != current.tools,
			"tools_changed_session", sessionPrevious.tools != "" && sessionPrevious.tools != current.tools)
		return nil, nil
	}
}

func fingerprintRequest(req *model.LLMRequest) requestFingerprints {
	if req == nil {
		return requestFingerprints{system: hashBytes(nil), tools: hashBytes(nil)}
	}
	var system any
	var declarations []*genai.FunctionDeclaration
	if req.Config != nil {
		system = req.Config.SystemInstruction
		for _, packed := range req.Config.Tools {
			if packed != nil {
				declarations = append(declarations, packed.FunctionDeclarations...)
			}
		}
	}
	sort.SliceStable(declarations, func(i, j int) bool {
		if declarations[i] == nil {
			return true
		}
		if declarations[j] == nil {
			return false
		}
		return declarations[i].Name < declarations[j].Name
	})
	return requestFingerprints{
		system: hashJSON(system),
		tools:  hashJSON(declarations),
		count:  len(declarations),
	}
}

func hashJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return hashBytes(nil)
	}
	return hashBytes(data)
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
