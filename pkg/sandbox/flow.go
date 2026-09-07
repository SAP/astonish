package sandbox

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/tool"
)

// FlowSandboxResult holds the wrapped tools and cleanup function returned by
// SetupFlowSandbox. Callers must invoke Cleanup when the flow session ends.
type FlowSandboxResult struct {
	Tools   []tool.Tool
	Cleanup func()
}

// SetupFlowSandbox wraps internal tools with sandbox proxies for flow execution.
// Flow sessions are short-lived: the container is created lazily on first tool
// call and destroyed when the returned cleanup function is called.
//
// Backend dispatch (Phase E §11 slice 4):
//
//   - sandbox.backend=incus (default): construct a classic *NodeClientPool
//     wired to Incus and wrap tools via WrapToolsWithNode. Behaviour is
//     byte-for-byte identical to the pre-Phase-E flow.
//   - sandbox.backend=k8s | mock: construct a sandbox.Backend via
//     BackendFromAppConfig and wrap it in a ToolNodePool via
//     NewBackendPool. Tools are wrapped via WrapToolsWithPool. Flow
//     sessions land on the selected backend without any call-site
//     change; the pool's Cleanup destroys every per-session pod/mock
//     session on teardown.
//
// If sandbox is not enabled, returns the original tools and a no-op cleanup.
func SetupFlowSandbox(appCfg *config.AppConfig, internalTools []tool.Tool) (*FlowSandboxResult, error) {
	noop := &FlowSandboxResult{Tools: internalTools, Cleanup: func() {}}

	if appCfg == nil || !IsSandboxEnabled(&appCfg.Sandbox) {
		return noop, nil
	}

	kind := BackendKind(appCfg.Sandbox.BackendKind())

	switch kind {
	case BackendKindDocker, BackendKindK8s, BackendKindOpenShell, BackendKindMock, BackendKindIncus, "":
		// Backend-agnostic path: any Backend implementation feeds a
		// ToolNodePool via NewBackendPool. SetupFlowSandbox does NOT
		// own the Backend — BackendFromAppConfig constructs it and
		// may return a cleanup func that disconnects kubeconfig, etc.
		// We chain that cleanup AFTER the pool's so the pool's
		// DestroySession calls still have a live backend to talk to.
		b, backendCleanup, err := BackendFromAppConfig(appCfg)
		if err != nil {
			return nil, fmt.Errorf("sandbox runtime not available (%s): %w", kind, err)
		}
		limits := EffectiveLimits(&appCfg.Sandbox)
		pool := NewBackendPool(b, ToResourceLimits(limits))
		wrapped := WrapToolsWithPool(internalTools, pool)

		cleanup := func() {
			// Pool first: every session destroyed via backend.DestroySession
			// while the backend is still live.
			if pool != nil {
				pool.Cleanup()
			}
			if backendCleanup != nil {
				backendCleanup()
			}
		}
		slog.Debug("flow sandbox wired to backend-agnostic pool",
			"component", "sandbox", "kind", kind, "tool_count", len(wrapped))
		return &FlowSandboxResult{Tools: wrapped, Cleanup: cleanup}, nil

	default:
		return nil, fmt.Errorf("sandbox: unsupported backend kind %q for flow setup", kind)
	}
}

// ToResourceLimits converts the user-visible config.SandboxLimits shape
// (free-form memory string like "2Gi", CPU count, process cap) into the
// backend-neutral sandbox.ResourceLimits expected by Backend implementations.
//
// Memory parsing accepts the small subset Astonish's user documentation
// advertises (pure digits = MiB; suffixes K/M/G/T with optional `i`/`B`
// swallowed case-insensitively). Anything more exotic falls back to zero
// — backends already treat zero as "no caller-specified cap" and apply
// their own defaults, so the failure mode is benign.
//
// This converter is the inverse of IncusBackend.mapLimits (which goes
// ResourceLimits → SandboxLimits for the Incus orchestration path).
// Keeping both conversions local makes each call site read naturally
// without a package-level "normalise" hop.
func ToResourceLimits(l config.SandboxLimits) ResourceLimits {
	return ResourceLimits{
		CPUs:             l.CPU,
		MemoryMiB:        parseMemoryToMiB(l.Memory),
		PIDs:             l.Processes,
		RequestCPUMillis: l.Requests.CPUMillis,
		RequestMemoryMiB: l.Requests.MemoryMiB,
	}
}

// parseMemoryToMiB converts a user-facing memory string to MiB.
// Accepted forms (case-insensitive, whitespace trimmed):
//
//	""              → 0
//	"512"           → 512          (plain integer = MiB)
//	"512M" / "512MB" / "512Mi" / "512MiB" → 512
//	"2G"  / "2GB"  / "2Gi"  / "2GiB"      → 2048
//	"1T"  / "1TB"  / "1Ti"  / "1TiB"      → 1024*1024
//	"1048576K" / "1048576KB"               → 1024
//
// Anything else returns 0 — callers treat zero as "no cap requested".
// The intent is not to rival k8s resource-parser fidelity; it's to turn
// the typical operator string into a useful integer for backends that
// prefer structured limits (k8s requests/limits, mock ceilings).
func parseMemoryToMiB(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Drop optional trailing "B" (bytes) and "i" (IEC) — treat Ki == K == 1024 for our purposes.
	trimmed := strings.TrimSuffix(strings.ToLower(s), "b")
	trimmed = strings.TrimSuffix(trimmed, "i")

	// Multiplier chosen by the last alphabetic character, if any.
	multMiB := 1 // default to MiB when no suffix
	if n := len(trimmed); n > 0 && trimmed[n-1] >= 'a' && trimmed[n-1] <= 'z' {
		switch trimmed[n-1] {
		case 'k':
			// KiB / KB — fractional MiB. Round down after divide so
			// we never over-promise memory.
			if v, err := strconv.Atoi(trimmed[:n-1]); err == nil && v >= 0 {
				return v / 1024
			}
			return 0
		case 'm':
			multMiB = 1
		case 'g':
			multMiB = 1024
		case 't':
			multMiB = 1024 * 1024
		default:
			return 0
		}
		trimmed = trimmed[:n-1]
	}

	v, err := strconv.Atoi(trimmed)
	if err != nil || v < 0 {
		return 0
	}
	return v * multMiB
}
