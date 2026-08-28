package sandbox

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

type warmSpyClient struct {
	mu      sync.Mutex
	bound   bool
	ready   bool
	allowed []NetworkAllowEndpoint
	readyCh chan struct{}
}

func (c *warmSpyClient) BindSession(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bound = true
}

func (c *warmSpyClient) EnsureReady(string) error {
	c.mu.Lock()
	c.ready = true
	ch := c.readyCh
	c.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (c *warmSpyClient) Call(string, string, map[string]interface{}) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func (c *warmSpyClient) SetNetworkAllowEndpoints(endpoints []NetworkAllowEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowed = append([]NetworkAllowEndpoint(nil), endpoints...)
}

type warmSpyPool struct {
	client *warmSpyClient
}

func (s *warmSpyPool) GetOrCreate(string) ToolNodeClient                     { return s.client }
func (s *warmSpyPool) GetOrCreateWithTemplate(string, string) ToolNodeClient { return s.client }
func (s *warmSpyPool) GetOrCreateWithChain(string, string, []string) ToolNodeClient {
	return s.client
}
func (s *warmSpyPool) GetOrCreateWithImage(string, string, []string, string) ToolNodeClient {
	return s.client
}
func (s *warmSpyPool) Cleanup()                       {}
func (s *warmSpyPool) GetBackend() Backend            { return nil }
func (s *warmSpyPool) Alias(_, _ string)              {}
func (s *warmSpyPool) Remove(_ string)                {}
func (s *warmSpyPool) SetSessionScope(_, _, _ string) {}

func TestWarmFlowSession_BindsAndEnsuresReady(t *testing.T) {
	prev := NetworkPolicyPreSeeder
	t.Cleanup(func() { NetworkPolicyPreSeeder = prev })

	var (
		mu             sync.Mutex
		preSeedSession string
	)
	NetworkPolicyPreSeeder = func(ctx context.Context, sessionID string) {
		mu.Lock()
		defer mu.Unlock()
		preSeedSession = sessionID
	}

	client := &warmSpyClient{readyCh: make(chan struct{})}
	nt := NewNodeToolWithPool(testTool{name: "shell_command"}, &warmSpyPool{client: client})
	teamStore := &testNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "**.cloud.sap",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	ctx := store.WithNetworkPolicyStores(context.Background(), &store.NetworkPolicyStores{Team: teamStore})

	WarmFlowSession(ctx, []tool.Tool{nt}, "flow-sess-warm")

	client.mu.Lock()
	bound, allowed := client.bound, append([]NetworkAllowEndpoint(nil), client.allowed...)
	client.mu.Unlock()
	if !bound {
		t.Fatal("expected BindSession during WarmFlowSession")
	}
	if len(allowed) != 1 || allowed[0].Host != "**.cloud.sap" {
		t.Fatalf("expected network allow endpoints before bind, got %+v", allowed)
	}

	select {
	case <-client.readyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background EnsureReady")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seeded := preSeedSession
		mu.Unlock()
		if seeded == "flow-sess-warm" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected background PreSeed for flow-sess-warm")
}

func TestWarmFlowSession_NoopWithoutNodeTool(t *testing.T) {
	WarmFlowSession(context.Background(), nil, "sess")
	WarmFlowSession(context.Background(), []tool.Tool{testTool{name: "memory_search"}}, "sess")
	WarmFlowSession(context.Background(), []tool.Tool{
		NewNodeToolWithPool(testTool{name: "shell_command"}, &warmSpyPool{client: &warmSpyClient{}}),
	}, "")
}
