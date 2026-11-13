package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"net"
	"net/url"
)

// BotMandatedReadTools installs only concrete retrieval adapters. The origin
// checker is trusted current tenant policy; the mandate independently pins every
// origin and tool. No arbitrary named tool can acquire read classification.
func BotMandatedReadTools(m *bot.MandateExecution, network tools.RoutineReadPolicy) ([]tool.Tool, error) {
	if m == nil || network.Authorize == nil {
		return nil, store.ErrBotForbidden
	}
	policy := tools.RoutineReadPolicy{AllowIP: func(ctx context.Context, address string, ip net.IP) error {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			pinned := false
			for _, c := range m.Authorization.Snapshot().Constraints {
				for _, r := range c.Resources {
					u, e := url.Parse(r)
					if e == nil && c.Action == execution.CapabilityToolExecute && u.Host == address {
						pinned = true
					}
				}
			}
			if !pinned {
				return store.ErrBotForbidden
			}
		}
		if network.AllowIP != nil {
			return network.AllowIP(ctx, address, ip)
		}
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return store.ErrBotForbidden
		}
		return nil
	}, Authorize: func(ctx context.Context, origin string) error {
		if err := m.Check(ctx); err != nil {
			return err
		}
		if err := m.Authorization.Authorize(execution.CapabilityToolExecute, origin); err != nil {
			if m.Authorization.Authorize(execution.CapabilityToolExecute, BotPublicReadResource) != nil {
				return err
			}
			u, e := url.Parse(origin)
			if e != nil {
				return e
			}
			ips, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
			if e != nil {
				return e
			}
			for _, ip := range ips {
				if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
					return store.ErrBotForbidden
				}
			}
		}
		return network.Authorize(ctx, origin)
	}}
	var result []tool.Tool
	web, err := functiontool.New(functiontool.Config{Name: "web_fetch", Description: "Retrieve an unauthenticated page within the saved mandate network policy"}, func(c tool.Context, a tools.WebFetchArgs) (tools.WebFetchResult, error) {
		return tools.WebFetch(botBoundToolContext{Context: c, owner: tools.WithRoutineReadPolicy(c, policy)}, a)
	})
	if err != nil {
		return nil, err
	}
	httpTool, err := functiontool.New(functiontool.Config{Name: "http_request", Description: "Retrieve with GET or HEAD only; credentials, custom headers and bodies are forbidden"}, func(c tool.Context, a tools.HttpRequestArgs) (tools.HttpRequestResult, error) {
		return tools.HttpRequest(botBoundToolContext{Context: c, owner: tools.WithRoutineReadPolicy(c, policy)}, a)
	})
	if err != nil {
		return nil, err
	}
	for _, t := range []tool.Tool{web, httpTool} {
		if m.Authorization.Authorize(execution.CapabilityToolExecute, t.Name()) == nil {
			result = append(result, &botMandatedReadTool{botRunnableTool: t.(botRunnableTool), policy: policy})
		}
	}
	return result, nil
}

type botMandatedReadTool struct {
	botRunnableTool
	policy tools.RoutineReadPolicy
}

func (t *botMandatedReadTool) Run(c tool.Context, a any) (map[string]any, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if _, err = t.describe(c, raw); err != nil {
		return nil, err
	}
	result, err := t.botRunnableTool.Run(c, a)
	if err == nil {
		var args map[string]any
		_ = json.Unmarshal(raw, &args)
		recordBuiltinToolObservationWithCapture(c, c.SessionID(), t.Name(), args, mandatedReadObservation(t.Name(), result), result)
	}
	return result, err
}

// ADK emits a flat map when schema conversion succeeds and a typed envelope
// otherwise. Unwrap only concrete results from this pinned adapter, never maps
// or strings claiming to carry verification metadata from providers or MCP.
func mandatedReadObservation(name string, result map[string]any) map[string]any {
	if name != "web_fetch" && name != "http_request" {
		return nil
	}
	if result["error"] != nil {
		return nil
	}
	typed, wrapped := result["result"]
	if !wrapped {
		return result
	}
	switch value := typed.(type) {
	case tools.WebFetchResult:
		if name != "web_fetch" {
			return nil
		}
		typed = value
	case tools.HttpRequestResult:
		if name != "http_request" {
			return nil
		}
		typed = value
	default:
		return nil
	}
	var observation map[string]any
	if encoded, err := json.Marshal(typed); err == nil {
		_ = json.Unmarshal(encoded, &observation)
	}
	return observation
}

func (t *botMandatedReadTool) describe(c context.Context, raw json.RawMessage) (execution.ActionDescriptor, error) {
	var a tools.HttpRequestArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return execution.ActionDescriptor{}, err
	}
	if t.Name() == "web_fetch" {
		var values map[string]any
		_ = json.Unmarshal(raw, &values)
		for k := range values {
			if k != "url" && k != "mode" && k != "max_chars" {
				return execution.ActionDescriptor{}, fmt.Errorf("unexpected web fetch argument")
			}
		}
	}
	if err := tools.ValidateRoutineRead(a); err != nil {
		return execution.ActionDescriptor{}, err
	}
	origin, err := tools.RoutineReadOrigin(a.URL)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	if err = t.policy.Authorize(c, origin); err != nil {
		return execution.ActionDescriptor{}, err
	}
	return execution.ActionDescriptor{Tool: t.Name(), Arguments: raw, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type mandatedReadClassifier struct {
	base     agent.BotActionClassifier
	adapters map[string]interface {
		describe(context.Context, json.RawMessage) (execution.ActionDescriptor, error)
	}
}

func (c mandatedReadClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	if t := c.adapters[name]; t != nil {
		return t.describe(ctx, raw)
	}
	return c.base.DescribeAction(ctx, name, raw)
}
func WithBotMandatedReadClassifier(base agent.BotActionClassifier, catalog []tool.Tool) agent.BotActionClassifier {
	c := mandatedReadClassifier{base: base, adapters: map[string]interface {
		describe(context.Context, json.RawMessage) (execution.ActionDescriptor, error)
	}{}}
	seen := map[string]bool{}
	for _, t := range catalog {
		if t == nil {
			continue
		}
		if seen[t.Name()] {
			delete(c.adapters, t.Name())
			continue
		}
		seen[t.Name()] = true
		switch read := t.(type) {
		case *botMandatedReadTool:
			c.adapters[t.Name()] = read
		case *botMandatedSearchTool:
			c.adapters[t.Name()] = read
		}
	}
	return c
}
func botMandateScopedTools(m *bot.MandateExecution, catalog []tool.Tool) []tool.Tool {
	var allowed []tool.Tool
	for _, t := range catalog {
		if t != nil && m.Authorization.Authorize(execution.CapabilityToolExecute, t.Name()) == nil {
			allowed = append(allowed, t)
		}
	}
	return allowed
}
