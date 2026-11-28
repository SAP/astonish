package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	astonishmcp "github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// nodeMCP performs a single discovery or call in the sandbox. Connections are
// deliberately not reused across operations: revoked credentials/configuration
// cannot survive vault replacement. A failed call is never retried/replayed.
func nodeMCP(ctx context.Context, args astonishmcp.NodeRequest) (out map[string]any, err error) {
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		return nil, fmt.Errorf("owner MCP credential vault required")
	}
	if args.Action != astonishmcp.NodeDiscover && args.Action != astonishmcp.NodeCall {
		return nil, fmt.Errorf("invalid MCP operation")
	}
	if !args.Config.IsEnabled() {
		return nil, fmt.Errorf("MCP server disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(cs)
	defer func() {
		if err != nil {
			err = fmt.Errorf("MCP node: %s", redactor.Redact(err.Error()))
		}
		if out != nil {
			out = redactor.RedactMap(out)
		}
	}()
	resolver := httpCredentialResolver{CredentialResolver: credentials.NewStoreAdapter(cs), redactor: redactor, ctx: ctx, store: cs}
	cfg, err := astonishmcp.ResolveMCPServerConfig(args.Config, resolver)
	if err != nil {
		return nil, err
	}
	// Static values in owner configuration also must not escape in diagnostics.
	for k, v := range cfg.Env {
		registerHTTPAuthSecret(redactor, "mcp/"+k, v)
	}
	var client *http.Client
	if cfg.Transport == "sse" || cfg.Transport == "streamableHTTP" {
		origin, e := url.Parse(cfg.URL)
		if e != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
			return nil, fmt.Errorf("invalid remote MCP endpoint")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// Keep the sandbox proxy. Outside the explicit node network policy mode,
		// use the same public-IP-only dial restriction as http_request.
		if !httpReqAllowPrivateNetworks {
			transport.Proxy = nil
			transport.DialContext = httpRequestPublicDial
		}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: &nodeMCPRoundTripper{base: transport, origin: origin, headers: cfg.Env}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("MCP redirects blocked") }}
	}
	transport, _, err := astonishmcp.NewNodeTransport(cfg, client)
	if err != nil {
		return nil, err
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "astonish-node", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var result any
	switch args.Action {
	case astonishmcp.NodeDiscover:
		// Preserve the server's exact JSON Schema (including refs, unions and enums).
		all := []*sdk.Tool{}
		cursor := ""
		seen := map[string]bool{}
		for {
			page, e := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
			if e != nil {
				return nil, e
			}
			all = append(all, page.Tools...)
			if len(all) > 10000 {
				return nil, fmt.Errorf("MCP discovery exceeds tool limit")
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, fmt.Errorf("MCP discovery repeated cursor")
			}
			seen[cursor] = true
		}
		result = map[string]any{"tools": all}
	case astonishmcp.NodeCall:
		if args.Tool == "" {
			return nil, fmt.Errorf("MCP tool required")
		}
		result, err = session.CallTool(ctx, &sdk.CallToolParams{Name: args.Tool, Arguments: args.Arguments})
		if err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > 8*1024*1024 {
		return nil, fmt.Errorf("MCP response exceeds node limit")
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

type nodeMCPRoundTripper struct {
	base    http.RoundTripper
	origin  *url.URL
	headers map[string]string
}

func (t *nodeMCPRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// SSE's endpoint event can supply a different URL without an HTTP redirect.
	// Gate every request BEFORE attaching any custom owner authentication headers.
	if r.URL.Scheme != t.origin.Scheme || !strings.EqualFold(r.URL.Host, t.origin.Host) || r.URL.User != nil {
		return nil, fmt.Errorf("cross-origin MCP request blocked")
	}
	clone := r.Clone(r.Context())
	for k, v := range t.headers {
		switch strings.ToLower(k) {
		case "host", "proxy-authorization", "proxy-connection", "connection", "transfer-encoding", "content-length":
			return nil, fmt.Errorf("unsafe MCP header")
		}
		clone.Header.Set(k, v)
	}
	return t.base.RoundTrip(clone)
}
