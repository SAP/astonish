package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// The transport and exact-action decision are controlled, not the production
// Employee/WorkerFactory, scoped MCP loader, index, bridge or HTTP adapter.
type privateMCPHTTPGuard struct {
	request bot.RunRequest
	deny    bool
	calls   *int
}

func (g privateMCPHTTPGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	*g.calls++
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	principal, authenticated := execution.PrincipalFromContext(ctx)
	if !ok || !authenticated || identity.BotID != g.request.Bot.ID || identity.RunID != g.request.Run.ID || identity.ActorID != g.request.Principal.Subject || principal.Subject != g.request.Principal.Subject {
		return execution.PolicyDecision{}, fmt.Errorf("private MCP dispatch lost owner/run identity: %+v %+v", identity, principal)
	}
	if action.Tool != "mcp:cached-private/lookup_signal" {
		return execution.PolicyDecision{}, fmt.Errorf("private MCP dispatch lost qualified wrapper identity: %q", action.Tool)
	}
	if err := g.request.Authorization.AuthorizeTool(ctx, action.Tool); err != nil {
		return execution.PolicyDecision{}, err
	}
	if g.deny {
		return execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: "controlled exact-action denial"}, store.ErrBotForbidden
	}
	digest, err := action.Digest()
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, nil
}

func TestEmployeePrivateConfiguredMCPHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/denied=%t", scope, denied), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
				f.worker.Stop()
				org, err := f.db.ForOrg("interactive")
				if err != nil {
					t.Fatal(err)
				}
				server := &store.MCPServer{Name: "cached-private", Transport: "streamableHTTP", URL: "https://mcp.example.test/", CachedTools: json.RawMessage(`[{"name":"lookup_signal","description":"Look up the violet signal","inputSchema":{"type":"object","properties":{"signal":{"type":"string"}},"required":["signal"]}}]`)}
				if err := org.OrgMCPServers().Save(f.ctx, server); err != nil {
					t.Fatal(err)
				}
				effects, guards, workerStep, employeeStep := 0, 0, 0, 0
				executed := false
				searched, described, received := false, false, false
				f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
					if name != mcp.NodeOperation {
						return nil, fmt.Errorf("unexpected transport %s", name)
					}
					if args["tool"] != "lookup_signal" {
						t.Fatalf("wrong MCP target: %+v", args)
					}
					if effects != 0 || guards == 0 {
						t.Fatal("replayed or unguarded MCP effect")
					}
					effects++
					return json.RawMessage(`{"content":"PRIVATE_MCP_VIOLET"}`), nil
				}
				assessment := f.model.assessment
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					prompt := ""
					if req.Config != nil && req.Config.SystemInstruction != nil {
						for _, p := range req.Config.SystemInstruction.Parts {
							prompt += p.Text
						}
					}
					if strings.HasPrefix(prompt, "BOT_") {
						return assessment(req)
					}
					// This multi-term query is neither a substring of the cached name or
					// description nor a server reference. Request-MCP fallback matching
					// cannot satisfy it: the real private worker's index must contain it.
					private := strings.Contains(prompt, "You are a private tool-enabled Chat worker")
					for _, c := range req.Contents {
						for _, p := range c.Parts {
							if p.FunctionResponse != nil {
								raw, _ := json.Marshal(p.FunctionResponse.Response)
								if private && p.FunctionResponse.Name == "execute_tool" {
									executed = strings.Contains(string(raw), "PRIVATE_MCP_VIOLET")
								}
								if private && p.FunctionResponse.Name == "search_tools" {
									var catalog struct {
										Matches []struct {
											ToolName    string `json:"tool_name"`
											GroupName   string `json:"group_name"`
											Description string `json:"description"`
										} `json:"matches"`
									}
									if err := json.Unmarshal(raw, &catalog); err != nil {
										t.Fatal(err)
									}
									for _, match := range catalog.Matches {
										if match.ToolName == "ask_user" || match.ToolName == "employee_chat" {
											t.Fatalf("prohibited cached group tool indexed in private worker: %+v", match)
										}
										if match.GroupName == "mcp:cached-private" {
											if match.ToolName != "lookup_signal" || match.Description != "Look up the violet signal" {
												t.Fatalf("wrong private indexed MCP inventory: %+v", match)
											}
											searched = true
										}
									}
								}
								if private && p.FunctionResponse.Name == "describe_tools" {
									described = strings.Contains(string(raw), "mcp:cached-private/lookup_signal") && strings.Contains(string(raw), `"group_name":"mcp:cached-private"`) && strings.Contains(string(raw), `"required":["signal"]`)
								}
								if !private && p.FunctionResponse.Name == "employee_chat" {
									received = strings.Contains(string(raw), "PRIVATE_WORKER_SETTLED")
								}
							}
						}
					}
					part := &genai.Part{Text: "Observed scoped result."}
					call := func(id, name string, args map[string]any) {
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}
					}
					if private {
						workerStep++
						switch workerStep {
						case 1:
							call("cached-search", "search_tools", map[string]any{"query": "violet signal lookup forbidden cached worker question recursive", "max_results": 100})
						case 2:
							if !searched {
								t.Fatal("configured lazy MCP missing from private search")
							}
							call("cached-describe", "describe_tools", map[string]any{"names": []string{"mcp:cached-private/lookup_signal"}})
						case 3:
							if !described {
								t.Fatal("configured lazy MCP missing from private describe")
							}
							call("cached-execute", "execute_tool", map[string]any{"name": "mcp:cached-private/lookup_signal", "arguments": map[string]any{"signal": "violet"}})
						default:
							part.Text = "PRIVATE_WORKER_SETTLED"
						}
					} else {
						employeeStep++
						if employeeStep == 1 {
							call("employee-dispatch", "employee_chat", map[string]any{"message": "Look up violet using configured MCP"})
						}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, true
				}
				cfg := f.workerConfig
				prepare := cfg.Prepare
				cfg.Prepare = func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					safe, err := prepare(c, q, s)
					if err != nil {
						return nil, err
					}
					safe.PolicyRuntime.Guard = privateMCPHTTPGuard{request: q, deny: denied, calls: &guards}
					return safe, nil
				}
				f.worker, err = daemon.NewBotWorker(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer f.worker.Stop()
				run := f.turn()
				want := 1
				if denied {
					want = 0
				}
				if executed == denied {
					t.Fatalf("MCP result did not match authorization: executed=%t denied=%t", executed, denied)
				}
				if !searched || !described || effects != want || guards == 0 || !received {
					t.Fatalf("lost private MCP sequence: search=%t describe=%t effects=%d guards=%d received=%t run=%+v", searched, described, effects, guards, received, run)
				}
				home := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				if strings.Contains(home, "PRIVATE_MCP_VIOLET") || strings.Contains(home, "PRIVATE_WORKER_SETTLED") || strings.Contains(home, "lookup_signal") {
					t.Fatal("private MCP trace leaked into Home")
				}
			})
		}
	}
}
