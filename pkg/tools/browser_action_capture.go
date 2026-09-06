package tools

import (
	"encoding/json"

	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// --- browser_start_action_capture ---

type BrowserStartActionCaptureArgs struct{}

type BrowserStartActionCaptureResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func BrowserStartActionCapture(mgr *browser.Manager) func(tool.Context, BrowserStartActionCaptureArgs) (BrowserStartActionCaptureResult, error) {
	return func(ctx tool.Context, _ BrowserStartActionCaptureArgs) (BrowserStartActionCaptureResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		if err := mgr.StartActionCapture(false); err != nil {
			return BrowserStartActionCaptureResult{}, err
		}
		return BrowserStartActionCaptureResult{
			Success: true,
			Message: "DOM action capture started. Interactions are appended to the in-page action log.",
		}, nil
	}
}

// --- browser_stop_action_capture ---

type BrowserStopActionCaptureArgs struct{}

type BrowserStopActionCaptureResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func BrowserStopActionCapture(mgr *browser.Manager) func(tool.Context, BrowserStopActionCaptureArgs) (BrowserStopActionCaptureResult, error) {
	return func(ctx tool.Context, _ BrowserStopActionCaptureArgs) (BrowserStopActionCaptureResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		if err := mgr.StopActionCapture(); err != nil {
			return BrowserStopActionCaptureResult{}, err
		}
		return BrowserStopActionCaptureResult{
			Success: true,
			Message: "DOM action capture stopped. Use browser_get_action_log to read events.",
		}, nil
	}
}

// --- browser_get_action_log ---

type BrowserGetActionLogArgs struct {
	Clear bool `json:"clear,omitempty" jsonschema:"If true, clear the log after reading."`
}

type BrowserGetActionLogResult struct {
	Success bool                  `json:"success"`
	Count   int                   `json:"count"`
	Events  []browser.ActionEvent `json:"events"`
	JSON    string                `json:"json"`
}

func BrowserGetActionLog(mgr *browser.Manager) func(tool.Context, BrowserGetActionLogArgs) (BrowserGetActionLogResult, error) {
	return func(ctx tool.Context, args BrowserGetActionLogArgs) (BrowserGetActionLogResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		events, err := mgr.GetActionLog()
		if err != nil {
			return BrowserGetActionLogResult{}, err
		}
		raw, err := json.Marshal(events)
		if err != nil {
			return BrowserGetActionLogResult{}, err
		}
		if args.Clear {
			_ = mgr.ClearActionLog()
		}
		return BrowserGetActionLogResult{
			Success: true,
			Count:   len(events),
			Events:  events,
			JSON:    string(raw),
		}, nil
	}
}

// --- browser_clear_action_log ---

type BrowserClearActionLogArgs struct{}

type BrowserClearActionLogResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func BrowserClearActionLog(mgr *browser.Manager) func(tool.Context, BrowserClearActionLogArgs) (BrowserClearActionLogResult, error) {
	return func(ctx tool.Context, _ BrowserClearActionLogArgs) (BrowserClearActionLogResult, error) {
		if ctx != nil {
			mgr.EnsureSessionID(ctx.SessionID())
		}
		if err := mgr.ClearActionLog(); err != nil {
			return BrowserClearActionLogResult{}, err
		}
		return BrowserClearActionLogResult{Success: true, Message: "Action log cleared."}, nil
	}
}
