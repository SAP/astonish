package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"strings"
)

// Applied to the logical executable (not execute_tool/delegate/drill envelope).
// Every executable view retains this binding; children cannot unlock a parent.
func (b *botFactoryBinding) checkResearchDetail(ctx context.Context, session, name string, args any) error {
	if b.research == nil {
		return nil
	}
	if name != "web_fetch" && name != "http_request" && !strings.HasPrefix(name, "browser_") && !strings.HasPrefix(name, "mcp:") {
		return nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var v struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return store.ErrBotInvalid
	}
	return b.research.AllowDetail(ctx, b.req.Run.ID, session, v.URL)
}
