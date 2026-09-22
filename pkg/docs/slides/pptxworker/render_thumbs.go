package pptxworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ThumbProtocolVersion pins the request/response contract shared with
// render_thumbs.mjs (pptx-glimpse). Independent of the import worker protocol.
const ThumbProtocolVersion = 1

// ThumbRequest asks the thumbnail worker to rasterize every slide of a PPTX.
// Width is the output width in pixels; height follows the slide aspect ratio.
// Zero Width uses the worker default (480).
type ThumbRequest struct {
	ProtocolVersion int    `json:"protocolVersion"`
	PPTXBase64      string `json:"pptxBase64"`
	Width           int    `json:"width,omitempty"`
}

// ThumbSlide is one rendered slide. PNGBase64 is empty when that slide failed;
// the failure reason is in Warnings.
type ThumbSlide struct {
	SlideNumber int      `json:"slideNumber"`
	PNGBase64   string   `json:"pngBase64"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Warnings    []string `json:"warnings,omitempty"`
}

// ThumbResponse is the single JSON message render_thumbs.mjs writes to stdout.
type ThumbResponse struct {
	ProtocolVersion int          `json:"protocolVersion"`
	Slides          []ThumbSlide `json:"slides,omitempty"`
	Warnings        []string     `json:"warnings,omitempty"`
	Error           string       `json:"error,omitempty"`
}

// ThumbRunner invokes render_thumbs.mjs through Node. WorkingDir must be the
// directory that contains node_modules/node-pptx-png (web/).
type ThumbRunner struct {
	NodePath   string
	WorkingDir string
	ScriptPath string
	Timeout    time.Duration
}

// Run renders every slide. A worker-level error is returned as a Go error.
// Per-slide failures stay in ThumbResponse.Slides so the caller can still
// show the slides that rendered.
func (r ThumbRunner) Run(ctx context.Context, req ThumbRequest) (ThumbResponse, error) {
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = ThumbProtocolVersion
	}
	if req.ProtocolVersion != ThumbProtocolVersion {
		return ThumbResponse{}, fmt.Errorf("unsupported pptx thumbnail protocol %d", req.ProtocolVersion)
	}
	if req.PPTXBase64 == "" {
		return ThumbResponse{}, fmt.Errorf("pptx thumbnail worker: empty pptx")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return ThumbResponse{}, fmt.Errorf("marshal pptx thumbnail request: %w", err)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	node := r.NodePath
	if node == "" {
		node = "node"
	}
	cmd := exec.CommandContext(ctx, node, r.ScriptPath)
	cmd.Dir = r.WorkingDir
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ThumbResponse{}, fmt.Errorf("pptx thumbnail worker timeout: %w", ctx.Err())
		}
		return ThumbResponse{}, fmt.Errorf("pptx thumbnail worker: %w: %s", err, stderr.String())
	}
	var response ThumbResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return ThumbResponse{}, fmt.Errorf("decode pptx thumbnail worker response: %w: %s", err, stderr.String())
	}
	if response.ProtocolVersion != ThumbProtocolVersion {
		return ThumbResponse{}, fmt.Errorf("pptx thumbnail worker returned protocol %d", response.ProtocolVersion)
	}
	if response.Error != "" {
		return ThumbResponse{}, fmt.Errorf("pptx thumbnail worker: %s", response.Error)
	}
	return response, nil
}

// DecodePNG returns the raw PNG bytes for a rendered slide.
func (s ThumbSlide) DecodePNG() ([]byte, error) {
	if s.PNGBase64 == "" {
		return nil, fmt.Errorf("slide %d has no png", s.SlideNumber)
	}
	raw, err := base64.StdEncoding.DecodeString(s.PNGBase64)
	if err != nil {
		return nil, fmt.Errorf("slide %d png: %w", s.SlideNumber, err)
	}
	return raw, nil
}
