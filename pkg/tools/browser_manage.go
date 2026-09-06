package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// --- browser_tabs ---

type BrowserTabsArgs struct {
	Action    string `json:"action" jsonschema:"Tab action: list, new, close, or select"`
	URL       string `json:"url,omitempty" jsonschema:"URL for the new tab (used with action=new)"`
	TargetID  string `json:"targetId,omitempty" jsonschema:"Tab target ID (used with action=close or select)"`
	Incognito bool   `json:"incognito,omitempty" jsonschema:"Open in incognito mode with isolated cookies/storage (used with action=new). Use for testing login flows or browsing without personal session data."`
}

type TabInfo struct {
	TargetID string `json:"targetId"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

type BrowserTabsResult struct {
	Success bool      `json:"success"`
	Tabs    []TabInfo `json:"tabs,omitempty"`
	Message string    `json:"message,omitempty"`
}

func BrowserTabs(mgr *browser.Manager, guard *browser.NavigationGuard) func(tool.Context, BrowserTabsArgs) (BrowserTabsResult, error) {
	return func(ctx tool.Context, args BrowserTabsArgs) (BrowserTabsResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)

		b, err := mgr.GetOrLaunch()
		if err != nil {
			return BrowserTabsResult{}, err
		}

		switch strings.ToLower(args.Action) {
		case "list", "":
			pages, err := b.Pages()
			if err != nil {
				return BrowserTabsResult{}, err
			}
			var tabs []TabInfo
			for _, pg := range pages {
				info, err := pg.Info()
				if err != nil {
					continue
				}
				tabs = append(tabs, TabInfo{
					TargetID: string(info.TargetID),
					Title:    info.Title,
					URL:      info.URL,
				})
			}
			return BrowserTabsResult{Success: true, Tabs: tabs}, nil

		case "new":
			url := args.URL
			if url == "" {
				url = "about:blank"
			}
			if url != "about:blank" {
				if err := guard.Check(url); err != nil {
					return BrowserTabsResult{}, err
				}
			}

			var pg *rod.Page
			if args.Incognito {
				// Incognito: isolated cookie jar and storage
				pg, err = mgr.NewIncognitoPage()
				if err != nil {
					return BrowserTabsResult{}, fmt.Errorf("failed to create incognito tab: %w", err)
				}
				if url != "about:blank" {
					if err := pg.Navigate(url); err != nil {
						return BrowserTabsResult{}, fmt.Errorf("failed to navigate incognito tab: %w", err)
					}
					_ = pg.Timeout(mgr.NavigationTimeout()).WaitLoad()
				}
			} else {
				// Create page to about:blank first so that SetActivePage can
				// inject stealth JS (via EvalOnNewDocument) and UA overrides
				// BEFORE any real navigation. Without this, the first page
				// load runs without anti-detection measures.
				pg, err = b.Page(proto.TargetCreateTarget{URL: "about:blank"})
				if err != nil {
					return BrowserTabsResult{}, fmt.Errorf("failed to create tab: %w", err)
				}
				mgr.SetActivePage(pg)
				if url != "about:blank" {
					if err := pg.Navigate(url); err != nil {
						return BrowserTabsResult{}, fmt.Errorf("failed to navigate new tab: %w", err)
					}
					_ = pg.Timeout(mgr.NavigationTimeout()).WaitLoad()
				}
			}

			info, _ := pg.Info()
			msg := "New tab opened"
			if args.Incognito {
				msg = "New incognito tab opened (isolated cookies/storage)"
			}
			if info != nil {
				msg += fmt.Sprintf(": %s", info.URL)
			}
			return BrowserTabsResult{Success: true, Message: msg}, nil

		case "close":
			if args.TargetID == "" {
				return BrowserTabsResult{}, fmt.Errorf("targetId is required for close action")
			}
			pg, err := mgr.GetPage(proto.TargetTargetID(args.TargetID))
			if err != nil {
				return BrowserTabsResult{}, err
			}
			if err := pg.Close(); err != nil {
				return BrowserTabsResult{}, fmt.Errorf("failed to close tab: %w", err)
			}
			return BrowserTabsResult{Success: true, Message: "Tab closed"}, nil

		case "select":
			if args.TargetID == "" {
				return BrowserTabsResult{}, fmt.Errorf("targetId is required for select action")
			}
			pg, err := mgr.GetPage(proto.TargetTargetID(args.TargetID))
			if err != nil {
				return BrowserTabsResult{}, err
			}
			pg, err = pg.Activate()
			if err != nil {
				return BrowserTabsResult{}, fmt.Errorf("failed to activate tab: %w", err)
			}
			mgr.SetActivePage(pg)
			return BrowserTabsResult{Success: true, Message: "Tab selected"}, nil

		default:
			return BrowserTabsResult{}, fmt.Errorf("unknown action %q — use list, new, close, or select", args.Action)
		}
	}
}

// --- browser_close ---

type BrowserCloseArgs struct{}

type BrowserCloseResult struct {
	Success bool `json:"success"`
}

func BrowserClose(mgr *browser.Manager) func(tool.Context, BrowserCloseArgs) (BrowserCloseResult, error) {
	return func(_ tool.Context, _ BrowserCloseArgs) (BrowserCloseResult, error) {
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserCloseResult{}, err
		}
		if err := pg.Close(); err != nil {
			return BrowserCloseResult{}, fmt.Errorf("failed to close page: %w", err)
		}
		return BrowserCloseResult{Success: true}, nil
	}
}

// --- browser_resize ---

type BrowserResizeArgs struct {
	Width  int `json:"width" jsonschema:"Viewport width in pixels"`
	Height int `json:"height" jsonschema:"Viewport height in pixels"`
}

type BrowserResizeResult struct {
	Success bool `json:"success"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
}

func BrowserResize(mgr *browser.Manager) func(tool.Context, BrowserResizeArgs) (BrowserResizeResult, error) {
	return func(_ tool.Context, args BrowserResizeArgs) (BrowserResizeResult, error) {
		if args.Width <= 0 || args.Height <= 0 {
			return BrowserResizeResult{}, fmt.Errorf("width and height must be positive")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserResizeResult{}, err
		}

		if err := pg.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width:  args.Width,
			Height: args.Height,
		}); err != nil {
			return BrowserResizeResult{}, fmt.Errorf("resize failed: %w", err)
		}

		return BrowserResizeResult{Success: true, Width: args.Width, Height: args.Height}, nil
	}
}

// --- browser_wait_for ---

type BrowserWaitForArgs struct {
	Text       string `json:"text,omitempty" jsonschema:"Wait for this text to appear on the page"`
	TextGone   string `json:"textGone,omitempty" jsonschema:"Wait for this text to disappear from the page"`
	Selector   string `json:"selector,omitempty" jsonschema:"Wait for a CSS selector to be visible"`
	URL        string `json:"url,omitempty" jsonschema:"Wait for the page URL to contain this string"`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Timeout in milliseconds (default 30000)"`
	State      string `json:"state,omitempty" jsonschema:"Wait for page state: load, domcontentloaded, or networkidle"`
	Expression string `json:"expression,omitempty" jsonschema:"Wait for a JavaScript expression to return truthy"`
}

type BrowserWaitForResult struct {
	Success   bool `json:"success"`
	ElapsedMs int  `json:"elapsed_ms"`
}

func BrowserWaitFor(mgr *browser.Manager) func(tool.Context, BrowserWaitForArgs) (BrowserWaitForResult, error) {
	return func(_ tool.Context, args BrowserWaitForArgs) (BrowserWaitForResult, error) {
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserWaitForResult{}, err
		}

		timeout := 30 * time.Second
		if args.Timeout > 0 {
			timeout = time.Duration(args.Timeout) * time.Millisecond
		}
		pg = pg.Timeout(timeout)

		start := time.Now()

		// Wait for page state
		if args.State != "" {
			switch strings.ToLower(args.State) {
			case "load":
				if err := pg.WaitLoad(); err != nil {
					return BrowserWaitForResult{}, fmt.Errorf("wait for load failed: %w", err)
				}
			case "domcontentloaded":
				if err := pg.WaitLoad(); err != nil {
					return BrowserWaitForResult{}, fmt.Errorf("wait for DOMContentLoaded failed: %w", err)
				}
			case "networkidle":
				wait := pg.WaitRequestIdle(500*time.Millisecond, nil, nil, nil)
				wait()
			default:
				return BrowserWaitForResult{}, fmt.Errorf("unknown state %q", args.State)
			}
		}

		// Wait for text to appear
		if args.Text != "" {
			if err := pg.Wait(rod.Eval(fmt.Sprintf(
				`() => document.body && document.body.innerText.includes(%q)`, args.Text,
			))); err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for text %q failed: %w", args.Text, err)
			}
		}

		// Wait for text to disappear
		if args.TextGone != "" {
			if err := pg.Wait(rod.Eval(fmt.Sprintf(
				`() => !document.body || !document.body.innerText.includes(%q)`, args.TextGone,
			))); err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for text gone %q failed: %w", args.TextGone, err)
			}
		}

		// Wait for CSS selector to be visible
		if args.Selector != "" {
			el, err := pg.Element(args.Selector)
			if err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for selector %q failed: %w", args.Selector, err)
			}
			if err := el.WaitVisible(); err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for selector %q visible failed: %w", args.Selector, err)
			}
		}

		// Wait for URL to contain string
		if args.URL != "" {
			if err := pg.Wait(rod.Eval(fmt.Sprintf(
				`() => window.location.href.includes(%q)`, args.URL,
			))); err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for URL %q failed: %w", args.URL, err)
			}
		}

		// Wait for JS expression
		if args.Expression != "" {
			if err := pg.Wait(rod.Eval(fmt.Sprintf(
				`() => !!(%s)`, args.Expression,
			))); err != nil {
				return BrowserWaitForResult{}, fmt.Errorf("wait for expression failed: %w", err)
			}
		}

		elapsed := time.Since(start)
		return BrowserWaitForResult{
			Success:   true,
			ElapsedMs: int(elapsed.Milliseconds()),
		}, nil
	}
}

// --- browser_file_upload ---

type BrowserFileUploadArgs struct {
	Ref   string   `json:"ref,omitempty" jsonschema:"Element ref for the file input"`
	Paths []string `json:"paths" jsonschema:"File paths to upload"`
}

type BrowserFileUploadResult struct {
	Success       bool `json:"success"`
	FilesUploaded int  `json:"files_uploaded"`
}

func BrowserFileUpload(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserFileUploadArgs) (BrowserFileUploadResult, error) {
	return func(_ tool.Context, args BrowserFileUploadArgs) (BrowserFileUploadResult, error) {
		if len(args.Paths) == 0 {
			return BrowserFileUploadResult{}, fmt.Errorf("paths is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserFileUploadResult{}, err
		}

		var el *rod.Element
		if args.Ref != "" {
			el, err = refs.ResolveElement(pg, args.Ref)
			if err != nil {
				return BrowserFileUploadResult{}, err
			}
		} else {
			// Try to find a file input on the page
			el, err = pg.Element("input[type=file]")
			if err != nil {
				return BrowserFileUploadResult{}, fmt.Errorf("no file input found on page — provide a ref")
			}
		}

		if err := el.SetFiles(args.Paths); err != nil {
			return BrowserFileUploadResult{}, fmt.Errorf("file upload failed: %w", err)
		}

		return BrowserFileUploadResult{Success: true, FilesUploaded: len(args.Paths)}, nil
	}
}

// --- browser_handle_dialog ---

type BrowserHandleDialogArgs struct {
	Accept     bool   `json:"accept" jsonschema:"Accept (true) or dismiss (false) the dialog"`
	PromptText string `json:"promptText,omitempty" jsonschema:"Text to enter for prompt dialogs"`
	TimeoutMs  int    `json:"timeout_ms,omitempty" jsonschema:"Max milliseconds to wait for a dialog to appear. Default: 10000 (10s). Use 0 for the default."`
}

type BrowserHandleDialogResult struct {
	Success    bool   `json:"success"`
	DialogType string `json:"dialog_type,omitempty"`
	Message    string `json:"message,omitempty"`
}

func BrowserHandleDialog(mgr *browser.Manager) func(tool.Context, BrowserHandleDialogArgs) (BrowserHandleDialogResult, error) {
	return func(_ tool.Context, args BrowserHandleDialogArgs) (BrowserHandleDialogResult, error) {
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserHandleDialogResult{}, err
		}

		timeout := 10 * time.Second
		if args.TimeoutMs > 0 {
			timeout = time.Duration(args.TimeoutMs) * time.Millisecond
		}

		wait, handle := pg.HandleDialog()

		// Run the blocking wait() in a goroutine and race it against a timeout.
		// Rod's wait() does not accept a context, so this is the only safe pattern.
		type dialogResult struct {
			dialog *proto.PageJavascriptDialogOpening
		}
		ch := make(chan dialogResult, 1)
		go func() {
			d := wait()
			ch <- dialogResult{dialog: d}
		}()

		var dialog *proto.PageJavascriptDialogOpening
		select {
		case res := <-ch:
			dialog = res.dialog
		case <-time.After(timeout):
			return BrowserHandleDialogResult{
				Success: false,
				Message: fmt.Sprintf("No JavaScript dialog appeared within %dms. Most websites use inline messages instead of native dialogs (alert/confirm/prompt). Use browser_snapshot to read the current page state.", timeout.Milliseconds()),
			}, nil
		}

		if err := handle(&proto.PageHandleJavaScriptDialog{
			Accept:     args.Accept,
			PromptText: args.PromptText,
		}); err != nil {
			return BrowserHandleDialogResult{}, fmt.Errorf("handle dialog failed: %w", err)
		}

		return BrowserHandleDialogResult{
			Success:    true,
			DialogType: string(dialog.Type),
			Message:    dialog.Message,
		}, nil
	}
}
