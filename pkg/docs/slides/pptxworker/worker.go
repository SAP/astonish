package pptxworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Runner invokes a pinned JavaScript worker through a controlled Node process.
// The worker receives one JSON request on stdin and emits one JSON response.
type Runner struct {
	NodePath   string
	WorkingDir string
	ScriptPath string
	Timeout    time.Duration
}

func (r Runner) Run(ctx context.Context, req Request) (Response, error) {
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = ProtocolVersion
	}
	if req.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("unsupported pptx worker protocol %d", req.ProtocolVersion)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal pptx request: %w", err)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
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
			return Response{}, fmt.Errorf("pptx worker timeout: %w", ctx.Err())
		}
		return Response{}, fmt.Errorf("pptx worker: %w: %s", err, stderr.String())
	}
	var response Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return Response{}, fmt.Errorf("decode pptx worker response: %w", err)
	}
	if response.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("pptx worker returned protocol %d", response.ProtocolVersion)
	}
	if response.Error != "" {
		return Response{}, fmt.Errorf("pptx worker: %s", response.Error)
	}
	return response, nil
}
