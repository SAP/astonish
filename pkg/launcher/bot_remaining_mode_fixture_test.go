package launcher

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/sandbox"
)

// remainingModePool supplies a scoped node transport while retaining the shared
// Chat catalog and progressive dispatcher. It never installs replacement tools.
type remainingModePool struct {
	catalogTestPool
	client sandbox.ToolNodeClient
}

func (*remainingModePool) GetBackend() sandbox.Backend                 { return nil }
func (*remainingModePool) SetSessionScope(string, string, string)      {}
func (p *remainingModePool) GetOrCreate(string) sandbox.ToolNodeClient { return p.client }
func (*remainingModePool) ResolveOwner(id string) string               { return id }

type remainingModeNode struct {
	call func(string, map[string]any) (json.RawMessage, error)
}

func (*remainingModeNode) BindSession(string)       {}
func (*remainingModeNode) EnsureReady(string) error { return nil }
func (n *remainingModeNode) Call(_ string, name string, args map[string]any) (json.RawMessage, error) {
	return n.call(name, args)
}
