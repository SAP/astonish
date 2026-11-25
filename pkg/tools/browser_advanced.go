package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
	"google.golang.org/adk/tool"
)

// --- browser_evaluate ---

type BrowserEvaluateArgs struct {
	Expression string `json:"expression" jsonschema:"JavaScript expression to evaluate in the page context"`
	Ref        string `json:"ref,omitempty" jsonschema:"Element ref to use as 'this' context (optional)"`
}

type BrowserEvaluateResult struct {
	Result interface{} `json:"result"`
	Type   string      `json:"type"`
}

func BrowserEvaluate(mgr *browser.Manager, refs *browser.RefMap) func(tool.Context, BrowserEvaluateArgs) (BrowserEvaluateResult, error) {
	return func(_ tool.Context, args BrowserEvaluateArgs) (BrowserEvaluateResult, error) {
		if args.Expression == "" {
			return BrowserEvaluateResult{}, fmt.Errorf("expression is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserEvaluateResult{}, err
		}

		// Rod's Eval wraps the expression with .apply(), so it expects a
		// function, not a raw expression. Wrap the user's expression in an
		// arrow function so that raw expressions like "document.title" or
		// "1 + 2" work correctly.
		jsFunc := fmt.Sprintf("() => { return (%s) }", args.Expression)

		var res *proto.RuntimeRemoteObject

		if args.Ref != "" {
			el, err := refs.ResolveElement(pg, args.Ref)
			if err != nil {
				return BrowserEvaluateResult{}, err
			}
			res, err = el.Eval(jsFunc)
			if err != nil {
				return BrowserEvaluateResult{}, fmt.Errorf("eval failed: %w", err)
			}
		} else {
			res, err = pg.Eval(jsFunc)
			if err != nil {
				return BrowserEvaluateResult{}, fmt.Errorf("eval failed: %w", err)
			}
		}

		return BrowserEvaluateResult{
			Result: gsonToInterface(res.Value),
			Type:   string(res.Type),
		}, nil
	}
}

// --- browser_run_code ---

type BrowserRunCodeArgs struct {
	Code string `json:"code" jsonschema:"Multi-line JavaScript code to execute in the page context. Has access to the full browser DOM API."`
}

type BrowserRunCodeResult struct {
	Result interface{} `json:"result"`
	Type   string      `json:"type"`
}

func BrowserRunCode(mgr *browser.Manager) func(tool.Context, BrowserRunCodeArgs) (BrowserRunCodeResult, error) {
	return func(_ tool.Context, args BrowserRunCodeArgs) (BrowserRunCodeResult, error) {
		if args.Code == "" {
			return BrowserRunCodeResult{}, fmt.Errorf("code is required")
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserRunCodeResult{}, err
		}

		// Wrap the user's code in an async arrow function to support await.
		// Rod's Eval wraps the JS with .apply(), so we must pass a function
		// (not an IIFE) to avoid calling .apply() on a Promise return value.
		// Apply a 30-second timeout so runaway scripts don't hang forever.
		wrapped := fmt.Sprintf("async () => { %s }", args.Code)
		res, err := pg.Timeout(30 * time.Second).Evaluate(rod.Eval(wrapped).ByPromise())
		if err != nil {
			return BrowserRunCodeResult{}, fmt.Errorf("code execution failed: %w", err)
		}

		return BrowserRunCodeResult{
			Result: gsonToInterface(res.Value),
			Type:   string(res.Type),
		}, nil
	}
}

// --- browser_pdf ---

type BrowserPDFArgs struct {
	Path string `json:"path,omitempty" jsonschema:"File path to save the PDF (default: temp file)"`
}

type BrowserPDFResult struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
}

func BrowserPDF(mgr *browser.Manager) func(tool.Context, BrowserPDFArgs) (BrowserPDFResult, error) {
	return func(ctx tool.Context, args BrowserPDFArgs) (BrowserPDFResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		sessionID := mgr.SessionID()
		if mgr.SandboxEnabled && (ctx == nil || sessionID == "" || mgr.WriteArtifactFunc == nil) {
			return BrowserPDFResult{}, fmt.Errorf("sandbox PDF artifact transport is not wired")
		}
		savePath := args.Path
		if mgr.SandboxEnabled {
			if savePath == "" {
				savePath = fmt.Sprintf("browser-%d.pdf", time.Now().UnixNano())
			}
			name := strings.TrimPrefix(savePath, "/workspace/")
			if name != filepath.Base(name) || strings.ContainsAny(name, "\\\x00\r\n") || name == ".pdf" || !strings.HasSuffix(strings.ToLower(name), ".pdf") {
				return BrowserPDFResult{}, fmt.Errorf("PDF path must be a basename or /workspace/<basename> ending in .pdf")
			}
			savePath = "/workspace/" + name
		}
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserPDFResult{}, err
		}
		callCtx := context.Context(context.Background())
		if ctx != nil {
			callCtx = ctx
		}
		pg = pg.Context(callCtx).Timeout(60 * time.Second)
		printed, err := (proto.PagePrintToPDF{PrintBackground: true, TransferMode: proto.PagePrintToPDFTransferModeReturnAsStream}).Call(pg)
		if err != nil {
			return BrowserPDFResult{}, fmt.Errorf("PDF generation failed: %w", err)
		}
		if printed.Stream == "" {
			return BrowserPDFResult{}, fmt.Errorf("PDF backend returned no stream")
		}
		defer (proto.IOClose{Handle: printed.Stream}).Call(pg)
		data := []byte{}
		chunkSize := 64 << 10
		for {
			chunk, err := (proto.IORead{Handle: printed.Stream, Size: &chunkSize}).Call(pg)
			if err != nil {
				return BrowserPDFResult{}, err
			}
			part := []byte(chunk.Data)
			if chunk.Base64Encoded {
				part, err = base64.StdEncoding.DecodeString(chunk.Data)
				if err != nil {
					return BrowserPDFResult{}, err
				}
			}
			if len(part) > (32<<20)-len(data) {
				return BrowserPDFResult{}, fmt.Errorf("PDF exceeds 32 MiB limit")
			}
			data = append(data, part...)
			if chunk.EOF {
				break
			}
			if len(part) == 0 {
				return BrowserPDFResult{}, fmt.Errorf("PDF stream made no progress")
			}
		}
		if len(data) == 0 {
			return BrowserPDFResult{}, fmt.Errorf("PDF stream was empty")
		}
		if err := callCtx.Err(); err != nil {
			return BrowserPDFResult{}, err
		}
		if mgr.SandboxEnabled {
			if mgr.SessionID() != sessionID {
				return BrowserPDFResult{}, fmt.Errorf("browser session changed during PDF capture")
			}
			if err := mgr.WriteArtifactFunc(callCtx, sessionID, savePath, data); err != nil {
				return BrowserPDFResult{}, err
			}
		} else {
			if savePath == "" {
				f, err := os.CreateTemp("", "browser-*.pdf")
				if err != nil {
					return BrowserPDFResult{}, err
				}
				savePath = f.Name()
				_ = f.Close()
			} else if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
				return BrowserPDFResult{}, err
			}
			if err := os.WriteFile(savePath, data, 0644); err != nil {
				return BrowserPDFResult{}, err
			}
		}
		return BrowserPDFResult{Path: savePath, SizeBytes: len(data)}, nil
	}
}

// gsonToInterface converts a gson.JSON value to a Go interface.
func gsonToInterface(v gson.JSON) interface{} {
	if v.Nil() {
		return nil
	}

	// Use MarshalJSON to get bytes, then unmarshal to generic interface
	data, err := v.MarshalJSON()
	if err != nil {
		return v.String()
	}

	var result interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return v.String()
	}
	return result
}

// --- browser_response_body ---

type BrowserResponseBodyArgs struct {
	Action     string `json:"action" jsonschema:"Action: listen (start intercepting), read (get captured response), or stop (remove interceptor)"`
	URLPattern string `json:"urlPattern,omitempty" jsonschema:"URL pattern to match (wildcard: * matches any chars). Used with action=listen. Example: '*api/data*'"`
	Timeout    int    `json:"timeout,omitempty" jsonschema:"Max milliseconds to wait for a matching response (used with action=read, default 30000)"`
	MaxChars   int    `json:"maxChars,omitempty" jsonschema:"Maximum characters in response body (used with action=read, default 50000)"`
}

type BrowserResponseBodyResult struct {
	Success      bool              `json:"success"`
	Message      string            `json:"message,omitempty"`
	Listening    bool              `json:"listening,omitempty"`
	URLPattern   string            `json:"urlPattern,omitempty"`
	URL          string            `json:"url,omitempty"`
	Status       int               `json:"status,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
	ResourceType string            `json:"resourceType,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
}

func BrowserResponseBody(mgr *browser.Manager) func(tool.Context, BrowserResponseBodyArgs) (BrowserResponseBodyResult, error) {
	return func(ctx tool.Context, args BrowserResponseBodyArgs) (BrowserResponseBodyResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserResponseBodyResult{}, err
		}
		capture := mgr.ResponseCaptureFor(pg)
		switch args.Action {
		case "listen":
			if err := capture.Start(pg, args.URLPattern); err != nil {
				return BrowserResponseBodyResult{}, err
			}
			return BrowserResponseBodyResult{Success: true, Listening: true, URLPattern: args.URLPattern, Message: fmt.Sprintf("Listening for responses matching %q", args.URLPattern)}, nil
		case "read":
			return handleResponseRead(ctx, capture, args)
		case "stop":
			pattern := capture.Pattern()
			if err := capture.Stop(); err != nil {
				return BrowserResponseBodyResult{}, err
			}
			return BrowserResponseBodyResult{Success: true, Message: fmt.Sprintf("Stopped capture for %q", pattern)}, nil
		default:
			return BrowserResponseBodyResult{}, fmt.Errorf("invalid action %q: must be listen, read, or stop", args.Action)
		}
	}
}

func handleResponseRead(ctx context.Context, capture *browser.CDPResponseCapture, args BrowserResponseBodyArgs) (BrowserResponseBodyResult, error) {
	if !capture.IsActive() {
		return BrowserResponseBodyResult{}, fmt.Errorf("no response interceptor active — call with action=\"listen\" first")
	}

	timeout := args.Timeout
	if timeout <= 0 {
		timeout = 30000
	}
	maxChars := args.MaxChars
	if maxChars <= 0 {
		maxChars = 50000
	}

	// Check if we already have captured responses.
	responses := capture.Responses()
	if len(responses) == 0 {
		// Wait for a response up to timeout.
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		defer timer.Stop()

		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-ctx.Done():
			return BrowserResponseBodyResult{}, ctx.Err()
		case <-capture.WaitChan():
			responses = capture.Responses()
		case <-timer.C:
			return BrowserResponseBodyResult{
				Success: false,
				Message: fmt.Sprintf("No matching response received within %dms", timeout),
			}, nil
		}
	}

	if len(responses) == 0 {
		return BrowserResponseBodyResult{
			Success: false,
			Message: "No responses captured",
		}, nil
	}

	// Return the most recent response.
	latest := responses[len(responses)-1]
	body := latest.Body
	truncated := false
	if len(body) > maxChars {
		body = body[:maxChars]
		truncated = true
	}

	return BrowserResponseBodyResult{
		Success:      true,
		URL:          latest.URL,
		Status:       latest.Status,
		Headers:      latest.Headers,
		Body:         body,
		ResourceType: latest.ResourceType,
		Truncated:    truncated,
		Message:      fmt.Sprintf("Captured response from %s (status %d, %d chars)", latest.URL, latest.Status, len(latest.Body)),
	}, nil
}
