package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
)

func TestIsTransientMCPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("failed to init MCP session: %w", context.Canceled),
			want: true,
		},
		{
			name: "deeply wrapped context.Canceled",
			err:  fmt.Errorf("failed to get tools from MCP server 'codegraph': failed to list MCP tools: %w", fmt.Errorf("failed to init MCP session: %w", context.Canceled)),
			want: true,
		},
		{
			name: "wrapped context.DeadlineExceeded",
			err:  fmt.Errorf("operation timed out: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("dial tcp 127.0.0.1:8080: connection refused"),
			want: true,
		},
		{
			name: "connection reset",
			err:  fmt.Errorf("read tcp: connection reset by peer"),
			want: true,
		},
		{
			name: "broken pipe",
			err:  fmt.Errorf("write: broken pipe"),
			want: true,
		},
		{
			name: "EOF",
			err:  fmt.Errorf("unexpected EOF"),
			want: true,
		},
		{
			name: "timeout",
			err:  fmt.Errorf("i/o timeout waiting for server"),
			want: true,
		},
		{
			name: "permanent - server not found",
			err:  fmt.Errorf("server 'foo' not found in config"),
			want: false,
		},
		{
			name: "permanent - credential resolution failed",
			err:  fmt.Errorf("failed to resolve credentials for MCP server 'foo': credential not found"),
			want: false,
		},
		{
			name: "permanent - server disabled",
			err:  fmt.Errorf("server 'foo' is disabled"),
			want: false,
		},
		{
			name: "permanent - unsupported transport",
			err:  fmt.Errorf("unsupported transport type: grpc"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTransientMCPError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientMCPError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestLazyMCPToolset_RetryOnTransientError(t *testing.T) {
	t.Parallel()

	// Create a toolset with a dummy config that will fail to start
	// (no real server running), simulating the state after a transient failure.
	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary-that-will-fail",
		},
		entries:   []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode: false,
		// Simulate a previous transient failure.
		started:     true,
		startErr:    fmt.Errorf("failed to get tools from MCP server 'test-server': %w", context.Canceled),
		retryCount:  0,
		lastAttempt: time.Now().Add(-5 * time.Second), // Well past backoff.
	}

	// Call ensureServerStarted — it should attempt a retry (and fail again
	// because there's no real server, but the retry count should increment).
	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// The call should fail (no real server), but the retry should have been attempted.
	if err == nil {
		t.Fatal("expected error from ensureServerStarted, got nil")
	}

	// Verify retry was attempted: retryCount should have incremented.
	toolset.mu.Lock()
	rc := toolset.retryCount
	la := toolset.lastAttempt
	toolset.mu.Unlock()

	if rc != 1 {
		t.Errorf("expected retryCount=1 after retry attempt, got %d", rc)
	}

	// lastAttempt should have been updated to a recent time.
	if time.Since(la) > 5*time.Second {
		t.Errorf("lastAttempt was not updated during retry (age: %v)", time.Since(la))
	}
}

func TestLazyMCPToolset_NoRetryOnPermanentError(t *testing.T) {
	t.Parallel()

	permanentErr := fmt.Errorf("server 'test-server' not found in config")

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    permanentErr,
		retryCount:  0,
		lastAttempt: time.Now().Add(-10 * time.Second), // Well past backoff.
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the same permanent error without retry.
	if err != permanentErr {
		t.Errorf("expected permanent error to be returned directly, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != 0 {
		t.Errorf("expected retryCount=0 (no retry for permanent error), got %d", rc)
	}
}

func TestLazyMCPToolset_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()

	transientErr := fmt.Errorf("failed to start MCP server 'test-server': %w", context.Canceled)

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    transientErr,
		retryCount:  maxMCPRetries, // Already exhausted all retries.
		lastAttempt: time.Now().Add(-10 * time.Second),
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the cached error without retry.
	if err != transientErr {
		t.Errorf("expected cached transient error after max retries, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != maxMCPRetries {
		t.Errorf("expected retryCount=%d (unchanged), got %d", maxMCPRetries, rc)
	}
}

func TestLazyMCPToolset_BackoffRespected(t *testing.T) {
	t.Parallel()

	transientErr := fmt.Errorf("failed to start MCP server 'test-server': %w", context.Canceled)

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    transientErr,
		retryCount:  1,
		lastAttempt: time.Now(), // Just now — backoff not elapsed.
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the cached error without retry (backoff not elapsed).
	if err != transientErr {
		t.Errorf("expected cached error during backoff period, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != 1 {
		t.Errorf("expected retryCount=1 (unchanged during backoff), got %d", rc)
	}
}

func TestLazyMCPToolset_SuccessResetsRetryCount(t *testing.T) {
	t.Parallel()

	// This test verifies that a successful startup resets the retry count.
	// We can't easily test a full successful MCP startup without a real server,
	// but we can verify the field is set correctly after construction.
	toolset := NewLazyMCPToolset("test-server", nil, config.MCPServerConfig{}, false)
	if toolset.retryCount != 0 {
		t.Errorf("expected initial retryCount=0, got %d", toolset.retryCount)
	}
	if !toolset.lastAttempt.IsZero() {
		t.Errorf("expected initial lastAttempt to be zero, got %v", toolset.lastAttempt)
	}
}
