package channels

import (
	"sync"

	"google.golang.org/genai"
)

const progressiveExecuteToolName = "execute_tool"

type progressToolIdentity struct {
	id   string
	name string
}

type progressToolTracker struct {
	mu      sync.Mutex
	byID    map[string]progressToolIdentity
	pending []progressToolIdentity
}

func newProgressToolTracker() *progressToolTracker {
	return &progressToolTracker{byID: make(map[string]progressToolIdentity)}
}

func (t *progressToolTracker) call(call *genai.FunctionCall) string {
	identity := progressToolIdentity{id: call.ID, name: effectiveProgressToolName(call.Name, call.Args)}
	if call.Name != progressiveExecuteToolName {
		return identity.name
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if call.ID != "" {
		t.byID[call.ID] = identity
	}
	t.pending = append(t.pending, identity)
	return identity.name
}

func (t *progressToolTracker) response(response *genai.FunctionResponse) string {
	if response.Name != progressiveExecuteToolName {
		return response.Name
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if response.ID != "" {
		if identity, ok := t.byID[response.ID]; ok {
			delete(t.byID, response.ID)
			t.removePending(identity.id)
			return identity.name
		}
	}
	if len(t.pending) > 0 {
		identity := t.pending[0]
		t.pending = t.pending[1:]
		if identity.id != "" {
			delete(t.byID, identity.id)
		}
		return identity.name
	}
	return response.Name
}

func (t *progressToolTracker) removePending(id string) {
	if id == "" {
		return
	}
	for i := range t.pending {
		if t.pending[i].id == id {
			t.pending = append(t.pending[:i], t.pending[i+1:]...)
			return
		}
	}
}

func effectiveProgressToolName(name string, args map[string]any) string {
	if name != progressiveExecuteToolName || args == nil {
		return name
	}
	underlying, ok := args["name"].(string)
	if !ok || underlying == "" {
		return name
	}
	return underlying
}
