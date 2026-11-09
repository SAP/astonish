package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// Model an external vault upload that completes successfully even if its caller
// is cancelled or loses authority while the upload is in flight.
type mcpBlockingPush struct {
	*productionCapabilityBackend
	entered, release chan struct{}
}

func (b *mcpBlockingPush) PushFile(ctx context.Context, id, path string, r io.Reader, mode os.FileMode) error {
	close(b.entered)
	<-b.release
	return b.productionCapabilityBackend.PushFile(ctx, id, path, r, mode)
}

func TestBotMCPRechecksAfterVaultPush(t *testing.T) {
	for _, discovery := range []bool{true, false} {
		route := "call"
		if discovery {
			route = "discovery"
		}
		t.Run(route, func(t *testing.T) {
			for _, mode := range []string{"paused", "lease-revoked", "lease-expired", "cancelled", "isolation", "isolation-secret", "target-changed", "target-disabled", "approved"} {
				t.Run(mode, func(t *testing.T) {
					f := newBotFactoryFixture(t)
					f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
					backend := &productionCapabilityBackend{}
					client := &productionCapabilityClient{backend: backend}
					blocking := &mcpBlockingPush{productionCapabilityBackend: backend, entered: make(chan struct{}), release: make(chan struct{})}
					exe, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					s := store.MCPServer{Name: "controlled", Command: exe, Args: []string{"-test.run=^TestBotMCPNodeStdioFixture$"}, Env: map[string]string{"ASTONISH_MCP_FIXTURE": "1", "TOKEN": "{{CREDENTIAL:owner:token}}"}}
					name := "mcp-discovery:controlled"
					if !discovery {
						name = "mcp:controlled/read_owner"
						s.CachedTools = json.RawMessage(`[{"name":"read_owner","inputSchema":{"type":"object","properties":{}}}]`)
					}
					servers := newMemMCPStore(s)
					target, ctx := productionFactoryTool(t, f, name, func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
						return BotMCPTools(ctx, productionCapabilityPool{backend: blocking, client: client}, id, servers)
					})
					ctx = approveMCPTestTool(t, target, ctx, map[string]any{})
					bound := target.(*botBoundTool)
					base, cancel := context.WithCancel(ctx)
					defer cancel()
					ctx = botFactoryHandoffContext{base: base, session: ctx.SessionID()}
					type response struct {
						out map[string]any
						err error
					}
					done := make(chan response, 1)
					go func() { out, err := target.Run(ctx, map[string]any{}); done <- response{out, err} }()
					select {
					case <-blocking.entered:
					case <-time.After(5 * time.Second):
						close(blocking.release)
						t.Fatal("vault push not reached")
					}
					// Channel synchronization keeps the fixture mutation race-free.
					switch mode {
					case "paused":
						f.req.Bot.Status = store.BotPaused
					case "lease-revoked":
						f.req.Run.Lease.Fence++
					case "lease-expired":
						f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Minute)
					case "cancelled":
						cancel()
					case "isolation":
						bound.binding.verifyIsolation = func(context.Context) error { return store.ErrBotForbidden }
					case "isolation-secret":
						bound.binding.verifyIsolation = func(context.Context) error {
							return fmt.Errorf("isolation check failed: %s", productionTestSecret)
						}
					case "target-changed":
						s.Env = map[string]string{"TOKEN": "{{CREDENTIAL:other:token}}"}
						if err := servers.Save(context.Background(), &s); err != nil {
							t.Fatal(err)
						}
					case "target-disabled":
						enabled := false
						s.Enabled = &enabled
						if err := servers.Save(context.Background(), &s); err != nil {
							t.Fatal(err)
						}
					}
					close(blocking.release)
					select {
					case result := <-done:
						raw, _ := json.Marshal(result.out)
						wire, _ := json.Marshal(client.args)
						if strings.Contains(string(raw)+string(wire)+fmt.Sprint(result.err), productionTestSecret) {
							t.Fatal("credential leaked in result, error, or MCP request")
						}
						if len(backend.vault) == 0 {
							t.Fatal("test did not finish provisioning")
						}
						if mode == "approved" {
							if result.err != nil || client.calls != 1 {
								t.Fatalf("approved unchanged target failed: %v calls=%d", result.err, client.calls)
							}
							if _, err := target.Run(ctx, map[string]any{}); err == nil || client.calls != 1 {
								t.Fatal("exact grant replay succeeded")
							}
						} else if result.err == nil || client.calls != 0 || backend.starts != 0 {
							t.Fatalf("post-vault fence missed: %v node calls=%d starts=%d", result.err, client.calls, backend.starts)
						}
						rows, err := servers.List(context.Background())
						if err != nil || len(rows) != 1 {
							t.Fatalf("server read: %v", err)
						}
						if discovery && (len(rows[0].CachedTools) > 0) != (mode == "approved") {
							t.Fatal("discovery cache changed without successful authorized discovery")
						}
					case <-time.After(15 * time.Second):
						t.Fatal("dispatch stuck")
					}
				})
			}
		})
	}
}
