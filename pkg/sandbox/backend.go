// Package sandbox — SandboxBackend interface (Phase B.1).
//
// This file defines the runtime-backend abstraction described in
// docs/architecture/sandbox-backends.md §3. It was the first of several Phase
// B slices; B.1 ONLY introduces the interface + shared value types. No existing
// code is rewired, no Incus code is moved, no call sites are changed. Behavior
// is identical before and after this commit.
//
// Subsequent slices:
//   - B.2 ✅: pkg/sandbox/incus/ subpackage created with *IncusClient and its
//            close dependencies. IncusBackend adapter (pkg/sandbox/incus_backend.go)
//            satisfies this interface by delegating to existing sandbox helpers.
//   - B.3 ✅: Backend factory (sandbox.NewBackend), NodeClientPool.GetBackend()
//            accessor, and Backend contract test. Per-call-site migration to
//            the Backend interface is an incremental follow-on gated on
//            Phase A (template/layer-store semantics) and new Backend methods
//            for below-abstraction concerns (direct dial, template-container
//            PTY). Existing *IncusClient consumers continue to work via the
//            additive shim layer.
//   - B.4 ✅: MockBackend added in pkg/sandbox/mock, registered with
//            sandbox.NewBackend via RegisterBackendFactory hook; Backend
//            contract helper promoted out of _test.go so external packages
//            can invoke it. MockBackend runs clean through
//            RunBackendContract.
//   - B.5 ✅: External callers migrated to import pkg/sandbox/incus
//            directly; public shim files (shims_incus.go, shims_incus_ext.go)
//            deleted; only internally-used names kept as aliases in
//            pkg/sandbox/incus_aliases.go (documented as an internal
//            bridge, not a public API surface).
//
// Scope notes:
//   - Types in this file are deliberately backend-neutral. Backend-specific
//     handles (Incus snapshot names, K8s pod names) belong in an opaque
//     BackendRef field, not in the public shape.
//   - The interface is intentionally *narrower* than the union of existing
//     pkg/sandbox free functions. Higher-level orchestration (scope-aware
//     default template resolution, per-chat advisory locks, NodeClientPool,
//     etc.) lives in calling code OVER this interface, not inside it.
//   - ChatEventJournal (Phase A) and LayerStore (Phase A) remain separate
//     interfaces used by the call graph around Backend; they are NOT methods
//     on Backend because they are pure persistence concerns.

package sandbox

import (
	"context"
	"io"
	"os"
	"time"
)

// Backend is the runtime abstraction over the sandbox tier. Implementations:
//   - IncusBackend  (pkg/sandbox, Phase B.2): LXC via Incus SDK; overlayfs
//     fast-clone; used by personal mode and platform deployments with
//     sandbox.backend=incus. Lives in pkg/sandbox (not pkg/sandbox/incus) to
//     avoid an import cycle: the adapter delegates to orchestration helpers
//     (EnsureSessionContainer, DestroyForSession, etc.) that live in
//     pkg/sandbox and already import pkg/sandbox/incus for *IncusClient.
//   - K8sSandboxBackend (pkg/sandbox/k8s, Phase C): Kubernetes pods with the
//     Sysbox runtime; CephFS-backed content-addressed layer store; used by
//     platform deployments with sandbox.backend=k8s.
//   - MockBackend (pkg/sandbox/mock, Phase B.4): in-memory for tests.
//
// Implementations MUST be safe for concurrent use. Each call that returns a
// stream (ExecInteractive) transfers ownership of that stream to the caller
// and is not shared across goroutines.
type Backend interface {
	// ----- Session lifecycle (§3.1) -----------------------------------

	// CreateSession materialises a new sandbox for a chat/fleet session.
	// Idempotent on SessionID: a second call with the same SessionID
	// returns the existing session without recreation.
	CreateSession(ctx context.Context, spec SessionSpec) (*Session, error)

	// StartSession resumes a stopped/evicted session. For K8sBackend this
	// may recreate a pod and re-mount the persisted upper layer from
	// CephFS; for IncusBackend it re-mounts the overlay and starts the
	// container. Must be no-op (no error) if the session is already
	// running.
	StartSession(ctx context.Context, sessionID string) error

	// StopSession pauses a running session without destroying its data.
	// For K8sBackend: streams the upper layer to CephFS then deletes the
	// pod (§5.5). For IncusBackend: stops the container but leaves the
	// overlay mounted (today's idle path). Must be no-op if already
	// stopped.
	StopSession(ctx context.Context, sessionID string) error

	// DestroySession permanently removes the session and its writable
	// layer. Idempotent: calls against non-existent sessions must
	// succeed without error.
	DestroySession(ctx context.Context, sessionID string) error

	// SessionState returns the current backend-observed state of the
	// session, or SessionStateGone if the session has been destroyed or
	// never existed.
	SessionState(ctx context.Context, sessionID string) (SessionState, error)

	// WaitForSessionReady blocks until the session's container/pod is
	// Running and ready to accept exec commands. Returns an error if
	// the context is cancelled, the timeout expires, or the session
	// enters a terminal failure state (e.g., pod CrashLoopBackOff).
	//
	// On Incus this returns almost immediately (containers start in
	// <1s). On K8s this polls pod phase until Running (image pull,
	// PVC bind, overlay composition may take 10-60s on first use).
	//
	// Callers that need to exec into a session immediately after
	// CreateSession MUST call this first.
	WaitForSessionReady(ctx context.Context, sessionID string) error

	// ListSessions returns all sessions visible to the backend matching
	// the filter. Fleet sessions and normal chat sessions share the same
	// list; callers filter by Type.
	ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error)

	// ----- Exec and file I/O (§3.2) -----------------------------------

	// Exec runs a command non-interactively and captures stdout/stderr +
	// exit code. Blocks until the command exits or ctx is cancelled.
	Exec(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error)

	// ExecInteractive starts a PTY-attached process and returns a stream
	// the caller reads/writes. The caller MUST call Close on the returned
	// stream to release backend resources.
	ExecInteractive(ctx context.Context, sessionID string, opts PTYSpec) (ExecStream, error)

	// ExecStreaming starts a non-interactive process with streaming
	// bidirectional stdin/stdout but NO PTY allocation. Unlike Exec, the
	// process is long-running and the caller reads/writes incrementally.
	// Unlike ExecInteractive, no terminal processing (echo, CR/LF
	// translation, signal interpretation) is applied — making this
	// suitable for machine-to-machine protocols such as MCP (JSON-RPC
	// over stdio) and NDJSON.
	//
	// The caller MUST call Close on the returned stream to release
	// backend resources. SeparateStderr in the spec, when non-nil,
	// receives the process's stderr on a dedicated stream instead of
	// being merged into the stdout reader.
	ExecStreaming(ctx context.Context, sessionID string, opts ExecStreamSpec) (ExecStream, error)

	// PushFile writes content to a path inside the sandbox with the given
	// mode. Implementations MAY create missing parent directories.
	PushFile(ctx context.Context, sessionID, path string, content io.Reader, mode os.FileMode) error

	// PullFile reads a file from the sandbox. The returned ReadCloser MUST
	// be closed by the caller.
	PullFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error)

	// ----- Templates (§3.3) -------------------------------------------
	//
	// These are backend-tier operations: building, capturing, refreshing,
	// deleting a template's *bytes*. The template DAG metadata
	// (scope, owner, parent) is owned by SandboxTemplateStore (Phase A)
	// and wired by calling code.

	// BuildTemplate creates a new template from scratch by provisioning a
	// throwaway session, running the provided bootstrap steps, then
	// capturing the session as a template layer. Returns the content
	// address (SHA-256) of the produced layer and its size in bytes.
	BuildTemplate(ctx context.Context, spec TemplateBuildSpec) (*TemplateArtifact, error)

	// SaveSessionAsTemplate captures the upper layer of a running session
	// as a new immutable layer. Returns the layer's content address. This
	// is the "save-as" primitive that must be fast (<5s for typical
	// deltas, §6).
	SaveSessionAsTemplate(ctx context.Context, sessionID string) (*TemplateArtifact, error)

	// RefreshTemplate re-runs the template's build steps to incorporate
	// upstream package updates. Returns a new layer artifact; callers
	// decide whether to cascade the new layer to dependent templates
	// (no automatic cascade).
	RefreshTemplate(ctx context.Context, templateID string) (*TemplateArtifact, error)

	// DeleteTemplate removes a template's bytes. MUST refuse to delete if
	// sessions or child templates reference the template, unless force
	// is set. Ref-count maintenance is the caller's responsibility (via
	// LayerStore).
	DeleteTemplate(ctx context.Context, templateID string, force bool) error

	// ----- Networking (§3.4) ------------------------------------------

	// EnsureOrgNetwork provisions the org-scoped network primitives.
	// Incus:  per-org bridge + profile.
	// K8s:    NetworkPolicy matching org labels.
	// Idempotent: safe to call on every org activation.
	EnsureOrgNetwork(ctx context.Context, orgSlug string) error

	// DeleteOrgNetwork removes org-scoped network primitives. Called on
	// org deletion. Idempotent.
	DeleteOrgNetwork(ctx context.Context, orgSlug string) error

	// ExposePort opens an inbound route to a port inside the session
	// container. Returns the externally visible address.
	ExposePort(ctx context.Context, sessionID string, port int, proto string) (*ExposedAddr, error)

	// UnexposePort closes a previously-exposed port. Idempotent.
	UnexposePort(ctx context.Context, sessionID string, port int) error

	// ----- Fleet (§3.5) -----------------------------------------------

	// EnsureFleetContainer creates (if absent) a long-running fleet
	// container from a template. Called repeatedly by the fleet
	// controller; MUST be idempotent and cheap on the hot path.
	EnsureFleetContainer(ctx context.Context, spec FleetSpec) (*Session, error)

	// ----- Diagnostics (§3.6) -----------------------------------------

	// Capabilities reports backend-specific features (e.g., whether the
	// backend supports live evict, whether it exposes a VNC port, etc.)
	// so the UI can gate features without hard-coding backend identity.
	Capabilities() BackendCapabilities

	// Health performs a connectivity/readiness check and returns a
	// structured status. Used by the /healthz endpoint.
	Health(ctx context.Context) (*BackendHealth, error)

	// Kind returns a stable identifier for this backend implementation
	// ("docker", "k8s", "openshell", "mock"). Useful for logging/metrics labels.
	Kind() BackendKind

	// ServerArchitecture returns the native architecture ("amd64" or "arm64")
	// of containers/pods managed by this backend. Used during base template
	// builds (especially for architecture-specific packages such as the
	// KasmVNC .deb) when the caller does not explicitly specify one.
	ServerArchitecture() string
}

// ---------------------------------------------------------------------------
// Shared value types (backend-neutral)
// ---------------------------------------------------------------------------

// BackendKind identifies a backend implementation. It is deliberately a
// typed string so callers can switch on it but so we can also log/format it.
type BackendKind string

const (
	BackendKindIncus     BackendKind = "incus"
	BackendKindDocker    BackendKind = "docker"
	BackendKindK8s       BackendKind = "k8s"
	BackendKindOpenShell BackendKind = "openshell"
	BackendKindMock      BackendKind = "mock"
)

// BaseTemplateID is the canonical template identifier for the default base
// layer. All backends treat an empty SessionSpec.TemplateID as equivalent to
// BaseTemplateID. For K8s, the seed Job populates layers/@base/rootfs; for
// Incus, the template registry holds @base as the root of the clone tree.
const BaseTemplateID = "@base"

// SessionType distinguishes the two long-running session flavors.
type SessionType string

const (
	SessionTypeChat  SessionType = "chat"  // interactive chat / Studio session
	SessionTypeFleet SessionType = "fleet" // long-running fleet worker
)

// SessionState is the public observable state of a session container.
// Backends map their internal states to these five values. SessionStateGone
// is the terminal/absent state (destroyed or never existed).
type SessionState string

const (
	SessionStateCreating SessionState = "creating"
	SessionStateRunning  SessionState = "running"
	SessionStateStopped  SessionState = "stopped" // stopped but data preserved
	SessionStateEvicting SessionState = "evicting"
	SessionStateResuming SessionState = "resuming"
	SessionStateGone     SessionState = "gone" // destroyed or never existed
)

// Session describes a materialised sandbox container. BackendRef is an
// opaque, backend-specific handle (Incus: container name; K8s: pod name).
// Callers MUST NOT parse it.
type Session struct {
	SessionID  string            `json:"session_id"`
	Type       SessionType       `json:"type"`
	TemplateID string            `json:"template_id"`
	OrgSlug    string            `json:"org_slug,omitempty"`
	TeamSlug   string            `json:"team_slug,omitempty"`
	State      SessionState      `json:"state"`
	BackendRef string            `json:"backend_ref"`
	Labels     map[string]string `json:"labels,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	LastActive time.Time         `json:"last_active,omitempty"`
}

// SessionSpec is the CreateSession argument bundle.
type SessionSpec struct {
	SessionID string      `json:"session_id"` // caller-chosen UUID
	Type      SessionType `json:"type"`
	// TemplateID identifies the template/layer to use as the session's
	// base filesystem. Empty string is normalised to BaseTemplateID
	// ("@base") by all backend implementations — callers need not set it
	// explicitly for sessions using the default base layer.
	TemplateID   string   `json:"template_id"`
	OrgSlug      string   `json:"org_slug,omitempty"`
	TeamSlug     string   `json:"team_slug,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	LayerChain   []string `json:"layer_chain"`              // resolved via store.SandboxTemplateStore.Resolve()
	UpperLayerID string   `json:"upper_layer_id,omitempty"` // resume: previously-evicted upper
	// Image is the container image to use for this session. When non-empty,
	// the OpenShell backend uses it instead of the global SandboxImage config.
	// K8s and Incus backends ignore this field (they use LayerChain).
	Image  string            `json:"image,omitempty"`
	Limits ResourceLimits    `json:"limits"`
	Labels map[string]string `json:"labels,omitempty"`
	// Env is injected into the sandbox container at create time (OpenShell/K8s).
	// Incus fleet sessions inject via LazyNodeClient.Env instead.
	Env map[string]string `json:"env,omitempty"`
	// NetworkAllowEndpoints are extra OpenShell L7 allow rules merged into the
	// create-time sandbox policy (in addition to YAML presets). Populated from
	// platform/org/team NetworkPolicyAllow stores so the first CONNECT to
	// approved hosts (e.g. **.cloud.sap) succeeds without a fail-then-PreSeed cycle.
	NetworkAllowEndpoints []NetworkAllowEndpoint `json:"network_allow_endpoints,omitempty"`
}

// NetworkAllowEndpoint is a host:port pair allowed for sandbox egress at create time.
type NetworkAllowEndpoint struct {
	Host string `json:"host"`
	Port uint32 `json:"port"`
}

// SessionFilter narrows ListSessions queries. Zero-value fields are ignored.
type SessionFilter struct {
	Type     SessionType
	OrgSlug  string
	TeamSlug string
	State    SessionState
}

// ResourceLimits caps a session's resource use. Zero means "no backend-enforced
// limit"; the backend MAY still impose hard ceilings from its own config.
type ResourceLimits struct {
	CPUs      int   `json:"cpus,omitempty"`       // whole CPU count (ceiling)
	MemoryMiB int   `json:"memory_mib,omitempty"` // memory ceiling in MiB
	DiskMiB   int   `json:"disk_mib,omitempty"`   // upper-layer quota
	PIDs      int   `json:"pids,omitempty"`       // process count cap
	TimeoutS  int64 `json:"timeout_s,omitempty"`  // hard wall-clock timeout

	// RequestCPUMillis is the K8s scheduler CPU reservation in millicores.
	// On K8s, pods request this from the scheduler (the "idle floor").
	// Zero means "auto-derive from CPUs" using a built-in ratio.
	// Ignored by Incus.
	RequestCPUMillis int `json:"request_cpu_millis,omitempty"`

	// RequestMemoryMiB is the K8s scheduler memory reservation in MiB.
	// Zero means "auto-derive from MemoryMiB" using a built-in ratio.
	// Ignored by Incus.
	RequestMemoryMiB int `json:"request_memory_mib,omitempty"`
}

// ExecSpec configures a non-interactive Exec call.
type ExecSpec struct {
	Command []string          `json:"command"`
	WorkDir string            `json:"work_dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Stdin   io.Reader         `json:"-"` // optional
}

// ExecResult is the outcome of a non-interactive Exec.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
}

// PTYSpec configures an interactive PTY exec.
type PTYSpec struct {
	Command []string          `json:"command"`
	WorkDir string            `json:"work_dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Rows    int               `json:"rows"` // default 24
	Cols    int               `json:"cols"` // default 80

	// SeparateStderr, when non-nil, receives stderr on a separate stream
	// instead of being merged into stdout. Required for MCP over stdio.
	SeparateStderr io.Writer `json:"-"`
}

// ExecStreamSpec configures a streaming non-interactive exec (no PTY).
// Used for machine-to-machine protocols (MCP JSON-RPC, NDJSON) that
// need a long-running bidirectional stdin/stdout stream without terminal
// processing.
type ExecStreamSpec struct {
	Command []string          `json:"command"`
	WorkDir string            `json:"work_dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// SeparateStderr, when non-nil, receives the process's stderr on a
	// dedicated writer instead of being merged into the stdout reader.
	// Critical for protocols that parse stdout as structured data.
	SeparateStderr io.Writer `json:"-"`
}

// ExecStream is a bidirectional PTY stream. Callers read Stdout, write
// Stdin, MAY call Resize on SIGWINCH, MUST call Close to release
// resources, and call Wait to get the exit code.
type ExecStream interface {
	io.Reader // reads from process stdout (PTY merged by default)
	io.Writer // writes to process stdin
	Resize(rows, cols int) error
	Wait() (int, error) // blocks until process exits
	Close() error
}

// TemplateBuildSpec describes how to build a new template.
type TemplateBuildSpec struct {
	TemplateID   string   `json:"template_id"`
	ParentLayers []string `json:"parent_layers"` // full inherited chain
	// Steps are shell commands executed inside a throwaway container on
	// top of ParentLayers. The container's final upper layer becomes the
	// template's top layer.
	Steps []string `json:"steps"`
	// Labels are attached to the build container for debugging.
	Labels map[string]string `json:"labels,omitempty"`
}

// TemplateArtifact is the output of a template build or session-save.
type TemplateArtifact struct {
	LayerID     string    `json:"layer_id"` // sha256 hex of canonical tar
	ParentLayer string    `json:"parent_layer,omitempty"`
	SizeBytes   int64     `json:"size_bytes"`
	CephFSPath  string    `json:"cephfs_path,omitempty"` // mount path on the RWX PVC; field name is historical — filesystem-agnostic
	CreatedAt   time.Time `json:"created_at"`
}

// ExposedAddr is the externally-visible address of an exposed port.
type ExposedAddr struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	URL      string `json:"url,omitempty"` // full URL if the backend exposes HTTP
}

// FleetSpec describes a fleet container.
type FleetSpec struct {
	FleetKey   string `json:"fleet_key"`
	TemplateID string `json:"template_id"`
	OrgSlug    string `json:"org_slug"`
	TeamSlug   string `json:"team_slug"`
	// Image is the container image to use for this fleet container.
	// When non-empty, the OpenShell backend uses it instead of the global
	// SandboxImage config. K8s and Incus backends ignore this field.
	Image  string            `json:"image,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Limits ResourceLimits    `json:"limits"`
}

// BackendCapabilities are feature flags the UI may query to gate controls.
type BackendCapabilities struct {
	Kind                  BackendKind `json:"kind"`
	SupportsLiveEvict     bool        `json:"supports_live_evict"`
	SupportsFastClone     bool        `json:"supports_fast_clone"`
	SupportsPortExpose    bool        `json:"supports_port_expose"`
	SupportsOrgIsolation  bool        `json:"supports_org_isolation"`
	MaxConcurrentSessions int         `json:"max_concurrent_sessions,omitempty"` // 0 = no advertised cap
}

// BackendHealth is a structured health probe result.
type BackendHealth struct {
	Healthy   bool              `json:"healthy"`
	Reason    string            `json:"reason,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
	Details   map[string]string `json:"details,omitempty"` // backend-specific diagnostics
}
