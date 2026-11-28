package launcher

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// botBrowserExtra selects only handlers whose effects remain inside the private
// run browser. This is a transport allowlist, NOT an effect-policy exemption:
// BotCatalogueClassifier deliberately leaves browser actions unknown.
func botBrowserExtra(name string) bool {
	switch name {
	case "browser_highlight", "browser_clear_highlights", "browser_move_cursor",
		"browser_fullscreen", "browser_pause", "browser_run_code",
		"browser_set_offline", "browser_set_headers", "browser_set_credentials",
		"browser_set_geolocation", "browser_set_media", "browser_set_timezone",
		"browser_set_locale", "browser_set_device", "browser_start_action_capture",
		"browser_stop_action_capture", "browser_get_action_log", "browser_clear_action_log":
		return true
	}
	return false
}

// botBrowserUnsupported documents why seemingly reusable Chat handlers must not
// be exposed by the production Bot catalogue. Preserve the real argument schema.
func botBrowserUnsupported(name string) string {
	switch name {
	case "browser_response_body":
		return "response interception replays HTTP on the daemon; a sandbox CDP response-stage adapter is required"
	case "browser_request_human":
		return "native browser takeover is blocked awaiting an explicit owner-bound handoff capability; no durable resumable browser continuation or takeover surface is available (no host listener was opened and no human wait was started)"
	case "browser_start_recording", "browser_stop_recording", "browser_recording_status":
		return "recording requires a scoped artifact/stat adapter; daemon filesystem fallback is disabled"
	}
	return ""
}

type botUnsupportedBrowserTool struct {
	botRunnableTool
	reason string
}

func (t *botUnsupportedBrowserTool) Run(tool.Context, any) (map[string]any, error) {
	return nil, fmt.Errorf("%w: %s: %s", store.ErrBotUnsupported, t.Name(), t.reason)
}

const botBrowserPDFLimit = 32 << 20

// newBotBrowserPDFTool prints the SAME run page and writes only through Backend.
// It must never call tools.BrowserPDF: that handler writes on the daemon. Paths
// are intentionally restricted to /workspace basenames; sandbox symlink/network
// enforcement remains the backend's responsibility, not a host path check.
func newBotBrowserPDFTool(mgr *browser.Manager, backend sandbox.Backend, sessionID string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{Name: "browser_pdf", Description: "Save the current run browser page as a PDF in its sandbox. Path must be a PDF basename or /workspace/<basename>; default is a unique file. Maximum 32 MiB. No daemon file is written."}, func(ctx tool.Context, args tools.BrowserPDFArgs) (tools.BrowserPDFResult, error) {
		var empty tools.BrowserPDFResult
		if ctx == nil || backend == nil || sessionID == "" || !mgr.SandboxEnabled {
			return empty, store.ErrBotUnsupported
		}
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		filename := args.Path
		if filename == "" {
			filename = "browser-" + uuid.NewString() + ".pdf"
		}
		filename = strings.TrimPrefix(filename, "/workspace/")
		if filename != path.Base(filename) || strings.ContainsAny(filename, "\\\x00\r\n") || !strings.HasSuffix(strings.ToLower(filename), ".pdf") || filename == ".pdf" {
			return empty, fmt.Errorf("PDF path must be a basename or /workspace/<basename> ending in .pdf")
		}
		pg, err := mgr.CurrentPage()
		if err != nil {
			return empty, err
		}
		pg = pg.Context(ctx).Timeout(60 * time.Second)
		printed, err := (proto.PagePrintToPDF{PrintBackground: true, TransferMode: proto.PagePrintToPDFTransferModeReturnAsStream}).Call(pg)
		if err != nil {
			return empty, fmt.Errorf("PDF generation: %w", err)
		}
		if printed.Stream == "" {
			return empty, fmt.Errorf("PDF backend did not return a stream")
		}
		defer (proto.IOClose{Handle: printed.Stream}).Call(pg)
		// Read CDP chunks directly: rod's StreamReader drops data delivered with
		// EOF and does not bound the requested chunk size.
		data := make([]byte, 0)
		chunkSize := 64 << 10
		for {
			chunk, readErr := (proto.IORead{Handle: printed.Stream, Size: &chunkSize}).Call(pg)
			if readErr != nil {
				return empty, fmt.Errorf("PDF stream: %w", readErr)
			}
			part := []byte(chunk.Data)
			if chunk.Base64Encoded {
				part, err = base64.StdEncoding.DecodeString(chunk.Data)
				if err != nil {
					return empty, fmt.Errorf("invalid PDF stream encoding: %w", err)
				}
			}
			if len(part) > botBrowserPDFLimit-len(data) {
				return empty, fmt.Errorf("PDF exceeds 32 MiB limit")
			}
			data = append(data, part...)
			if chunk.EOF {
				break
			}
			if len(part) == 0 {
				return empty, fmt.Errorf("PDF stream made no progress")
			}
		}
		if len(data) == 0 {
			return empty, fmt.Errorf("PDF stream was empty")
		}
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		output := "/workspace/" + filename
		if err := backend.PushFile(ctx, sessionID, output, bytes.NewReader(data), 0600); err != nil {
			return empty, fmt.Errorf("sandbox PDF write: %w", err)
		}
		return tools.BrowserPDFResult{Path: output, SizeBytes: len(data)}, nil
	})
}
