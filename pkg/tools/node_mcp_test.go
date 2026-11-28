package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	astonishmcp "github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
)

func TestNodeMCPDeniedOrigins(t *testing.T) {
	SetAllowPrivateNetworks(true)
	defer SetAllowPrivateNetworks(false)
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer other.Close()
	for _, mode := range []string{"redirect", "sse-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", other.URL)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()
			ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
			_, err := nodeMCP(ctx, astonishmcp.NodeRequest{Action: astonishmcp.NodeDiscover, Config: config.MCPServerConfig{Transport: "sse", URL: srv.URL, Env: map[string]string{"Authorization": "Bearer private-owner-token-12345"}}})
			if err == nil || leaked.Load() != 0 || strings.Contains(err.Error(), "private-owner-token-12345") {
				t.Fatalf("unsafe endpoint: %v", err)
			}
		})
	}
}
func TestNodeMCPRequiresVaultAndPublicNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	request := astonishmcp.NodeRequest{Action: astonishmcp.NodeDiscover, Config: config.MCPServerConfig{Transport: "streamableHTTP", URL: srv.URL}}
	raw, _ := json.Marshal(request)
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	if _, err := ExecuteTool(context.Background(), astonishmcp.NodeOperation, args, ""); err == nil {
		t.Fatal("ownerless node operation")
	}
	ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	if _, err := ExecuteTool(ctx, astonishmcp.NodeOperation, args, ""); err == nil {
		t.Fatal("private network accepted outside node policy")
	}
	if calls.Load() != 0 {
		t.Fatal("denied call dispatched")
	}
}
func TestNodeMCPProxyPreserved(t *testing.T) {
	SetAllowPrivateNetworks(true)
	defer SetAllowPrivateNetworks(false)
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	// Inject only in this test. Production clones (and retains) node DefaultTransport.
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	_, err := nodeMCP(ctx, astonishmcp.NodeRequest{Action: astonishmcp.NodeDiscover, Config: config.MCPServerConfig{Transport: "streamableHTTP", URL: "http://mcp.invalid/mcp"}})
	if err == nil || calls.Load() == 0 {
		t.Fatal("node bypassed denying proxy")
	}
}
