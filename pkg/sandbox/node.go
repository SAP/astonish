package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

// nodeRequest mirrors cmd/astonish.NodeRequest for the host side.
type nodeRequest struct {
	ID   string                 `json:"id"`
	Tool string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// nodeResponse mirrors cmd/astonish.NodeResponse for the host side.
type nodeResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Ready  bool            `json:"ready,omitempty"`
}

// NodeClient manages a persistent NDJSON connection to an `astonish node`
// process running inside an Incus container. It starts the node via
// ExecInteractive and proxies tool calls over stdin/stdout.
//
// Sequential dispatch: one request at a time (no concurrent tool calls).
// Auto-restart: if the node process crashes, the next Call() restarts it.
type NodeClient struct {
	client        *IncusClient
	containerName string

	mu      sync.Mutex
	proc    *ContainerProcess
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	nextID  atomic.Int64
	started bool
	closed  bool

	// Env holds environment variables to inject into the node process.
	// Set before Start() or startLocked() to pass credentials (GH_TOKEN,
	// BIFROST_API_KEY, etc.) into the container.
	Env map[string]string
}

// NewNodeClient creates a NodeClient for the given container.
// The node is NOT started until Start() or the first Call().
func NewNodeClient(client *IncusClient, containerName string) *NodeClient {
	return &NodeClient{
		client:        client,
		containerName: containerName,
	}
}

// Start launches the `astonish node` process inside the container and waits
// for the ready signal. The container must already be running.
func (nc *NodeClient) Start() error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.closed {
		return fmt.Errorf("node client is closed")
	}

	return nc.startLocked()
}

// startLocked starts the node process. Caller must hold nc.mu.
func (nc *NodeClient) startLocked() error {
	// Clean up any existing process
	nc.stopLocked()

	// If the container is stopped (e.g., idle timeout, OOM kill, external stop),
	// restart it before attempting to exec the node process. Without this check,
	// ExecNonInteractive fails because you cannot exec in a stopped container.
	//
	// We must also handle the case where the container no longer exists — it may
	// have been destroyed by PruneStaleOnStartup, idle reaping, or a template
	// refresh. IsRunning() returns false for both "stopped" and "does not exist"
	// (it swallows errors), so we need an explicit existence check to avoid
	// calling StartInstance on a non-existent container.
	if !nc.client.IsRunning(nc.containerName) {
		if !nc.client.InstanceExists(nc.containerName) {
			return fmt.Errorf("container %q no longer exists (may have been pruned or destroyed)", nc.containerName)
		}
		if err := nc.client.StartInstance(nc.containerName); err != nil {
			return fmt.Errorf("failed to restart stopped container %q: %w", nc.containerName, err)
		}
		// Verify the template layer is intact after restart. A template refresh
		// may have force-stopped this container and left a stale overlay mount.
		// Without this check, the node starts but tool calls see empty /usr/bin/.
		// Check sentinel first, fall back to /usr/bin/git for pre-sentinel templates.
		exitCode, _ := nc.client.ExecSimple(nc.containerName, []string{"test", "-f", OverlaySentinelPath})
		if exitCode != 0 {
			exitCode, _ = nc.client.ExecSimple(nc.containerName, []string{"test", "-x", "/usr/bin/git"})
			if exitCode != 0 {
				// Template layer missing — stale overlay. Stop and signal for full recovery.
				_ = nc.client.StopInstance(nc.containerName, true)
				return fmt.Errorf("container %q template layer not visible (stale overlay)", nc.containerName)
			}
		}
	}

	cmd := []string{BinaryDestPath, "node"}
	proc, err := ExecNonInteractive(nc.client, nc.containerName, cmd, ExecOpts{
		Env:            nc.Env,
		SeparateStderr: io.Discard, // Keep stderr separate from stdout to avoid corrupting the NDJSON protocol
	})
	if err != nil {
		return fmt.Errorf("failed to start astonish node in %q: %w", nc.containerName, err)
	}

	nc.proc = proc
	nc.stdin = proc.Stdin
	nc.scanner = bufio.NewScanner(proc.Stdout)

	// Increase scanner buffer for large responses (e.g., read_file of big files)
	const maxScanSize = 10 * 1024 * 1024 // 10MB
	nc.scanner.Buffer(make([]byte, 0, 64*1024), maxScanSize)

	// Wait for the ready signal with timeout
	readyCh := make(chan error, 1)
	go func() {
		if nc.scanner.Scan() {
			line := nc.scanner.Bytes()
			var resp nodeResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				readyCh <- fmt.Errorf("invalid ready message: %w", err)
				return
			}
			if !resp.Ready {
				readyCh <- fmt.Errorf("unexpected first message (expected ready): %s", string(line))
				return
			}
			readyCh <- nil
		} else {
			if err := nc.scanner.Err(); err != nil {
				readyCh <- fmt.Errorf("node stdout closed before ready: %w", err)
			} else {
				readyCh <- fmt.Errorf("node stdout closed before ready (EOF)")
			}
		}
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			nc.stopLocked()
			return err
		}
	case <-time.After(30 * time.Second):
		nc.stopLocked()
		return fmt.Errorf("timeout waiting for node ready signal in %q", nc.containerName)
	}

	nc.started = true

	// Configure git credential helper after every successful node start (including
	// restarts after crashes). This ensures gh CLI auth is always configured when
	// GH_TOKEN is available, regardless of whether this is the first start or a
	// recovery restart. The call is idempotent and fast (~200ms).
	configureGitCredentialHelper(nc.client, nc.containerName, nc.Env)

	return nil
}

// stopLocked stops the current node process. Caller must hold nc.mu.
func (nc *NodeClient) stopLocked() {
	if nc.proc != nil {
		nc.proc.Close()
		nc.proc = nil
	}
	nc.stdin = nil
	nc.scanner = nil
	nc.started = false
}

// Call sends a tool execution request to the node and blocks until
// the response is received. Returns the result as raw JSON (to be
// unmarshalled by the caller) or an error.
//
// If the node is not started or has crashed, Call() attempts to restart it.
func (nc *NodeClient) Call(toolName string, args map[string]interface{}) (json.RawMessage, error) {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.closed {
		return nil, fmt.Errorf("node client is closed")
	}
	if nc.scanner == nil || nc.stdin == nil {
		// Defensive: previous stop may have left us in bad state; try restart
		nc.started = false
	}

	// Auto-start or restart if needed
	if !nc.started {
		if err := nc.startLocked(); err != nil {
			return nil, fmt.Errorf("failed to start node: %w", err)
		}
	}

	id := strconv.FormatInt(nc.nextID.Add(1), 10)

	req := nodeRequest{
		ID:   id,
		Tool: toolName,
		Args: args,
	}

	// Send request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := nc.stdin.Write(reqBytes); err != nil {
		// Node likely crashed — mark for restart on next call
		nc.stopLocked()
		return nil, fmt.Errorf("failed to write to node stdin: %w", err)
	}

	// Read response
	if !nc.scanner.Scan() {
		// Node stdout closed — process likely crashed
		scanErr := nc.scanner.Err()
		nc.stopLocked()
		if scanErr != nil {
			return nil, fmt.Errorf("node stdout error: %w", scanErr)
		}
		return nil, fmt.Errorf("node process exited unexpectedly")
	}

	line := nc.scanner.Bytes()
	var resp nodeResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		// Corrupt data on the pipe — likely stderr leakage from a crashed
		// subprocess (Go stack traces, library warnings, etc.). Mark the node
		// for restart so the next Call() gets a clean pipe.
		nc.stopLocked()
		return nil, fmt.Errorf("invalid response from node: %w (line: %s)", err, string(line))
	}

	// Verify correlation ID
	if resp.ID != id {
		// Out-of-sync pipe — stale response or interleaved output.
		nc.stopLocked()
		return nil, fmt.Errorf("response ID mismatch: expected %s, got %s", id, resp.ID)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("tool %q error: %s", toolName, resp.Error)
	}

	return resp.Result, nil
}

// Close shuts down the node process and releases resources.
func (nc *NodeClient) Close() error {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	if nc.closed {
		return nil
	}
	nc.closed = true
	nc.stopLocked()
	return nil
}

// IsStarted returns whether the node process is currently running.
func (nc *NodeClient) IsStarted() bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	return nc.started
}

// ContainerName returns the name of the container this client is connected to.
func (nc *NodeClient) ContainerName() string {
	return nc.containerName
}

// LazyNodeClient wraps lazy container creation + NodeClient initialization.
// On the first Call(), it creates the session container, starts the node,
// and then proxies. Subsequent calls go directly to the underlying NodeClient.
//
// This is needed because the factory builds tools before a session ID exists.
// The session ID becomes available during ProcessRequest (before the LLM call).
//
// Lifecycle:
//  1. Factory creates LazyNodeClient (no session ID yet)
//  2. NodeTool.ProcessRequest calls BindSession(sessionID) — starts container in background
//  3. NodeTool.Run calls Call() — waits for background init if still in progress, then forwards
type LazyNodeClient struct {
	incusClient  *IncusClient
	sessRegistry *SessionRegistry
	tplRegistry  *TemplateRegistry
	template     string // template to clone from (empty = @base)
	limits       *config.SandboxLimits

	mu            sync.Mutex
	sessionID     string
	nodeClient    *NodeClient
	containerName string
	initialized   bool
	closed        bool

	// lastActivity records the Unix timestamp (seconds) of the last Call().
	// Used by the idle watchdog to detect containers that should be stopped.
	lastActivity atomic.Int64

	// Env holds environment variables to inject into the node process.
	// Set before BindSession() to pass credentials into the container.
	Env map[string]string

	// OverrideSessionID, when set, is used instead of the ADK child session ID
	// for container lookup and creation. Fleet sessions set this to the fleet
	// session ID so that recovered sessions reuse the same container — the ADK
	// child session ID changes on every RunTask() call, but the fleet session
	// ID is stable across recovery cycles.
	OverrideSessionID string

	// containerReady is closed when the container is created and running,
	// BEFORE the astonish node process is started. MCP transport only needs
	// the container, not the node, so it waits on this channel.
	containerReady chan struct{}
	containerErr   error

	// OrgSlug and TeamSlug scope the container to an org's isolated network.
	// Set by NodeClientPool.GetOrCreate when the pool has org context.
	// When empty, personal-mode naming and default network are used.
	OrgSlug  string
	TeamSlug string

	// initDone is created by BindSession and closed when background init completes
	// (both container creation AND node startup). Call() waits on this channel
	// before forwarding to the NodeClient.
	initDone chan struct{}
	initErr  error
}

// NewLazyNodeClient creates a lazy node client that defers container creation
// until BindSession is called (typically from ProcessRequest, before the LLM call).
func NewLazyNodeClient(client *IncusClient, sessRegistry *SessionRegistry, tplRegistry *TemplateRegistry, template string, limits *config.SandboxLimits) *LazyNodeClient {
	return &LazyNodeClient{
		incusClient:  client,
		sessRegistry: sessRegistry,
		tplRegistry:  tplRegistry,
		template:     template,
		limits:       limits,
	}
}

// BindSession starts container creation and node startup in the background.
// This is idempotent — only the first call for a given session ID triggers init.
// Typically called from NodeTool.ProcessRequest, which runs before the LLM call,
// giving the container time to start while the LLM generates its response.
func (lnc *LazyNodeClient) BindSession(sessionID string) {
	if sessionID == "" {
		return
	}

	// Fleet sessions set OverrideSessionID to the fleet session ID (stable
	// across recovery), so the sandbox registry lookup finds the original
	// container. Without this, each RunTask() would use a fresh ADK child
	// session ID that doesn't match the registry entry.
	if lnc.OverrideSessionID != "" {
		sessionID = lnc.OverrideSessionID
	}

	lnc.mu.Lock()
	// Already bound (same or different session) — no-op
	if lnc.initDone != nil {
		lnc.mu.Unlock()
		return
	}

	if lnc.closed {
		lnc.mu.Unlock()
		return
	}

	lnc.sessionID = sessionID
	lnc.containerReady = make(chan struct{})
	lnc.initDone = make(chan struct{})
	lnc.mu.Unlock()

	// Start container creation + node startup in background
	go lnc.initBackground(sessionID)
}

// initBackground runs container creation and node startup, then signals completion.
// Phase 1: container creation → closes containerReady (MCP transport can proceed)
// Phase 2: node startup → closes initDone (built-in tool calls can proceed)
func (lnc *LazyNodeClient) initBackground(sessionID string) {
	defer close(lnc.initDone)

	// Phase 1: Create or get the session container.
	// Use org-scoped lifecycle if org context is set (platform mode).
	var containerName string
	var err error
	if lnc.OrgSlug != "" {
		containerName, err = EnsureOrgSessionContainer(lnc.incusClient, lnc.sessRegistry, lnc.tplRegistry, sessionID, lnc.template, lnc.limits, lnc.OrgSlug, lnc.TeamSlug)
	} else {
		containerName, err = EnsureSessionContainer(lnc.incusClient, lnc.sessRegistry, lnc.tplRegistry, sessionID, lnc.template, lnc.limits)
	}
	if err != nil {
		lnc.mu.Lock()
		lnc.containerErr = fmt.Errorf("failed to create session container: %w", err)
		lnc.initErr = lnc.containerErr
		lnc.mu.Unlock()
		close(lnc.containerReady)
		return
	}

	// Store container name and signal that the container is ready.
	// MCP transport (EnsureContainerReady) can proceed from here.
	lnc.mu.Lock()
	lnc.containerName = containerName
	lnc.mu.Unlock()
	close(lnc.containerReady)

	// Phase 2: Create and start the node client
	nc := NewNodeClient(lnc.incusClient, containerName)
	nc.Env = lnc.Env // Forward environment variables (credentials) to node
	if err := nc.Start(); err != nil {
		lnc.mu.Lock()
		lnc.initErr = fmt.Errorf("failed to start node in %q: %w", containerName, err)
		lnc.mu.Unlock()
		return
	}

	lnc.mu.Lock()
	lnc.nodeClient = nc
	lnc.initialized = true
	lnc.mu.Unlock()

	// Record initial activity so the idle watchdog doesn't immediately stop
	// a freshly-created container.
	lnc.lastActivity.Store(time.Now().Unix())
}

// configureGitCredentialHelper runs `gh auth setup-git` inside the container
// if GH_TOKEN is available in the env map. This configures git to use `gh` as
// a credential helper so that git clone/push to private repos works seamlessly.
//
// Called synchronously during container init AND after node process restarts
// to ensure the credential helper is always configured.
func configureGitCredentialHelper(client *IncusClient, containerName string, env map[string]string) {
	ghToken := env["GH_TOKEN"]
	if ghToken == "" {
		return
	}
	_, err := ExecSimpleWithEnv(client, containerName,
		[]string{"sh", "-c", "command -v gh >/dev/null 2>&1 && gh auth setup-git"},
		map[string]string{"GH_TOKEN": ghToken})
	if err != nil {
		slog.Warn("failed to run gh auth setup-git", "component", "sandbox", "container", containerName, "error", err)
	}
}

// Call proxies a tool call to the container node. If BindSession was called,
// it waits for the background init to complete. If not, it does a synchronous
// init as a fallback (safety net for code paths that skip ProcessRequest).
//
// If the call fails because the container no longer exists (pruned, destroyed,
// or template refresh), Call() automatically re-creates the container and
// retries once. This handles the race where a container is destroyed between
// init and the first (or any subsequent) tool call.
func (lnc *LazyNodeClient) Call(sessionID, toolName string, args map[string]interface{}) (json.RawMessage, error) {
	// Record activity for idle watchdog
	lnc.lastActivity.Store(time.Now().Unix())

	result, err := lnc.callOnce(sessionID, toolName, args)
	if err != nil && lnc.isContainerGone(err) {
		slog.Info("container gone for session, re-creating and retrying", "component", "sandbox",
			"session", sessionID[:min(8, len(sessionID))])
		if resetErr := lnc.resetForRetry(sessionID); resetErr != nil {
			return nil, fmt.Errorf("container gone and recovery failed: %w (original: %v)", resetErr, err)
		}
		return lnc.callOnce(sessionID, toolName, args)
	}
	return result, err
}

// callOnce performs a single tool call attempt — waits for init, then delegates
// to the underlying NodeClient.
func (lnc *LazyNodeClient) callOnce(sessionID, toolName string, args map[string]interface{}) (json.RawMessage, error) {
	// Ensure BindSession was called (idempotent — no-op if already called)
	lnc.BindSession(sessionID)

	// Wait for background init to complete.
	// initDone is guaranteed non-nil after BindSession (if sessionID != "")
	// or after Close(). Guard against nil to prevent deadlock.
	lnc.mu.Lock()
	done := lnc.initDone
	lnc.mu.Unlock()
	if done == nil {
		return nil, fmt.Errorf("lazy node client: no session bound (empty session ID)")
	}
	<-done

	lnc.mu.Lock()
	if lnc.closed {
		lnc.mu.Unlock()
		return nil, fmt.Errorf("lazy node client is closed")
	}
	if lnc.initErr != nil {
		err := lnc.initErr
		lnc.mu.Unlock()
		return nil, err
	}
	nc := lnc.nodeClient
	lnc.mu.Unlock()

	// Delegate to the NodeClient (which handles its own locking)
	return nc.Call(toolName, args)
}

// isContainerGone returns true if the error indicates the container was
// destroyed, no longer exists, or has a broken filesystem (stale overlay).
// This covers errors from:
//   - startLocked() explicit "no longer exists" check
//   - the Incus API ("Instance not found")
//   - pipe breaks from external force-stop (template refresh, admin action)
//   - stale overlay detection ("template layer not visible")
func (lnc *LazyNodeClient) isContainerGone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no longer exists") ||
		strings.Contains(msg, "Instance not found") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "write to node stdin") ||
		strings.Contains(msg, "node stdout") ||
		strings.Contains(msg, "template layer not visible")
}

// resetForRetry tears down the current state and re-initializes the container
// and node from scratch. This is called when a tool call fails because the
// container was destroyed externally. The caller must retry the call after this.
func (lnc *LazyNodeClient) resetForRetry(sessionID string) error {
	lnc.mu.Lock()

	// Close the old node client if any
	if lnc.nodeClient != nil {
		lnc.nodeClient.Close()
		lnc.nodeClient = nil
	}

	// Reset init state so initBackground can run again
	lnc.initialized = false
	lnc.initErr = nil
	lnc.containerErr = nil
	lnc.containerName = ""
	lnc.containerReady = make(chan struct{})
	lnc.initDone = make(chan struct{})

	lnc.mu.Unlock()

	// Re-run background init synchronously (we need the container before retry)
	lnc.initBackground(sessionID)

	// Check if re-init succeeded
	lnc.mu.Lock()
	err := lnc.initErr
	lnc.mu.Unlock()
	return err
}

// Close shuts down the node and optionally the container.
func (lnc *LazyNodeClient) Close() error {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.closed {
		return nil
	}
	lnc.closed = true

	if lnc.nodeClient != nil {
		lnc.nodeClient.Close()
	}

	// If BindSession was never called, initialize channels as already-closed
	// so any goroutine that later calls Call()/EnsureReady()/EnsureContainerReady()
	// unblocks immediately instead of deadlocking on a nil channel.
	if lnc.containerReady == nil {
		lnc.containerReady = make(chan struct{})
		close(lnc.containerReady)
		lnc.containerErr = fmt.Errorf("lazy node client is closed")
	}
	if lnc.initDone == nil {
		lnc.initDone = make(chan struct{})
		close(lnc.initDone)
		lnc.initErr = fmt.Errorf("lazy node client is closed")
	}

	return nil
}

// Cleanup closes the node and destroys the session container.
// Used when a session is explicitly deleted or the agent is re-created.
func (lnc *LazyNodeClient) Cleanup() {
	// If init is in progress, wait for it to finish before cleaning up
	lnc.mu.Lock()
	done := lnc.initDone
	alreadyShutdown := lnc.closed
	lnc.mu.Unlock()
	if done != nil {
		<-done
	}

	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.nodeClient != nil {
		lnc.nodeClient.Close()
		lnc.nodeClient = nil
	}

	// Skip container destruction if CleanupForShutdown already ran —
	// the container was intentionally preserved for reconnection.
	if !alreadyShutdown {
		if lnc.containerName != "" && lnc.incusClient != nil {
			// Use destroyOverlayContainer to properly unmount overlayfs
			// and clean up overlay dirs before deleting the container.
			if err := destroyOverlayContainer(lnc.incusClient, lnc.containerName); err != nil {
				slog.Warn("failed to destroy overlay container during cleanup", "component", "sandbox", "container", lnc.containerName, "error", err)
			}
		}

		if lnc.containerName != "" && lnc.sessRegistry != nil {
			// Find and remove the session registry entry for this container
			for _, entry := range lnc.sessRegistry.List() {
				if entry.ContainerName == lnc.containerName {
					if err := lnc.sessRegistry.Remove(entry.SessionID); err != nil {
						slog.Warn("failed to remove session registry entry", "component", "sandbox", "session", entry.SessionID, "error", err)
					}
					break
				}
			}
		}
	}

	lnc.initialized = false
	lnc.closed = true
}

// CleanupForShutdown stops the node process and container but preserves
// the container and its overlay for reconnection after daemon restart.
// Unlike Cleanup(), this does NOT destroy the container or remove the
// sandbox registry entry — the session persists in the session store and
// will get its container back via EnsureSessionContainer on the next tool call.
func (lnc *LazyNodeClient) CleanupForShutdown() {
	lnc.mu.Lock()
	done := lnc.initDone
	lnc.mu.Unlock()
	if done != nil {
		<-done
	}

	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.nodeClient != nil {
		lnc.nodeClient.Close()
		lnc.nodeClient = nil
	}

	// Stop the container but don't destroy it or its overlay.
	// EnsureSessionContainer will re-mount and restart it later.
	if lnc.containerName != "" && lnc.incusClient != nil {
		if lnc.incusClient.IsRunning(lnc.containerName) {
			if err := lnc.incusClient.StopInstance(lnc.containerName, true); err != nil {
				slog.Warn("failed to stop container for shutdown", "component", "sandbox", "container", lnc.containerName, "error", err)
			}
		}
	}

	lnc.initialized = false
	lnc.closed = true
}

// StopForIdle stops the node process and container to reclaim resources after
// an idle period. Unlike CleanupForShutdown, this does NOT set closed=true.
// The pool removes this client from its map, so the next tool call for the
// same session creates a fresh LazyNodeClient which calls EnsureSessionContainer
// to restart the stopped container. The container and its overlay are preserved.
func (lnc *LazyNodeClient) StopForIdle() {
	lnc.mu.Lock()
	done := lnc.initDone
	lnc.mu.Unlock()
	if done != nil {
		<-done
	}

	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.nodeClient != nil {
		lnc.nodeClient.Close()
		lnc.nodeClient = nil
	}

	// Stop the container but don't destroy it or its overlay.
	// EnsureSessionContainer will re-mount and restart it on the next tool call.
	if lnc.containerName != "" && lnc.incusClient != nil {
		if lnc.incusClient.IsRunning(lnc.containerName) {
			if err := lnc.incusClient.StopInstance(lnc.containerName, true); err != nil {
				slog.Warn("failed to stop container for idle", "component", "sandbox", "container", lnc.containerName, "error", err)
			}
		}
	}

	lnc.initialized = false
}

// IsInitialized returns whether the container and node are running.
func (lnc *LazyNodeClient) IsInitialized() bool {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()
	return lnc.initialized
}

// HasInitFailed returns true if background initialization completed with an
// error. This is a non-blocking check used by NodeClientPool.GetOrCreate()
// to discard clients that failed transiently (e.g., forkstart failure during
// template refresh) so the pool creates a fresh client that can retry.
func (lnc *LazyNodeClient) HasInitFailed() bool {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	// Only report failure if init has actually completed (initDone is closed).
	// If init is still in progress, this returns false so the pool doesn't
	// discard a client that's still starting up.
	if lnc.initDone == nil {
		return false
	}
	select {
	case <-lnc.initDone:
		return lnc.initErr != nil
	default:
		return false // still in progress
	}
}

// IsClosed returns true if the client has been closed or cleaned up.
// Used by NodeClientPool.GetOrCreate() as a safety net to detect clients that
// were destroyed by ReplaceSession (e.g., when a sub-agent switches templates
// and the parent's alias wasn't properly updated).
func (lnc *LazyNodeClient) IsClosed() bool {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()
	return lnc.closed
}

// Template returns the sandbox template this client was created with.
// Empty string means "@base" (the default base container).
func (lnc *LazyNodeClient) Template() string {
	return lnc.template
}

// GetContainerName returns the container name (empty if not yet initialized).
func (lnc *LazyNodeClient) GetContainerName() string {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()
	return lnc.containerName
}

// GetIncusClient returns the Incus client for host-side operations (e.g., snapshotting).
func (lnc *LazyNodeClient) GetIncusClient() *IncusClient {
	return lnc.incusClient
}

// GetSessionRegistry returns the session registry.
func (lnc *LazyNodeClient) GetSessionRegistry() *SessionRegistry {
	return lnc.sessRegistry
}

// GetContainerIP returns the container's bridge IPv4 address. It waits for the
// container to be ready first. This is the IP the host can use to reach services
// running inside the container (e.g., for browser_navigate in sandbox mode).
func (lnc *LazyNodeClient) GetContainerIP(sessionID string) (string, error) {
	containerName, err := lnc.EnsureContainerReady(sessionID)
	if err != nil {
		return "", fmt.Errorf("container not ready: %w", err)
	}
	return lnc.incusClient.GetContainerIPv4(containerName)
}

// EnsureNodeReady blocks until the container AND node are ready and returns the
// container name. If either failed to start, returns an error.
// Used by built-in tool calls that need both the container and the NDJSON node.
func (lnc *LazyNodeClient) EnsureNodeReady(sessionID string) (string, error) {
	lnc.BindSession(sessionID)

	lnc.mu.Lock()
	done := lnc.initDone
	lnc.mu.Unlock()
	if done == nil {
		return "", fmt.Errorf("lazy node client: no session bound (empty session ID)")
	}
	<-done

	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.initErr != nil {
		return "", lnc.initErr
	}
	return lnc.containerName, nil
}

// LastActivity returns the time of the last Call() or init completion.
// Returns zero time if no activity has been recorded.
func (lnc *LazyNodeClient) LastActivity() time.Time {
	ts := lnc.lastActivity.Load()
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// TouchActivity updates the last activity timestamp without making a tool call.
// Used by browser tools which communicate directly with Chromium inside the
// container via CDP, bypassing the node process and thus never calling Call().
// Without this, the idle watchdog would kill containers with active browser sessions.
func (lnc *LazyNodeClient) TouchActivity() {
	lnc.lastActivity.Store(time.Now().Unix())
}

// EnsureContainerReady blocks until the container is created and running,
// but does NOT wait for the astonish node process to start. Returns the
// container name and the Incus client.
//
// This is used by MCP transport creation — MCP servers run as separate
// processes inside the container via ExecNonInteractive and do not need
// the NDJSON tool server. If the container creation itself fails, returns
// an error; if only the node fails, this still succeeds.
func (lnc *LazyNodeClient) EnsureContainerReady(sessionID string) (string, error) {
	lnc.BindSession(sessionID)

	lnc.mu.Lock()
	ready := lnc.containerReady
	lnc.mu.Unlock()
	if ready == nil {
		return "", fmt.Errorf("lazy node client: no session bound (empty session ID)")
	}
	<-ready

	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.containerErr != nil {
		return "", lnc.containerErr
	}
	return lnc.containerName, nil
}

// EnsureReady satisfies ToolNodeClient. It triggers provisioning and blocks
// until the container is running. Equivalent to EnsureContainerReady but
// discards the container name (callers using the interface don't need it).
func (lnc *LazyNodeClient) EnsureReady(sessionID string) error {
	_, err := lnc.EnsureContainerReady(sessionID)
	return err
}

// StopNode stops the node process without destroying the container.
// Used when snapshotting the container (must be quiescent).
// Call RestartNode() to bring the node back up afterward.
func (lnc *LazyNodeClient) StopNode() error {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.nodeClient == nil {
		return nil
	}

	return lnc.nodeClient.Close()
}

// RestartNode restarts the node process after it was stopped.
// Re-injects the Env variables for credentials.
func (lnc *LazyNodeClient) RestartNode() error {
	lnc.mu.Lock()
	defer lnc.mu.Unlock()

	if lnc.containerName == "" {
		return fmt.Errorf("no container to restart node in")
	}

	nc := NewNodeClient(lnc.incusClient, lnc.containerName)
	nc.Env = lnc.Env
	if err := nc.Start(); err != nil {
		return fmt.Errorf("failed to restart node in %q: %w", lnc.containerName, err)
	}

	lnc.nodeClient = nc
	lnc.initialized = true
	return nil
}

// ---------------------------------------------------------------------------
// NodeClientPool — per-session container multiplexer
// ---------------------------------------------------------------------------

// NodeClientPool manages a set of LazyNodeClient instances, one per session.
// When a tool call arrives for a session that doesn't have a client yet, the
// pool creates one. This gives every chat/Studio session its own container.
//
// Fleet sessions bypass the pool — they create LazyNodeClient directly via
// wireFleetSandbox(), which is correct because each fleet session already
// has its own lifecycle.
type NodeClientPool struct {
	incusClient  *IncusClient
	backend      Backend // Phase B.3: added alongside incusClient for incremental migration
	sessRegistry *SessionRegistry
	tplRegistry  *TemplateRegistry
	template     string
	limits       *config.SandboxLimits

	mu       sync.Mutex
	clients  map[string]*LazyNodeClient
	env      map[string]string
	closed   bool
	orgSlug  string // org slug for container naming/networking (platform mode)
	teamSlug string // team slug for container naming (platform mode)

	// registryResolver, when set, resolves the team-scoped session registry
	// for a given org/team at per-session bind time. This is how platform
	// deployments route each chat session's container record into the caller's
	// own team schema instead of a single registry captured at pool-construction
	// time (which would bind every team's containers to whichever team happened
	// to trigger the first chat). Returns nil to fall back to the pool default.
	registryResolver func(orgSlug, teamSlug string) *SessionRegistry

	// sessionScopes holds per-session tenant overrides resolved from the live
	// request context (see SetSessionScope). Keyed by session ID.
	sessionScopes map[string]sessionScope
}

// sessionScope captures the tenant-resolved registry and slugs for a single
// session, resolved from the request that first touches that session.
type sessionScope struct {
	registry *SessionRegistry
	orgSlug  string
	teamSlug string
}

// NewNodeClientPool creates a pool that will create per-session LazyNodeClients
// on demand. The template parameter selects which container template to clone
// from (empty = @base).
//
// A Backend adapter is constructed internally from the supplied *IncusClient
// and registries; callers can retrieve it via GetBackend() and pass it to
// components that have been migrated to the Backend interface (Phase B.3).
func NewNodeClientPool(client *IncusClient, sessRegistry *SessionRegistry, tplRegistry *TemplateRegistry, template string, limits *config.SandboxLimits) *NodeClientPool {
	p := &NodeClientPool{
		incusClient:   client,
		sessRegistry:  sessRegistry,
		tplRegistry:   tplRegistry,
		template:      template,
		limits:        limits,
		clients:       make(map[string]*LazyNodeClient),
		sessionScopes: make(map[string]sessionScope),
	}
	// Best-effort Backend construction. Failure here is non-fatal: callers
	// that need the Backend can check GetBackend() for nil. This keeps pool
	// creation resilient in tests that pass zero-value registries.
	if client != nil && sessRegistry != nil && tplRegistry != nil {
		if b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindIncus,
			Client:     client,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: limits,
		}); err == nil {
			p.backend = b
		}
	}
	return p
}

// SetEnv sets environment variables that will be injected into all future
// LazyNodeClient instances created by this pool. Must be called before any
// GetOrCreate calls (typically at factory init time).
func (p *NodeClientPool) SetEnv(env map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = env
}

// SetOrgContext sets the org and team slugs for platform mode.
// When set, container names include the org/team slugs and containers
// are attached to the org-specific bridge network for isolation.
// Must be called before any GetOrCreate calls.
func (p *NodeClientPool) SetOrgContext(orgSlug, teamSlug string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orgSlug = orgSlug
	p.teamSlug = teamSlug
}

// OrgSlug returns the org slug set via SetOrgContext.
func (p *NodeClientPool) OrgSlug() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.orgSlug
}

// SetRegistryResolver installs a function that resolves the team-scoped session
// registry for a given org/team. Platform deployments set this so that each
// chat session's container record is persisted into the caller's own team
// schema (resolved per session from the live request context via
// SetSessionScope), rather than a single registry captured when the pool — and
// the process-wide shared chat agent — was first constructed.
func (p *NodeClientPool) SetRegistryResolver(fn func(orgSlug, teamSlug string) *SessionRegistry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registryResolver = fn
}

// SetSessionScope records the tenant identity for a session, resolved from the
// live request context (see NodeTool.getClientFromContext). If a registry
// resolver is installed, the team-scoped registry is resolved now and used for
// this session's container lifecycle so the record lands in the caller's team
// schema. This must be called before the session's first GetOrCreate* so the
// freshly created LazyNodeClient picks up the correct registry and slugs.
//
// It is a no-op once a client already exists for the session (the registry and
// slugs are immutable for the life of a container) and when both slugs are
// empty (nothing to scope — the pool default applies).
func (p *NodeClientPool) SetSessionScope(sessionID, orgSlug, teamSlug string) {
	if sessionID == "" || (orgSlug == "" && teamSlug == "") {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	// Immutable once a client (and therefore a container) exists.
	if _, ok := p.clients[sessionID]; ok {
		return
	}
	if _, ok := p.sessionScopes[sessionID]; ok {
		return
	}
	scope := sessionScope{orgSlug: orgSlug, teamSlug: teamSlug}
	if p.registryResolver != nil {
		scope.registry = p.registryResolver(orgSlug, teamSlug)
	}
	p.sessionScopes[sessionID] = scope
}

// GetOrCreate returns the LazyNodeClient for the given session ID, creating
// one if it doesn't exist yet. Thread-safe.
//
// If the cached client's background init failed (transient error), the client
// is discarded and a fresh one is created. This handles the case where
// initBackground() fails transiently (e.g., forkstart error during template
// refresh at startup) and the error gets cached permanently in initErr —
// without this check, every subsequent Call() for that session would return
// the same stale error forever.
func (p *NodeClientPool) GetOrCreate(sessionID string) *LazyNodeClient {
	return p.GetOrCreateWithTemplate(sessionID, "")
}

// GetOrCreateWithTemplate returns an existing client for the session, or
// creates a new one using the given template override. If template is empty,
// the pool's default template is used (which itself defaults to @base).
// This allows per-request template injection (e.g., team containers in chat).
func (p *NodeClientPool) GetOrCreateWithTemplate(sessionID, template string) *LazyNodeClient {
	if sessionID == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	if client, ok := p.clients[sessionID]; ok {
		// If the client's init failed or it was closed/destroyed (e.g., by
		// ReplaceSession from a sub-agent alias), discard it and fall through
		// to create a fresh one. The container itself may be fine (e.g.,
		// `incus start` works), so a retry with a new LazyNodeClient can succeed.
		if !client.HasInitFailed() && !client.IsClosed() {
			return client
		}
		slog.Info("discarding stale client for session, will retry", "component", "sandbox",
			"session", sessionID[:min(8, len(sessionID))],
			"initFailed", client.HasInitFailed(), "closed", client.IsClosed())
		delete(p.clients, sessionID)
		// Don't call client.Cleanup() here — the container may be reusable.
		// Close the node client (if any) but leave the container intact.
		if err := client.Close(); err != nil {
			slog.Warn("failed to close stale client", "component", "sandbox", "session", sessionID[:min(8, len(sessionID))], "error", err)
		}
	}

	// Use override template if non-empty, otherwise fall back to pool default
	tpl := p.template
	if template != "" {
		tpl = template
	}

	// Create a new LazyNodeClient for this session. If a per-session tenant
	// scope was resolved from the request context (platform mode), use its
	// team-scoped registry and slugs so the container record lands in the
	// caller's own team schema; otherwise fall back to the pool defaults.
	sessRegistry := p.sessRegistry
	orgSlug := p.orgSlug
	teamSlug := p.teamSlug
	if scope, ok := p.sessionScopes[sessionID]; ok {
		if scope.registry != nil {
			sessRegistry = scope.registry
		}
		orgSlug = scope.orgSlug
		teamSlug = scope.teamSlug
	}

	client := NewLazyNodeClient(p.incusClient, sessRegistry, p.tplRegistry, tpl, p.limits)
	client.Env = p.env
	client.OrgSlug = orgSlug
	client.TeamSlug = teamSlug
	p.clients[sessionID] = client
	return client
}

// Alias maps a child session ID to the same LazyNodeClient as the parent
// session. This enables sub-agents (which get fresh session IDs) to share
// the parent's sandbox container instead of creating a new one. Thread-safe.
// If the parent session has no client yet, Alias is a no-op — the child will
// create its own container via GetOrCreate on the first tool call.
func (p *NodeClientPool) Alias(childSessionID, parentSessionID string) {
	if childSessionID == "" || parentSessionID == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	parent, ok := p.clients[parentSessionID]
	if !ok || parent == nil {
		return
	}

	p.clients[childSessionID] = parent
	// Share the parent's tenant scope so the child (sub-agent) resolves the
	// same team schema if it ever creates its own client.
	if scope, ok := p.sessionScopes[parentSessionID]; ok {
		if p.sessionScopes == nil {
			p.sessionScopes = make(map[string]sessionScope)
		}
		p.sessionScopes[childSessionID] = scope
	}
}

// TouchActivity updates the last activity timestamp for a session's container
// without making a tool call. This prevents the idle watchdog from killing
// containers that are actively being used by browser tools (which communicate
// via CDP, bypassing the node process).
func (p *NodeClientPool) TouchActivity(sessionID string) {
	p.mu.Lock()
	client, ok := p.clients[sessionID]
	p.mu.Unlock()

	if ok && client != nil {
		client.TouchActivity()
	}
}

// Remove stops the node for a session and removes it from the pool.
// Does NOT destroy the sandbox container — that's TryDestroySession's
// job from the session deletion handler.
func (p *NodeClientPool) Remove(sessionID string) {
	p.mu.Lock()
	client, ok := p.clients[sessionID]
	if ok {
		delete(p.clients, sessionID)
	}
	delete(p.sessionScopes, sessionID)
	p.mu.Unlock()

	if ok && client != nil {
		if err := client.Close(); err != nil {
			slog.Warn("failed to close client during pool remove", "component", "sandbox", "session", sessionID[:min(8, len(sessionID))], "error", err)
		}
	}
}

// Cleanup destroys all session containers managed by this pool. Called on
// agent re-creation (settings change) or explicit session deletion.
func (p *NodeClientPool) Cleanup() {
	p.mu.Lock()
	p.closed = true
	clients := make(map[string]*LazyNodeClient, len(p.clients))
	for k, v := range p.clients {
		clients[k] = v
	}
	p.clients = nil
	p.sessionScopes = nil
	p.mu.Unlock()

	for _, client := range clients {
		client.Cleanup()
	}
}

// CleanupForShutdown stops all session containers without destroying them.
// Containers and overlays are preserved for reconnection after daemon restart.
func (p *NodeClientPool) CleanupForShutdown() {
	p.mu.Lock()
	p.closed = true
	clients := make(map[string]*LazyNodeClient, len(p.clients))
	for k, v := range p.clients {
		clients[k] = v
	}
	p.clients = nil
	p.mu.Unlock()

	for _, client := range clients {
		client.CleanupForShutdown()
	}
}

// GetContainerName returns the container name for a session (empty if not initialized).
func (p *NodeClientPool) GetContainerName(sessionID string) string {
	p.mu.Lock()
	client, ok := p.clients[sessionID]
	p.mu.Unlock()

	if !ok || client == nil {
		return ""
	}
	return client.GetContainerName()
}

// StopNode stops the node process for a session without destroying the container.
func (p *NodeClientPool) StopNode(sessionID string) error {
	p.mu.Lock()
	client, ok := p.clients[sessionID]
	p.mu.Unlock()

	if !ok || client == nil {
		return fmt.Errorf("no client for session %s", sessionID)
	}
	return client.StopNode()
}

// RestartNode restarts the node process for a session.
func (p *NodeClientPool) RestartNode(sessionID string) error {
	p.mu.Lock()
	client, ok := p.clients[sessionID]
	p.mu.Unlock()

	if !ok || client == nil {
		return fmt.Errorf("no client for session %s", sessionID)
	}
	return client.RestartNode()
}

// ReplaceSession tears down the existing container for a session and creates a
// new LazyNodeClient with the specified template. The next tool call for this
// session will create a container cloned from the new template. This is used by
// the use_sandbox_template tool to switch a chat session from @base to a
// project-specific template.
//
// IMPORTANT: If the session ID is an alias (e.g., a sub-agent sharing the
// parent's container), ALL session IDs pointing to the same LazyNodeClient
// are updated to the new client. Without this, the parent's pool entry would
// be left pointing at the destroyed client while only the sub-agent gets the
// new one (resulting in "lazy node client is closed" errors on the parent).
func (p *NodeClientPool) ReplaceSession(sessionID, template string) error {
	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("pool is closed")
	}

	existing, hadExisting := p.clients[sessionID]

	// Collect ALL session IDs that point to the same LazyNodeClient.
	// This handles the aliasing case: sub-agent and parent share one client.
	var aliasedIDs []string
	if hadExisting && existing != nil {
		for id, client := range p.clients {
			if client == existing {
				aliasedIDs = append(aliasedIDs, id)
			}
		}
	}

	// Create new client with the specified template and register it
	// BEFORE releasing the lock. This eliminates the race window where
	// concurrent GetOrCreate calls could find no client and create a
	// default (@base) one.
	client := NewLazyNodeClient(p.incusClient, p.sessRegistry, p.tplRegistry, template, p.limits)
	client.Env = p.env
	client.OrgSlug = p.orgSlug
	client.TeamSlug = p.teamSlug

	// Point ALL previously-aliased session IDs to the new client.
	// This ensures both the sub-agent AND the parent get the new container.
	p.clients[sessionID] = client
	for _, id := range aliasedIDs {
		if id != sessionID {
			p.clients[id] = client
		}
	}

	p.mu.Unlock()

	// Tear down existing client AFTER the new one is visible in the pool.
	// This waits for init, destroys the old container + registry entry.
	if hadExisting && existing != nil {
		existing.Cleanup()
	}

	return nil
}

// GetIncusClient returns the shared Incus client.
//
// Deprecated: Phase B.3 callers should prefer GetBackend() to remain
// backend-agnostic. This accessor is retained for components that still
// depend on Incus-specific behavior not yet exposed by the Backend
// interface (template build/refresh operations tied to the Phase A layer
// store).
func (p *NodeClientPool) GetIncusClient() *IncusClient {
	return p.incusClient
}

// GetBackend returns the Backend adapter wrapping this pool's Incus client.
// New or migrated call sites (Phase B.3) should use this in preference to
// GetIncusClient. Returns nil when the pool was constructed with
// insufficient dependencies to build a Backend (e.g., zero-value registries
// in unit tests); callers MUST handle nil.
func (p *NodeClientPool) GetBackend() Backend {
	return p.backend
}

// StartIdleWatchdog runs a background goroutine that periodically checks each
// managed LazyNodeClient for inactivity. If a client has been idle for longer
// than idleTimeout, its container is stopped (preserving overlay and registry)
// and the client is removed from the pool. The next tool call for that session
// will create a fresh LazyNodeClient which restarts the stopped container via
// EnsureSessionContainer (~200ms overlay + ~1s node startup).
//
// The watchdog checks every 60 seconds and stops when ctx is cancelled.
func (p *NodeClientPool) StartIdleWatchdog(ctx context.Context, idleTimeout time.Duration) {
	if idleTimeout <= 0 {
		return // disabled
	}

	const checkInterval = 60 * time.Second
	ticker := time.NewTicker(checkInterval)

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.stopIdleClients(idleTimeout)
			}
		}
	}()
}

// stopIdleClients finds clients that have been idle for longer than the timeout,
// stops their containers, and removes them from the pool.
func (p *NodeClientPool) stopIdleClients(idleTimeout time.Duration) {
	now := time.Now()

	// Snapshot the client map under lock
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	candidates := make(map[string]*LazyNodeClient, len(p.clients))
	for id, client := range p.clients {
		candidates[id] = client
	}
	p.mu.Unlock()

	// Check each client outside the pool lock (StopForIdle may block)
	for sessionID, client := range candidates {
		if !client.IsInitialized() {
			continue // not yet initialized, skip
		}

		lastAct := client.LastActivity()
		if lastAct.IsZero() {
			continue // no activity recorded yet
		}

		if now.Sub(lastAct) < idleTimeout {
			continue // still active
		}

		slog.Info("stopping idle container for session", "component", "sandbox",
			"session", sessionID[:min(8, len(sessionID))],
			"idle_for", now.Sub(lastAct).Round(time.Second).String())

		// Stop the container and node process (preserves overlay + registry)
		client.StopForIdle()

		// Remove from pool so the next GetOrCreate() creates a fresh client
		p.mu.Lock()
		if p.clients[sessionID] == client {
			delete(p.clients, sessionID)
		}
		p.mu.Unlock()
	}
}
