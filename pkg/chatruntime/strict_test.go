package chatruntime

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

var strictStoreError = errors.New("scoped store offline")

type brokenMCP struct {
	store.MCPServerStore
	cache bool
}

func (s brokenMCP) List(context.Context) ([]store.MCPServer, error) {
	if s.cache {
		return []store.MCPServer{{Name: "remote", URL: "https://example.invalid"}}, nil
	}
	return nil, strictStoreError
}
func (s brokenMCP) Get(context.Context, string) (*store.MCPServer, error) {
	return nil, strictStoreError
}

type brokenSkills struct{ store.SkillStore }

func (brokenSkills) LoadAll(context.Context) ([]store.Skill, error) { return nil, strictStoreError }

type brokenSettings struct{ store.SettingsStore }

func (brokenSettings) Get(context.Context) (*store.TeamSettings, error) { return nil, strictStoreError }

type brokenA2A struct{ store.A2AAgentStore }

func (brokenA2A) List(context.Context) ([]store.A2AAgent, error) { return nil, strictStoreError }

func TestStrictScopedStoreErrors(t *testing.T) {
	for name, svc := range map[string]*store.Services{
		"mcp-config": {TeamMCPServers: brokenMCP{}}, "mcp-cache": {TeamMCPServers: brokenMCP{cache: true}},
		"skills": {TeamSkills: brokenSkills{}}, "settings": {Settings: brokenSettings{}}, "a2a": {TeamA2AAgents: brokenA2A{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildScopedChatRuntimeInputs(context.Background(), BuildOptions{Principal: testPrincipal(), Services: svc, ResourceScope: ResourceScopeTeam})
			if !errors.Is(err, strictStoreError) {
				t.Fatalf("strict lost store failure: %v", err)
			}
			_, err = Build(context.Background(), BuildOptions{Principal: testPrincipal(), Services: svc, ResourceScope: ResourceScopeTeam, FallbackPolicy: FallbackAllowed, MCPConfig: &config.MCPConfig{}})
			if err != nil {
				t.Fatalf("legacy rejected permissive fallback: %v", err)
			}
		})
	}
}
func TestStrictInheritedPersonalCredentialsUnavailableToTeam(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent, _ = execution.WithPrincipal(parent, testPrincipal())
	identity := execution.BotRunIdentity{ActorID: "bot-owner", Version: 3}
	parent = execution.WithBotRunIdentity(parent, identity)
	personal := &privateCredential{}
	parent = store.WithCredentialStore(parent, personal)
	parent = store.WithSessionService(parent, &privateSessions{})
	parent = store.WithServices(parent, &store.Services{PersonalCredentials: personal})
	parent = agent.WithRequestMCPGroups(parent, map[string]*agent.ToolGroup{"private": {Name: "private"}})
	parent = agent.WithPromptOverrides(parent, &agent.PromptOverrides{SkillIndex: "private"})
	in, err := Build(parent, BuildOptions{Principal: testPrincipal(), Services: &store.Services{}, ResourceScope: ResourceScopeTeam})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := in.Bind(parent)
	if err != nil {
		t.Fatal(err)
	}
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil || cs == personal {
		t.Fatal("nil fallback or inherited personal credential store")
	}
	if cs.Get(ctx, "private") != nil {
		t.Fatal("personal credential leaked")
	}
	if _, _, err := credentials.ResolverFromContext(ctx).Resolve("private"); err == nil {
		t.Fatal("missing credentials did not fail closed")
	}
	if len(agent.RequestMCPGroupsFromContext(ctx)) != 0 {
		t.Fatal("private catalog inherited")
	}
	if agent.PromptOverridesFromContext(ctx).SkillIndex == "private" {
		t.Fatal("private skill index inherited")
	}
	if store.FromContext(ctx).PersonalCredentials != nil {
		t.Fatal("private services inherited")
	}
	if p, ok := execution.PrincipalFromContext(ctx); !ok || p.Subject != testPrincipal().Subject {
		t.Fatal("principal lost")
	}
	if got, ok := execution.BotRunIdentityFromContext(ctx); !ok || got != identity {
		t.Fatal("Bot identity lost")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("cancellation detached")
	}
}

type privateCredential struct{ store.CredentialStore }

func (*privateCredential) Get(context.Context, string) *store.Credential { return &store.Credential{} }
func TestStrictCachedToolsParser(t *testing.T) {
	if _, err := ParseCachedToolsJSONBytes([]byte("bad")); err == nil {
		t.Fatal("malformed cache accepted")
	}
	entries, err := ParseCachedToolsJSONBytes([]byte(`[{"name":"test","inputSchema":{"type":"object"}}]`))
	if err != nil || len(entries) != 1 || entries[0].Name != "test" {
		t.Fatalf("%v %v", entries, err)
	}
}
