package pptxworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ImportRunner invokes the pinned PPTX->ASD import worker through a controlled
// Node process. The worker receives one JSON ImportRequest on stdin and emits
// one JSON ImportResponse on stdout, mirroring the export Runner contract.
type ImportRunner struct {
	NodePath   string
	WorkingDir string
	ScriptPath string
	Timeout    time.Duration
}

func (r ImportRunner) Run(ctx context.Context, req ImportRequest) (ImportResponse, error) {
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = ImportProtocolVersion
	}
	if req.ProtocolVersion != ImportProtocolVersion {
		return ImportResponse{}, fmt.Errorf("unsupported pptx import worker protocol %d", req.ProtocolVersion)
	}
	if req.Mode == "" {
		req.Mode = "deck"
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return ImportResponse{}, fmt.Errorf("marshal pptx import request: %w", err)
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
			return ImportResponse{}, fmt.Errorf("pptx import worker timeout: %w", ctx.Err())
		}
		return ImportResponse{}, fmt.Errorf("pptx import worker: %w: %s", err, stderr.String())
	}
	var response ImportResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return ImportResponse{}, fmt.Errorf("decode pptx import worker response: %w", err)
	}
	if response.ProtocolVersion != ImportProtocolVersion {
		return ImportResponse{}, fmt.Errorf("pptx import worker returned protocol %d", response.ProtocolVersion)
	}
	if response.Error != "" {
		return ImportResponse{}, fmt.Errorf("pptx import worker: %s", response.Error)
	}
	return response, nil
}
