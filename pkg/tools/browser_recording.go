package tools

import (
	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// --- browser_start_recording ---

type BrowserStartRecordingArgs struct {
	Filename string `json:"filename,omitempty" jsonschema:"Optional basename for the MP4 (e.g. demo.mp4). Defaults to browser-<timestamp>.mp4. Letters, digits, '.', '_', '-' only."`
}

type BrowserStartRecordingResult struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func BrowserStartRecording(mgr *browser.Manager) func(tool.Context, BrowserStartRecordingArgs) (BrowserStartRecordingResult, error) {
	return func(ctx tool.Context, args BrowserStartRecordingArgs) (BrowserStartRecordingResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		res, err := mgr.StartRecording(browser.RecordingOptions{Filename: args.Filename})
		if err != nil {
			return BrowserStartRecordingResult{}, err
		}
		return BrowserStartRecordingResult{
			Status: "recording",
			Path:   res.Path,
			Width:  res.Width,
			Height: res.Height,
		}, nil
	}
}

// --- browser_stop_recording ---

type BrowserStopRecordingArgs struct{}

type BrowserStopRecordingResult struct {
	Path            string  `json:"path"`
	DurationSeconds float64 `json:"duration_seconds"`
	SizeBytes       int64   `json:"size_bytes"`
}

func BrowserStopRecording(mgr *browser.Manager) func(tool.Context, BrowserStopRecordingArgs) (BrowserStopRecordingResult, error) {
	return func(ctx tool.Context, _ BrowserStopRecordingArgs) (BrowserStopRecordingResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)
		res, err := mgr.StopRecording()
		if err != nil {
			return BrowserStopRecordingResult{}, err
		}
		return BrowserStopRecordingResult{
			Path:            res.Path,
			DurationSeconds: res.DurationSeconds,
			SizeBytes:       res.SizeBytes,
		}, nil
	}
}

// --- browser_recording_status ---

type BrowserRecordingStatusArgs struct{}

type BrowserRecordingStatusResult struct {
	Recording      bool    `json:"recording"`
	Path           string  `json:"path,omitempty"`
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
}

func BrowserRecordingStatus(mgr *browser.Manager) func(tool.Context, BrowserRecordingStatusArgs) (BrowserRecordingStatusResult, error) {
	return func(_ tool.Context, _ BrowserRecordingStatusArgs) (BrowserRecordingStatusResult, error) {
		st := mgr.RecordingStatus()
		return BrowserRecordingStatusResult{
			Recording:      st.Recording,
			Path:           st.Path,
			ElapsedSeconds: st.ElapsedSeconds,
		}, nil
	}
}
