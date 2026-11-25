package astonish

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

// NodeRequest is a tool execution request received over stdin (NDJSON).
type NodeRequest struct {
	ID   string                 `json:"id"`
	Tool string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
}

// NodeResponse is the result sent back over stdout (NDJSON).
type NodeResponse struct {
	ID     string `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NodeReadyMessage is sent once on startup to signal the node is ready.
type NodeReadyMessage struct {
	Ready bool `json:"ready"`
}

// sessionVault holds the loaded in-sandbox credential store, refreshed when
// the vault file mtime changes (host re-pushes before each http_request Call).
type sessionVault struct {
	mu       sync.Mutex
	store    store.CredentialStore
	modTime  int64
	loadedOK bool
}

func (v *sessionVault) credentialStore() store.CredentialStore {
	v.mu.Lock()
	defer v.mu.Unlock()

	info, err := os.Stat(sessioncreds.VaultPath)
	if err != nil {
		if os.IsNotExist(err) {
			v.store = nil
			v.loadedOK = false
			v.modTime = 0
			return nil
		}
		// Keep last good store if stat fails transiently.
		return v.store
	}
	mtime := info.ModTime().UnixNano()
	if v.loadedOK && mtime == v.modTime {
		return v.store
	}
	loaded, err := sessioncreds.Load(sessioncreds.VaultPath)
	if err != nil {
		return v.store
	}
	v.store = loaded
	v.modTime = mtime
	v.loadedOK = true
	return v.store
}

// handleNodeCommand runs the headless tool execution server.
// It reads NDJSON tool requests from stdin, dispatches via tools.ExecuteTool(),
// and writes NDJSON results to stdout. Sequential: one request at a time.
//
// Protocol:
//
//	Startup: writes {"ready":true}\n to stdout
//	Request: {"id":"1","tool":"read_file","args":{"path":"main.go"}}\n
//	Response: {"id":"1","result":{"content":"..."}}\n
//	Error:    {"id":"1","error":"file not found"}\n
func handleNodeCommand(args []string) error {
	if len(args) > 0 && args[0] == "--internal-home-admission" {
		if len(args) != 2 || (sandbox.BotHomeAdmissionMode(args[1]) != sandbox.BotHomeAdmissionWait && sandbox.BotHomeAdmissionMode(args[1]) != sandbox.BotHomeAdmissionTry) {
			return fmt.Errorf("invalid internal admission invocation")
		}
		return sandbox.RunBotHomeAdmission(sandbox.BotHomeAdmissionMode(args[1]), os.Stdin, os.Stdout)
	}
	if len(args) > 0 && (args[0] == "--internal-scoped-home-files" || args[0] == "--internal-scoped-home-workspace") {
		// The transport supplies a scoped PVC mount on fd 9 and the expected
		// owner. These arguments are not registered as model-callable tools.
		if len(args) != 4 || (args[2] != "public" && args[2] != "private") || (args[3] != "read" && args[3] != "mutate") {
			return fmt.Errorf("invalid internal scoped file invocation")
		}
		mount := os.NewFile(9, "retained-scope")
		if mount == nil {
			return fmt.Errorf("missing retained scope descriptor")
		}
		defer mount.Close()
		needPrivate := args[2] == "private"
		home, private, err := sandbox.OpenBotHomeFileRoots(mount, args[1], needPrivate, needPrivate && args[3] == "mutate")
		if err != nil {
			return fmt.Errorf("open retained file roots: %w", err)
		}
		defer home.Close()
		if private != nil {
			defer private.Close()
		}
		if args[0] == "--internal-scoped-home-workspace" {
			return sandbox.RunBotHomeWorkspace(private, os.Stdin, os.Stdout)
		}
		return sandbox.RunBotHomeFiles(home, private, os.Stdin, os.Stdout)
	}
	if len(args) > 0 && args[0] == "--internal-scoped-home-archive" {
		if len(args) != 3 || (args[2] != "export" && args[2] != "restore") {
			return fmt.Errorf("invalid internal scoped archive invocation")
		}
		mount := os.NewFile(9, "retained-scope")
		defer mount.Close()
		home, private, err := sandbox.OpenBotHomeFileRoots(mount, args[1], true, args[2] == "restore")
		if err != nil {
			return fmt.Errorf("open retained archive roots: %w", err)
		}
		defer home.Close()
		defer private.Close()
		return sandbox.RunBotHomeArchive(home, private, os.Stdin, os.Stdout, false)
	}
	if len(args) > 0 && args[0] == "--internal-home-archive" {
		if len(args) != 2 || args[1] != "admission-held" {
			return fmt.Errorf("invalid internal archive invocation")
		}
		home, private := os.NewFile(9, "retained-home"), os.NewFile(8, "retained-private")
		defer home.Close()
		defer private.Close()
		return sandbox.RunBotHomeArchive(home, private, os.Stdin, os.Stdout, true)
	}
	if len(args) > 0 && args[0] == "--internal-home-workspace" {
		private := os.NewFile(8, "retained-private")
		defer private.Close()
		return sandbox.RunBotHomeWorkspace(private, os.Stdin, os.Stdout)
	}
	if len(args) > 0 && args[0] == "--internal-home-files" {
		// Only the backend supplies these inherited root descriptors. No ready
		// message or model dispatch is involved in the storage protocol.
		home, private := os.NewFile(9, "retained-home"), os.NewFile(8, "retained-private")
		defer home.Close()
		defer private.Close()
		return sandbox.RunBotHomeFiles(home, private, os.Stdin, os.Stdout)
	}
	ctx := context.Background()
	vault := &sessionVault{}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)

	// Inside a sandbox container, disable SSRF private-IP blocking for
	// http_request. The sandbox's own network policy (OpenShell Landlock/seccomp,
	// K8s NetworkPolicy, Docker org networks) is the security boundary —
	// the Go-level SSRF check is redundant and blocks legitimate internal API access.
	tools.SetAllowPrivateNetworks(true)

	// Increase scanner buffer for large tool args (e.g., write_file with big content)
	const maxScanSize = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanSize)

	// Signal readiness
	if err := encoder.Encode(NodeReadyMessage{Ready: true}); err != nil {
		return fmt.Errorf("failed to send ready message: %w", err)
	}

	// Main loop: read requests, dispatch, respond
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req NodeRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// Invalid JSON — respond with error if we can extract an ID
			resp := NodeResponse{Error: fmt.Sprintf("invalid request JSON: %v", err)}
			encoder.Encode(resp)
			continue
		}

		if req.ID == "" || req.Tool == "" {
			resp := NodeResponse{ID: req.ID, Error: "missing required fields: id, tool"}
			encoder.Encode(resp)
			continue
		}

		// Extract caller identity (injected by host-side NodeTool for cache scoping)
		caller := ""
		if c, ok := req.Args["_caller"].(string); ok {
			caller = c
			delete(req.Args, "_caller")
		}

		toolCtx := ctx
		if cs := vault.credentialStore(); cs != nil {
			toolCtx = store.WithCredentialStore(ctx, cs)
		}

		// Dispatch tool execution
		result, err := tools.ExecuteTool(toolCtx, req.Tool, req.Args, caller)

		resp := NodeResponse{ID: req.ID}
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Result = result
		}

		if encErr := encoder.Encode(resp); encErr != nil {
			// stdout broken — nothing we can do, exit
			return fmt.Errorf("failed to write response: %w", encErr)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stdin read error: %w", err)
	}

	// stdin closed — clean exit
	return nil
}
