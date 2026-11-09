package tools

import (
	"context"
	"github.com/SAP/astonish/pkg/email"
	"google.golang.org/adk/tool"
	"testing"
)

func TestEmailDeclarationOnlyFailsClosed(t *testing.T) {
	ts, err := GetEmailToolsForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 8 {
		t.Fatalf("got %d tools", len(ts))
	}
	for _, target := range ts {
		if _, err := target.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}).Run(nil, map[string]any{}); err == nil {
			t.Fatalf("%s accepted missing transport", target.Name())
		}
	}
}
func TestScopedChatEmailNodeStillRequiresVault(t *testing.T) {
	old := httpReqAllowPrivateNetworks
	httpReqAllowPrivateNetworks = true
	t.Cleanup(func() { httpReqAllowPrivateNetworks = old })
	_, err := nodeEmail(context.Background(), email.NodeRequest{Tool: ScopedChatEmailPrefix + "email_reply"})
	if err == nil {
		t.Fatal("interactive protocol bypassed scoped vault")
	}
}
