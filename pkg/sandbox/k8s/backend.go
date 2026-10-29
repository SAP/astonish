// Package k8s provides the Kubernetes + Sysbox implementation of the
// sandbox.Backend interface. Phase C.
//
// This file owns the K8sBackend type, its Config, construction, the
// factory registration, the pure-accessor methods (Kind, Capabilities),
// diagnostics (Health), and the stubs for the operations whose
// implementation has not yet landed. Real operations live in their own
// files:
//
//   - session.go — pod lifecycle: CreateSession, StartSession, StopSession,
//                  DestroySession, SessionState, ListSessions.
//   - exec.go    — SPDY exec (Exec, ExecInteractive). TODO.
//   - files.go   — tar-over-exec (PushFile, PullFile). TODO.
//   - template.go, layer_store.go, layer_gc.go, network.go, fleet.go,
//     overlay_entrypoint.go — per §11 Phase C.
//
// Design invariants:
//
//   - Every method returns ctx.Err() before performing any state-mutating
//     work. The contract suite (sandbox.RunBackendContract) asserts this.
//   - K8sBackend MUST be safe for concurrent use.
//   - Session persistence lives in store.SandboxSessionStore (Phase A),
//     wrapped as *sandbox.SessionRegistry. K8sBackend never owns the
//     registry; it receives one fully constructed from the caller.
//   - Template metadata lives in store.SandboxTemplateStore (Phase A).
//     K8sBackend accepts it in Config for forward compatibility; slices
//     that depend on it will wire it in as they land.

package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNotImplementedYet is returned by K8sBackend methods whose
// implementation has not yet landed. Callers MAY check for this error
// with errors.Is to gate feature rollout or fall back to another backend
// during staged migrations.
var ErrNotImplementedYet = errors.New("sandbox/k8s: not implemented yet (Phase C)")

// Config bundles the K8sBackend dependencies. Fields are validated by
// New; missing required fields produce a clear error.
type Config struct {
	// Client is the Kubernetes API client. Required. Use kubernetes.NewForConfig
	// in production; tests MAY substitute k8s.io/client-go/kubernetes/fake.NewSimpleClientset.
	Client kubernetes.Interface

	// RESTConfig is the REST configuration used for subresource
	// transports (exec, attach, portforward). Required for Exec and
	// ExecInteractive in production; tests that exercise exec MUST
	// either supply a RESTConfig and a stub transport OR override the
	// executor factory via the unexported test hook.
	RESTConfig *rest.Config

	// Sessions is the session registry, backed by a
	// store.SandboxSessionStore (Phase A). Required.
	//
	// In platform mode construct with:
	//
	//     st := pgstore.NewPGSandboxSessionStore(db, teamSchema)
	//     sr := sandbox.NewSessionRegistryFromStore(st)
	//
	// In personal mode (single-pod dev):
	//
	//     sr := sandbox.NewSessionRegistry()
	Sessions *sandbox.SessionRegistry

	// Templates is the template metadata store (Phase A). Required
	// once the template operations land; accepted here for forward
	// compatibility so callers can wire it up now.
	Templates store.SandboxTemplateStore

	// Namespace is the Kubernetes namespace in which sandbox pods are
	// created. Defaults to "astonish-sandboxes".
	Namespace string

	// ControlPlaneNamespace is the namespace where the Astonish API and
	// Worker pods run. Used by EnsureOrgNetwork to allow ingress from
	// the control plane into sandbox pods. Defaults to "astonish".
	ControlPlaneNamespace string

	// RuntimeClassName is the RuntimeClass used for sandbox pods.
	// Defaults to the empty string, meaning "use the cluster default
	// runtime". Operators who run a specialised runtime (Sysbox, Kata)
	// may set this to the corresponding RuntimeClass name.
	// See docs/architecture/sandbox-backends.md §10.
	RuntimeClassName string

	// OverlayMode selects the overlayfs mount strategy in the
	// entrypoint. One of "fuse", "kernel", "auto" (see OverlayMode
	// in overlay_entrypoint.go). Defaults to OverlayModeFuse — the
	// most portable option that works without a specialised runtime.
	OverlayMode OverlayMode

	// Privileged, when true, sets securityContext.privileged=true on
	// the sandbox container. This is the simple escape hatch that
	// grants access to /dev/fuse (via in-container mknod) and the
	// CAP_SYS_ADMIN needed for the entrypoint's bind-mount step.
	// Defaults to false — production clusters should prefer the
	// unprivileged path (FuseDeviceResource + hostUsers:false).
	Privileged bool

	// HostUsers, when non-nil, sets PodSpec.hostUsers on the pod.
	// Set to pointer-to-false to request a user namespace for the pod
	// (K8s 1.33+; kernel 5.11+). Nil means "don't set the field"
	// (cluster default). See docs/architecture/sandbox-backends.md
	// §10 for the full matrix.
	HostUsers *bool

	// FuseDeviceResource is the extended resource key advertised by a
	// FUSE device plugin (e.g. "smarter-devices/fuse" for the
	// smarter-device-manager project). When non-empty, sandbox
	// containers request a quantity of 1 on their resource limits so
	// the kubelet plugs /dev/fuse into the container without
	// requiring privileged mode. When empty, no device-plugin resource
	// is requested — the entrypoint's EnsureFuseDevice mknod path is
	// the sole provider of /dev/fuse.
	FuseDeviceResource string

	// LayersPath is the node-local mount point for the RWX CephFS layer
	// store. Defaults to "/mnt/astonish-layers".
	LayersPath string

	// UppersPath is the node-local mount point for the RWX CephFS upper
	// store (persisted evicted upper layers). Defaults to
	// "/mnt/astonish-uppers".
	UppersPath string

	// LayersPVCName is the name of the PersistentVolumeClaim that backs
	// LayersPath (mounted read-only into sandbox pods). Defaults to
	// "astonish-layers".
	LayersPVCName string

	// UppersPVCName is the name of the PersistentVolumeClaim that backs
	// UppersPath (mounted RW for eviction/resume). Defaults to
	// "astonish-uppers".
	UppersPVCName string

	// SandboxImage is the container image used for sandbox pods. It
	// MUST ship the astonish-sandbox-entrypoint binary and the node
	// runtime. Defaults to
	// "ghcr.io/sap/astonish-sandbox-base:latest" (§10 "Astonish ships").
	SandboxImage string

	// MaxChainDepth caps the overlayfs lowerdir chain (default 20).
	// Must be ≤ node kernel's overlay.max_lower.
	MaxChainDepth int

	// MaxConcurrentEvictions caps parallel tar-stream evictions
	// (default 8).
	MaxConcurrentEvictions int

	// TemplatePersister, when non-nil, is invoked by BuildTemplate and
	// SaveSessionAsTemplate after the capture pipeline succeeds. It
	// gives the caller a single place to:
	//
	//   - Insert a row into store.SandboxLayerStore keyed by
	//     artifact.LayerID (content-addressed; safe to call repeatedly).
	//   - Upsert the template's top_layer_id in store.SandboxTemplateStore.
	//   - Increment the layer's ref_count under the same transaction.
	//
	// The callback is advisory: errors are returned to the caller of
	// BuildTemplate / SaveSessionAsTemplate but do NOT roll back the
	// on-CephFS layer bytes (those are content-addressed and safe to
	// leave — the GC reconciler will reap them if no row references
	// them after the grace period, per §5.12).
	//
	// Nil means "no persistence hook"; the backend returns the
	// artifact and the caller is responsible for persistence. This
	// matches the prior contract and keeps backward compatibility.
	TemplatePersister TemplatePersister
}

// TemplatePersister is the callback signature used by Config.TemplatePersister.
// It receives the just-captured artifact and optional caller-provided
// context (the session ID or template ID the artifact was built for).
// See Config.TemplatePersister for invocation rules.
type TemplatePersister func(ctx context.Context, artifact *sandbox.TemplateArtifact, persistHints TemplatePersistHints) error

// TemplatePersistHints gives the persister the breadcrumbs it needs to
// attribute the artifact to a template row without forcing the backend
// to know the template DAG. Fields are optional; persisters may ignore
// any they don't need.
type TemplatePersistHints struct {
	// TemplateID is set when BuildTemplate produced the artifact.
	TemplateID string

	// SessionID is set when SaveSessionAsTemplate produced the artifact.
	// Persisters that want to bind the new layer to a template row must
	// resolve SessionID → TemplateID themselves (via the session record
	// they own) because the backend does not keep that link.
	SessionID string

	// ParentTemplateID is propagated verbatim for persisters that want
	// to build the template DAG edge in the same transaction as the
	// layer row.
	ParentTemplateID string
}

func (c *Config) applyDefaults() {
	if c.Namespace == "" {
		c.Namespace = "astonish-sandboxes"
	}
	if c.ControlPlaneNamespace == "" {
		c.ControlPlaneNamespace = "astonish"
	}
	// RuntimeClassName intentionally defaults to the empty string
	// ("use the cluster default runtime"). Phase F drops the hard
	// dependency on Sysbox; operators who need a specialised runtime
	// opt in explicitly.
	if c.OverlayMode == "" {
		c.OverlayMode = OverlayModeFuse
	}
	if c.LayersPath == "" {
		c.LayersPath = "/mnt/astonish-layers"
	}
	if c.UppersPath == "" {
		c.UppersPath = "/mnt/astonish-uppers"
	}
	if c.LayersPVCName == "" {
		c.LayersPVCName = "astonish-layers"
	}
	if c.UppersPVCName == "" {
		c.UppersPVCName = "astonish-uppers"
	}
	if c.SandboxImage == "" {
		c.SandboxImage = "ghcr.io/sap/astonish-sandbox-base:latest"
	}
	if c.MaxChainDepth <= 0 {
		c.MaxChainDepth = 20
	}
	if c.MaxConcurrentEvictions <= 0 {
		c.MaxConcurrentEvictions = 8
	}
}

// K8sBackend is the Backend implementation backed by Kubernetes +
// Sysbox. Zero value is NOT usable; construct via New.
type K8sBackend struct {
	cfg        Config
	client     kubernetes.Interface
	restConfig *rest.Config
	sessions   *sandbox.SessionRegistry

	// execExecutorFactory constructs a remotecommand-compatible
	// Executor for a given pod/container/command. Defaults to the real
	// SPDY factory; tests override this to inject a stub.
	execExecutorFactory execExecutorFactory

	// startedAt records construction time for Health reporting.
	startedAt time.Time
}

// New constructs a K8sBackend from a Config. Returns an error if any
// required field is missing or invalid.
//
// Client is optional ONLY in contexts where the backend is constructed for
// the contract test / skeleton introspection. When Client is nil, every
// state-mutating method returns ErrNotImplementedYet (after checking
// ctx.Err()); this preserves the prior skeleton behavior during staged
// rollout. Production callers MUST supply a Client.
func New(cfg Config) (*K8sBackend, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("sandbox/k8s: Sessions registry is required")
	}
	cfg.applyDefaults()
	return &K8sBackend{
		cfg:                 cfg,
		client:              cfg.Client,
		restConfig:          cfg.RESTConfig,
		sessions:            cfg.Sessions,
		execExecutorFactory: defaultExecExecutorFactory,
		startedAt:           time.Now().UTC(),
	}, nil
}

// init registers K8sBackend with sandbox.NewBackend. Importing
// pkg/sandbox/k8s makes BackendKindK8s available to the factory.
//
// Production path (Phase D):
//
//	sandbox.NewBackend(sandbox.BackendFactoryConfig{
//	    Kind:     sandbox.BackendKindK8s,
//	    Sessions: sessRegistry,
//	    K8s:      cfg.Sandbox.Kubernetes,  // YAML sub-config
//	})
//
// The factory:
//   - Loads a *rest.Config via LoadRESTConfig(fc.K8s).
//   - Builds a clientset.
//   - Translates fc.K8s's YAML-friendly fields into a k8s.Config.
//   - Invokes New.
//
// Tests that just want a skeleton backend (no Kubernetes calls) can call
// k8s.New directly with a nil or fake Client and no K8s struct.
func init() {
	sandbox.RegisterBackendFactory(sandbox.BackendKindK8s, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		if fc.Sessions == nil {
			return nil, errors.New("sandbox/k8s: BackendFactoryConfig.Sessions is required")
		}
		cfg := Config{
			Sessions:              fc.Sessions,
			Namespace:             fc.K8s.Namespace,
			ControlPlaneNamespace: fc.K8s.ControlPlaneNamespace,
			RuntimeClassName:      fc.K8s.RuntimeClassName,
			SandboxImage:          fc.K8s.SandboxImage,
			LayersPVCName:         fc.K8s.LayersPVCName,
			UppersPVCName:         fc.K8s.UppersPVCName,
			// Phase F: portable overlay knobs.
			OverlayMode:        OverlayMode(fc.K8s.OverlayMode),
			Privileged:         fc.K8s.PrivilegedPods,
			HostUsers:          fc.K8s.HostUsers,
			FuseDeviceResource: fc.K8s.FuseDeviceResource,
		}
		// Only attempt cluster connectivity when the caller supplied an
		// explicit signal: InCluster=true, a KubeconfigPath, or
		// $KUBECONFIG in the environment. Without any of these, fall
		// back to the client-less skeleton so that smoke tests, unit
		// tests, and binaries that merely register the backend don't
		// need to stub a kubeconfig.
		if hasK8sConnectivitySignal(fc.K8s) {
			client, restCfg, err := NewClientFromOptions(LoadConfigOptions{
				KubeconfigPath: fc.K8s.KubeconfigPath,
				Context:        fc.K8s.Context,
				InCluster:      fc.K8s.InCluster,
				QPS:            fc.K8s.QPS,
				Burst:          fc.K8s.Burst,
			})
			if err != nil {
				return nil, fmt.Errorf("sandbox/k8s: load cluster config: %w", err)
			}
			cfg.Client = client
			cfg.RESTConfig = restCfg
		}
		return New(cfg)
	})
}

// hasK8sConnectivitySignal reports whether the caller has asked us to
// connect to a real cluster. See the factory comment for rationale.
func hasK8sConnectivitySignal(kc config.SandboxKubernetesConfig) bool {
	if kc.InCluster || kc.KubeconfigPath != "" {
		return true
	}
	// Honour the traditional $KUBECONFIG env var so that CLI invocations
	// like `KUBECONFIG=... astonish ...` light up the k8s backend.
	if v, ok := osLookupEnv("KUBECONFIG"); ok && v != "" {
		return true
	}
	// In-cluster token presence also counts; callers running inside a
	// Pod should not need to set InCluster=true explicitly.
	if _, statErr := osStat("/var/run/secrets/kubernetes.io/serviceaccount/token"); statErr == nil {
		return true
	}
	return false
}

// Test seams: these indirections let tests stub env / fs probing.
var (
	osLookupEnv = os.LookupEnv
	osStat      = func(name string) (osFileInfo, error) {
		fi, err := os.Stat(name)
		if err != nil {
			return nil, err
		}
		return fi, nil
	}
)

// osFileInfo is the minimal contract the factory needs from an fs probe;
// kept unexported so it doesn't leak through the package API. os.FileInfo
// satisfies this by construction.
type osFileInfo interface {
	Name() string
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

// Kind returns BackendKindK8s.
func (b *K8sBackend) Kind() sandbox.BackendKind {
	return sandbox.BackendKindK8s
}

func (b *K8sBackend) ArtifactReadsConfined() bool { return b != nil && b.sessions != nil }
func (b *K8sBackend) PullArtifact(ctx context.Context, sessionID, path string) (io.ReadCloser, error) {
	return b.PullFile(ctx, sessionID, path)
}

// ServerArchitecture returns the architecture of the Kubernetes cluster
// nodes (normalized). In homogeneous clusters this matches the pods that
// will be scheduled. We conservatively return "amd64" here; real deployments
// that need arm64 can explicitly set Architecture in the BaseConfig via the UI.
func (b *K8sBackend) ServerArchitecture() string {
	// Touch imports that this file provides for sibling .go files in the
	// k8s package (the build environment here sometimes typechecks files
	// in isolation). This keeps `make build` and CI green everywhere.
	_ = bytes.Buffer{}
	_ = json.RawMessage{}
	_ = io.EOF
	_ = slog.LevelInfo
	_ = http.MethodGet
	_ = filepath.Separator
	_ = sort.Strings
	_ = strconv.Itoa(0)
	_ = strings.TrimSpace
	_ = sync.Mutex{}
	_ = appsv1.Deployment{}
	_ = corev1.Pod{}
	_ = metav1.ObjectMeta{}
	_ = watch.Event{}
	_ = clientcmd.BuildConfigFromFlags
	return "amd64"
}

// Capabilities advertises the final (post-implementation) feature set.
// Flags are truthful about the backend's *design*; callers that need a
// runtime readiness signal should use Health.
func (b *K8sBackend) Capabilities() sandbox.BackendCapabilities {
	return sandbox.BackendCapabilities{
		Kind:                 sandbox.BackendKindK8s,
		SupportsLiveEvict:    true,
		SupportsFastClone:    true,
		SupportsPortExpose:   true,
		SupportsOrgIsolation: true,
	}
}

// Health reports the backend's readiness. Honours ctx cancellation.
//
// When a real Client is configured, Health issues a lightweight
// server-version probe to verify API-server connectivity. When Client is
// nil (skeleton / misconfigured backend), Health reports unhealthy with
// a descriptive reason.
//
// Future slices will extend Health to verify Sysbox RuntimeClass
// existence, CephFS mount readiness, and other backend prerequisites.
func (b *K8sBackend) Health(ctx context.Context) (*sandbox.BackendHealth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	details := map[string]string{
		"namespace":     b.cfg.Namespace,
		"runtime_class": b.cfg.RuntimeClassName,
		"layers_path":   b.cfg.LayersPath,
		"uppers_path":   b.cfg.UppersPath,
		"started_at":    b.startedAt.Format(time.RFC3339),
	}

	if b.client == nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    "k8s backend: no Kubernetes client configured",
			CheckedAt: time.Now().UTC(),
			Details:   details,
		}, nil
	}

	version, err := b.client.Discovery().ServerVersion()
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("k8s API-server unreachable: %v", err),
			CheckedAt: time.Now().UTC(),
			Details:   details,
		}, nil
	}
	details["server_version"] = version.GitVersion
	details["server_platform"] = version.Platform
	return &sandbox.BackendHealth{
		Healthy:   true,
		CheckedAt: time.Now().UTC(),
		Details:   details,
	}, nil
}

// ---------------------------------------------------------------------------
// Exec and file I/O — Exec/ExecInteractive live in exec.go; PushFile /
// PullFile live in files.go.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Templates — BuildTemplate / SaveSessionAsTemplate / RefreshTemplate /
// DeleteTemplate live in template.go.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Networking — EnsureOrgNetwork / DeleteOrgNetwork / ExposePort /
// UnexposePort live in network.go.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fleet — EnsureFleetContainer lives in fleet.go.
// ---------------------------------------------------------------------------
