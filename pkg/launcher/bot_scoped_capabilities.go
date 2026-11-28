package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// BotBrowserTools uses Chat's browser tools and backend CDP wiring, but creates a
// private manager and pins every call (including delegated calls) to this run.
// Unsupported backend kinds return an error, never a host browser fallback.
func BotBrowserTools(backend sandbox.Backend, sessionID string) ([]tool.Tool, func(), error) {
	if backend == nil || sessionID == "" {
		return nil, nil, store.ErrBotUnsupported
	}
	mgr := browser.NewManager(browser.BrowserConfig{})
	if !sandbox.WireBackendBrowserManager(mgr, backend, nil, nil, nil) {
		return nil, nil, store.ErrBotUnsupported
	}
	mgr.SetSessionID(sessionID)
	all, err := tools.GetBrowserToolsForSandbox(mgr)
	cleanup := func() { mgr.Cleanup() }
	if err != nil {
		return nil, cleanup, err
	}
	result := make([]tool.Tool, 0, len(all))
	for _, t := range all {
		// Only reviewed run-local implementations may replace catalogue entries.
		if t.Name() == "browser_pdf" {
			t, err = newBotBrowserPDFTool(mgr, backend, sessionID)
			if err != nil {
				return nil, cleanup, err
			}
		}
		switch t.Name() {
		case "browser_navigate", "browser_navigate_back", "browser_click", "browser_type", "browser_hover", "browser_drag", "browser_press_key", "browser_select_option", "browser_fill_form", "browser_snapshot", "browser_take_screenshot", "browser_console_messages", "browser_network_requests", "browser_tabs", "browser_close", "browser_resize", "browser_wait_for", "browser_file_upload", "browser_handle_dialog", "browser_evaluate", "browser_cookies", "browser_storage", "browser_pdf":
		default:
			if !botBrowserExtra(t.Name()) && botBrowserUnsupported(t.Name()) == "" {
				continue
			}
		}
		runnable, ok := t.(botRunnableTool)
		if !ok {
			return nil, cleanup, store.ErrBotUnsupported
		}
		if reason := botBrowserUnsupported(t.Name()); reason != "" {
			runnable = &botUnsupportedBrowserTool{botRunnableTool: runnable, reason: reason}
		}
		result = append(result, &botPinnedSessionTool{botRunnableTool: runnable, sessionID: sessionID, browserManager: mgr})
	}
	return result, cleanup, nil
}

type botPinnedSessionTool struct {
	botRunnableTool
	sessionID      string
	browserManager *browser.Manager
}
type botPinnedSessionContext struct {
	tool.Context
	sessionID string
}

func (c botPinnedSessionContext) SessionID() string { return c.sessionID }
func (t *botPinnedSessionTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pinned := botPinnedSessionContext{Context: ctx, sessionID: t.sessionID}
	if t.browserManager != nil {
		t.browserManager.SetRequestContext(pinned)
	}
	return t.botRunnableTool.Run(pinned, args)
}

// BotSearchFlowsTool invokes the real Chat flow search against owner-injected
// stores, refusing the ordinary Chat global filesystem registry fallback.
func BotSearchFlowsTool() (tool.Tool, error) {
	t, err := tools.NewSearchFlowsTool()
	if err != nil {
		return nil, err
	}
	return &botScopedFlowSearch{botRunnableTool: t.(botRunnableTool)}, nil
}

type botScopedFlowSearch struct{ botRunnableTool }

func (t *botScopedFlowSearch) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	if store.FlowStoreFromContext(ctx) == nil && store.TeamFlowStoreFromContext(ctx) == nil {
		return nil, store.ErrBotUnsupported
	}
	return t.botRunnableTool.Run(ctx, args)
}

var _ context.Context = botPinnedSessionContext{}
