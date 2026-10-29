package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/genai"
)

const (
	// Discovery is useful only as a short bridge to a concrete capability. A
	// larger budget can resend an increasingly expensive transcript without
	// making progress, as every discovery result is added to model context.
	maxDiscoveryCallsPerTurn   = 12
	maxIdenticalDiscoveryCalls = 2
)

var errToolDiscoveryLoop = errors.New("tool discovery loop detected")

var discoveryToolNames = map[string]struct{}{
	"search_tools":   {},
	"describe_tools": {},
	"skill_lookup":   {},
}

type toolDiscoveryGuard struct {
	mu                 sync.Mutex
	total              int
	identical          map[string]int
	historyInitialized bool
	historyBaseline    map[string]int
}

func newToolDiscoveryGuard() *toolDiscoveryGuard {
	return &toolDiscoveryGuard{
		identical:       make(map[string]int),
		historyBaseline: make(map[string]int),
	}
}

func (g *toolDiscoveryGuard) record(name string, args map[string]any) error {
	if _, discovery := discoveryToolNames[name]; !discovery {
		return nil
	}

	fingerprint := discoveryCallFingerprint(name, args)
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.total >= maxDiscoveryCallsPerTurn {
		return fmt.Errorf("%w: exceeded %d discovery calls in one turn; stop discovering tools and answer with the capabilities already found", errToolDiscoveryLoop, maxDiscoveryCallsPerTurn)
	}
	if g.identical[fingerprint] >= maxIdenticalDiscoveryCalls {
		return fmt.Errorf("%w: repeated identical %s call more than %d times; stop retrying this discovery call", errToolDiscoveryLoop, name, maxIdenticalDiscoveryCalls)
	}

	g.total++
	g.identical[fingerprint]++
	return nil
}

func (g *toolDiscoveryGuard) validateHistory(contents []*genai.Content) error {
	counts := discoveryHistoryCounts(contents)
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.historyInitialized {
		for fingerprint, count := range counts {
			g.historyBaseline[fingerprint] = count
		}
		g.historyInitialized = true
		return nil
	}

	total := 0
	for fingerprint, count := range counts {
		turnCount := count - g.historyBaseline[fingerprint]
		if turnCount <= 0 {
			continue
		}
		total += turnCount
		if turnCount >= maxIdenticalDiscoveryCalls {
			return fmt.Errorf("%w: an identical discovery call occurred %d times in this turn; stop retrying discovery and answer with the capabilities already found", errToolDiscoveryLoop, turnCount)
		}
	}
	if total >= maxDiscoveryCallsPerTurn {
		return fmt.Errorf("%w: reached %d discovery calls in one turn; stop discovering tools and answer with the capabilities already found", errToolDiscoveryLoop, total)
	}
	return nil
}

func discoveryHistoryCounts(contents []*genai.Content) map[string]int {
	counts := make(map[string]int)
	for _, content := range contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.FunctionCall == nil {
				continue
			}
			originalName := part.FunctionCall.Name
			name, args := effectiveDiscoveryCall(originalName, part.FunctionCall.Args)
			if _, discovery := discoveryToolNames[name]; !discovery {
				continue
			}
			// A failed direct invocation followed by the documented deferred
			// execute_tool bridge is one recovery attempt, not two identical
			// discovery calls. Keep the transport in history fingerprints while
			// still counting both calls toward the overall discovery budget.
			if originalName == executeToolName {
				name = executeToolName + ":" + name
			}
			counts[discoveryCallFingerprint(name, args)]++
		}
	}
	return counts
}

func effectiveDiscoveryCall(name string, args map[string]any) (string, map[string]any) {
	if name != executeToolName {
		return name, args
	}
	underlying, _ := args["name"].(string)
	underlyingArgs, _ := args["arguments"].(map[string]any)
	return underlying, underlyingArgs
}

func discoveryCallFingerprint(name string, args map[string]any) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%#v", args))
	}
	sum := sha256.Sum256(append([]byte(name+"\x00"), encoded...))
	return hex.EncodeToString(sum[:])
}
