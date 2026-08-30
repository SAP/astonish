package api

import (
	"sync"

	"google.golang.org/genai"
)

const deferredExecuteToolName = "execute_tool"

type uiToolIdentity struct {
	id   string
	name string
	args map[string]any
}

type uiToolTracker struct {
	mu      sync.Mutex
	byID    map[string]uiToolIdentity
	pending []uiToolIdentity
}

func newUIToolTracker() *uiToolTracker {
	return &uiToolTracker{byID: make(map[string]uiToolIdentity)}
}

func (t *uiToolTracker) call(call *genai.FunctionCall) uiToolIdentity {
	identity := unwrapUIToolCall(call.Name, call.Args)
	identity.id = call.ID
	if call.Name != deferredExecuteToolName {
		return identity
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if call.ID != "" {
		t.byID[call.ID] = identity
	}
	t.pending = append(t.pending, identity)
	return identity
}

func (t *uiToolTracker) response(response *genai.FunctionResponse) string {
	if response.Name != deferredExecuteToolName {
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

func (t *uiToolTracker) removePending(id string) {
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

func unwrapUIToolCall(name string, args map[string]any) uiToolIdentity {
	identity := uiToolIdentity{name: name, args: args}
	if name != deferredExecuteToolName || args == nil {
		return identity
	}
	underlyingName, ok := args["name"].(string)
	if !ok || underlyingName == "" {
		return identity
	}
	underlyingArgs, _ := args["arguments"].(map[string]any)
	if underlyingArgs == nil {
		underlyingArgs = map[string]any{}
	}
	return uiToolIdentity{name: underlyingName, args: underlyingArgs}
}
