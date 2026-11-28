package api

import (
	"context"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestScopedChatRuntimeChannelFailureIsExplicit(t *testing.T) {
	p := execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "owner", Authenticated: true}
	ctx, err := WithScopedChannelRuntimeContext(context.Background(), chatruntime.BuildOptions{Principal: p, ResourceScope: chatruntime.ResourceScopeTeam})
	if err == nil || ctx != nil {
		t.Fatal("strict channel retained original context on failure")
	}
	ctx, err = WithScopedChannelRuntimeContext(context.Background(), chatruntime.BuildOptions{Principal: p, ResourceScope: chatruntime.ResourceScopeTeam, Services: &store.Services{}})
	if err != nil || store.FromContext(ctx) == nil {
		t.Fatalf("scoped channel failed: %v", err)
	}
}
func TestScopedChatRuntimeParserWrapper(t *testing.T) {
	if got := parseCachedToolsJSONBytes([]byte(`[{"name":"scoped"}]`)); len(got) != 1 || got[0].Name != "scoped" {
		t.Fatal(got)
	}
	if got := parseCachedToolsJSONBytes([]byte(`invalid`)); got != nil {
		t.Fatal(got)
	}
}
