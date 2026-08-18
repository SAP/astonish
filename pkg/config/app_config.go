package config

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	General             GeneralConfig              `yaml:"general"`
	WebServers          map[string]WebServerConfig `yaml:"web_servers,omitempty" json:"web_servers,omitempty"`
	PerplexityWebSearch PerplexityWebSearchConfig  `yaml:"perplexity_web_search,omitempty" json:"perplexity_web_search,omitempty"`
	Providers           map[string]ProviderConfig  `yaml:"providers"`
	Chat                ChatConfig                 `yaml:"chat,omitempty"`
	Sessions            SessionConfig              `yaml:"sessions,omitempty"`
	Memory              MemoryConfig               `yaml:"memory,omitempty"`
	Storage             StorageConfig              `yaml:"storage,omitempty"`
	Daemon              DaemonConfig               `yaml:"daemon,omitempty"`
	Channels            ChannelsConfig             `yaml:"channels,omitempty"`
	Scheduler           SchedulerConfig            `yaml:"scheduler,omitempty"`
	Browser             BrowserAppConfig           `yaml:"browser,omitempty"`
	SubAgents           SubAgentAppConfig          `yaml:"sub_agents,omitempty"`
	Skills              SkillsConfig               `yaml:"skills,omitempty"`
	AgentIdentity       AgentIdentityConfig        `yaml:"agent_identity,omitempty"`
	CodeIntel           CodeIntelConfig            `yaml:"codeintel,omitempty" json:"codeintel,omitempty"`
	Sandbox             SandboxConfig              `yaml:"sandbox,omitempty"`
	Security            SecurityConfig             `yaml:"security,omitempty"`
}

type CodeIntelConfig struct {
	Enabled     *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	LibraryPath string `yaml:"library_path,omitempty" json:"library_path,omitempty"`
}

func (c CodeIntelConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// SandboxConfig controls the session container isolation system.
// Types are defined here (not in pkg/sandbox) because this package owns
// YAML deserialization. pkg/sandbox imports these types for runtime use.
type SandboxConfig struct {
	Enabled    *bool              `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Privileged *bool              `yaml:"privileged,omitempty" json:"privileged,omitempty"` // nil = default (unprivileged); set true for nested LXC
	Limits     SandboxLimits      `yaml:"limits,omitempty" json:"limits,omitempty"`
	Network    string             `yaml:"network,omitempty" json:"network,omitempty"`
	Prune      SandboxPruneConfig `yaml:"prune,omitempty" json:"prune,omitempty"`

	// Backend selects the sandbox execution implementation. Accepted
	// values:
	//   - ""      → "incus" (default; backward-compatible)
	//   - "incus" → local Incus daemon (docs/architecture/sandbox-backends.md §4.1)
	//   - "k8s"   → Kubernetes (portable overlay strategy; see
	//                docs/architecture/sandbox-backends.md §4.2 + §10.
	//                Phase F: fuse-overlayfs by default, kernel overlay
	//                or auto fallback; Sysbox optional, not required.)
	//   - "mock"  → in-memory mock (tests only)
	//
	// When "k8s" is selected, the Kubernetes sub-config below is
	// consulted. When any other value is selected, the Kubernetes
	// sub-config is ignored.
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`

	// Kubernetes holds backend-specific settings used when Backend == "k8s".
	// Fields have sensible defaults; operators typically only set
	// KubeconfigPath (or InCluster) and SandboxImage.
	Kubernetes SandboxKubernetesConfig `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`

	// OpenShell holds backend-specific settings used when Backend == "openshell".
	// The OpenShell backend delegates sandbox lifecycle to NVIDIA's OpenShell
	// gateway (ghcr.io/nvidia/openshell/gateway) via its gRPC API.
	OpenShell SandboxOpenShellConfig `yaml:"openshell,omitempty" json:"openshell,omitempty"`
}

// SandboxOpenShellConfig captures operator-tunable knobs for the OpenShell
// backend. The gateway is deployed separately (NVIDIA's Helm chart) and this
// config tells Astonish how to connect to it.
type SandboxOpenShellConfig struct {
	// GatewayAddr is the gRPC endpoint of the OpenShell gateway.
	// Example: "openshell.openshell.svc.cluster.local:8080"
	GatewayAddr string `yaml:"gateway_addr,omitempty" json:"gateway_addr,omitempty"`

	// GatewayTLS enables TLS for the gRPC connection to the gateway.
	// When true, the connection uses TLS with optional mTLS (see
	// ClientCertPath/ClientKeyPath). Default: true.
	GatewayTLS *bool `yaml:"gateway_tls,omitempty" json:"gateway_tls,omitempty"`

	// ClientCertPath is the path to the mTLS client certificate for
	// authenticating to the gateway. Mounted from the openshell-client-tls
	// secret. Empty disables mTLS (uses insecure or token auth).
	ClientCertPath string `yaml:"client_cert_path,omitempty" json:"client_cert_path,omitempty"`

	// ClientKeyPath is the path to the mTLS client private key.
	ClientKeyPath string `yaml:"client_key_path,omitempty" json:"client_key_path,omitempty"`

	// CACertPath is the CA certificate for verifying the gateway's server
	// cert. Empty uses the system CA pool.
	CACertPath string `yaml:"ca_cert_path,omitempty" json:"ca_cert_path,omitempty"`

	// AuthToken is a static bearer token for gateway authentication.
	// Used when mTLS is not configured (e.g., development with
	// server.auth.allowUnauthenticatedUsers). Empty disables token auth.
	AuthToken string `yaml:"auth_token,omitempty" json:"auth_token,omitempty"`

	// SandboxImage is the container image for sandbox pods. This image
	// should contain Astonish agent tooling. The OpenShell supervisor is
	// sideloaded by the gateway automatically.
	// Default: "ghcr.io/sap/astonish-sandbox-openshell:latest".
	SandboxImage string `yaml:"sandbox_image,omitempty" json:"sandbox_image,omitempty"`

	// Registry configures the OCI registry for custom-built sandbox images.
	Registry RegistryConfig `yaml:"registry,omitempty" json:"registry,omitempty"`

	// Builder configures the Kaniko-based image builder.
	Builder BuilderConfig `yaml:"builder,omitempty" json:"builder,omitempty"`

	// NetworkPolicy configures sandbox egress enforcement via the OpenShell
	// supervisor's network namespace proxy.
	NetworkPolicy NetworkPolicyConfig `yaml:"network_policy,omitempty" json:"network_policy,omitempty"`

	// LandlockCompatibility controls Landlock enforcement mode for sandboxes.
	// "best_effort" (default): degrades gracefully on kernels without Landlock.
	// "hard_requirement": fails sandbox startup if Landlock can't be enforced.
	LandlockCompatibility string `yaml:"landlock_compatibility,omitempty" json:"landlock_compatibility,omitempty"`

	// FilesystemPolicy allows extending the default sandbox Landlock filesystem
	// rules with additional paths. Useful for granting access to custom device
	// nodes or operator-specific mount points without modifying code.
	FilesystemPolicy FilesystemPolicyConfig `yaml:"filesystem_policy,omitempty" json:"filesystem_policy,omitempty"`

	// CertBundles mounts operator-provided CA trust material into every
	// OpenShell sandbox. Source "pvc" uses OpenShell driver_config PVC mounts;
	// source "configMap" (preferred on Cinder) relies on Kyverno to inject a
	// ConfigMap volume — OpenShell's driver_config schema is PVC-only.
	// Each bundle should hold a combined PEM (system CAs + corporate roots).
	// By default the bundle is also installed over the system CA path so the
	// OpenShell MITM proxy can verify corporate upstream TLS (trust env alone
	// is insufficient — the supervisor overwrites SSL_CERT_FILE to
	// /etc/openshell-tls/...).
	CertBundles []CertBundleConfig `yaml:"cert_bundles,omitempty" json:"cert_bundles,omitempty"`

	// IdleTimeoutMinutes evicts sandbox pods that have had no exec activity
	// for this duration. The pod is deleted (state → evicted) and recreated
	// transparently on the next tool call. Default: 240 (4 hours). Set to
	// -1 to disable idle eviction entirely.
	IdleTimeoutMinutes *int `yaml:"idle_timeout_minutes,omitempty" json:"idle_timeout_minutes,omitempty"`
}

// Cert bundle source values for CertBundleConfig.Source.
const (
	// CertBundleSourcePVC mounts via OpenShell driver_config (native PVC-only schema).
	CertBundleSourcePVC = "pvc"
	// CertBundleSourceConfigMap omits PVC from driver_config; Kyverno injects a ConfigMap mount.
	CertBundleSourceConfigMap = "configMap"
)

// CertBundleConfig describes a read-only CA bundle mounted into OpenShell
// sandboxes. Source selects PVC (OpenShell-native) vs ConfigMap (Kyverno inject).
type CertBundleConfig struct {
	// Name is the Kubernetes volume name (DNS-1123 label), e.g. "corp-root-ca".
	Name string `yaml:"name" json:"name"`

	// Source is "pvc" or "configMap". Empty inherits legacy rules: claim_name
	// without config_map_name → pvc; otherwise configMap.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`

	// ClaimName is the PVC name in the sandbox namespace (required for source pvc).
	ClaimName string `yaml:"claim_name,omitempty" json:"claim_name,omitempty"`

	// ConfigMapName is the ConfigMap name in the sandbox namespace (required for
	// source configMap). Kyverno mounts this ConfigMap into sandbox pods.
	ConfigMapName string `yaml:"config_map_name,omitempty" json:"config_map_name,omitempty"`

	// MountPath is the absolute path inside the sandbox where the bundle
	// is mounted, e.g. "/etc/astonish-ca/ca-bundle.crt".
	MountPath string `yaml:"mount_path" json:"mount_path"`

	// SubPath is an optional relative file key within the PVC or ConfigMap
	// (default ca-bundle.crt for chart-managed bundles).
	SubPath string `yaml:"sub_path,omitempty" json:"sub_path,omitempty"`

	// TrustEnv lists env var names set to MountPath. When empty, defaults to
	// SSL_CERT_FILE, CURL_CA_BUNDLE, REQUESTS_CA_BUNDLE, NODE_EXTRA_CA_CERTS,
	// and GIT_SSL_CAINFO. OpenShell typically overwrites these to its
	// openshell-tls bundle; InstallSystemTrust is what makes corporate
	// upstream TLS work under MITM.
	TrustEnv []string `yaml:"trust_env,omitempty" json:"trust_env,omitempty"`

	// InstallSystemTrust, when true (default if nil), also bind-mounts this
	// bundle over /etc/ssl/certs/ca-certificates.crt so the OpenShell
	// supervisor loads corporate roots for upstream TLS verification.
	// At most one cert_bundle may enable this.
	InstallSystemTrust *bool `yaml:"install_system_trust,omitempty" json:"install_system_trust,omitempty"`
}

// RegistryConfig holds OCI registry connection details for pushing
// custom-built sandbox images.
type RegistryConfig struct {
	// URL is the registry URL prefix. Images are pushed as:
	//   <URL>/astonish-sandbox-<scope>:<content-hash>
	// Examples: "ghcr.io/sap", "ghcr.io/org", "harbor.local:5000/proj"
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// SecretName is the name of a K8s Secret (type kubernetes.io/dockerconfigjson)
	// containing registry credentials. Must exist in the builder namespace.
	SecretName string `yaml:"secret_name,omitempty" json:"secret_name,omitempty"`
}

// BuilderConfig holds configuration for the Kaniko-based image builder.
type BuilderConfig struct {
	// Image is the pinned Kaniko executor image reference.
	// Default: "gcr.io/kaniko-project/executor:v1.23.2"
	Image string `yaml:"image,omitempty" json:"image,omitempty"`

	// Namespace where build Jobs run. Defaults to the control-plane namespace.
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
}

// NetworkPolicyConfig controls sandbox network egress enforcement via the
// OpenShell supervisor's network namespace proxy.
//
// The supervisor's proxy denies all traffic when no NetworkPolicies are
// provided. This config resolves named presets (groups of allowed hosts)
// plus operator-supplied extra endpoints into a concrete allowlist that
// is always sent to the supervisor.
type NetworkPolicyConfig struct {
	// Presets enables named groups of allowed endpoints.
	// Available presets: "default" (all below), "code_hosting",
	// "package_registries", "llm_apis", "tools", "system", "search", "cdn".
	// When empty or contains "default", all presets are enabled.
	Presets []string `yaml:"presets,omitempty" json:"presets,omitempty"`

	// ExtraEndpoints are additional host:port patterns added on top of
	// presets. Useful for internal services, private repos, etc.
	ExtraEndpoints []NetworkEndpointConfig `yaml:"extra_endpoints,omitempty" json:"extra_endpoints,omitempty"`
}

// NetworkEndpointConfig is a single allowed endpoint (host + optional port).
// Port 0 defaults to 443 in the proto mapping layer.
type NetworkEndpointConfig struct {
	Host string `yaml:"host" json:"host"`
	Port uint32 `yaml:"port,omitempty" json:"port,omitempty"`
}

// FilesystemPolicyConfig allows operators to extend the default Landlock
// filesystem rules applied to sandboxes. The base policy always includes
// standard system paths (read-only) and workspace/PTY paths (read-write);
// this config appends additional entries.
type FilesystemPolicyConfig struct {
	// ExtraReadOnly adds additional read-only paths to the default set.
	// Example: ["/data/shared-models"]
	ExtraReadOnly []string `yaml:"extra_read_only,omitempty" json:"extra_read_only,omitempty"`

	// ExtraReadWrite adds additional read-write paths to the default set.
	// Example: ["/mnt/scratch", "/dev/fuse"]
	ExtraReadWrite []string `yaml:"extra_read_write,omitempty" json:"extra_read_write,omitempty"`
}

// SandboxKubernetesConfig captures the operator-tunable knobs for the
// k8s backend. See pkg/sandbox/k8s.Config for the runtime struct;
// this type is the YAML-friendly mirror.
//
// Separating this from pkg/sandbox/k8s.Config keeps pkg/config free of
// a dependency on k8s.io/client-go (and therefore keeps single-node
// builds small when the k8s backend is not selected).
type SandboxKubernetesConfig struct {
	// KubeconfigPath is the path to a kubeconfig file. When empty, the
	// loader tries in-cluster config first, then $KUBECONFIG, then
	// ~/.kube/config. Ignored when InCluster is true.
	KubeconfigPath string `yaml:"kubeconfig_path,omitempty" json:"kubeconfig_path,omitempty"`

	// Context, if set, pins a named context in the loaded kubeconfig.
	Context string `yaml:"context,omitempty" json:"context,omitempty"`

	// InCluster forces the in-cluster config loader. Set this when
	// Astonish is deployed as a Pod in the same cluster it is
	// managing.
	InCluster bool `yaml:"in_cluster,omitempty" json:"in_cluster,omitempty"`

	// Namespace for sandbox pods. Default: "astonish-sandboxes".
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`

	// ControlPlaneNamespace is the namespace running the Astonish API
	// and worker pods. Default: "astonish". Used by EnsureOrgNetwork
	// so sandbox pods accept ingress from the control plane.
	ControlPlaneNamespace string `yaml:"control_plane_namespace,omitempty" json:"control_plane_namespace,omitempty"`

	// RuntimeClassName picks the Pod RuntimeClass. Default: empty
	// (cluster default). Phase F drops the hard Sysbox dependency;
	// operators who run a specialised runtime (Sysbox, Kata) opt in
	// explicitly by setting this to e.g. "sysbox-runc".
	RuntimeClassName string `yaml:"runtime_class_name,omitempty" json:"runtime_class_name,omitempty"`

	// OverlayMode selects the entrypoint's overlay strategy. One of:
	//   - ""       → "fuse" (default; most portable)
	//   - "fuse"   → fuse-overlayfs (needs /dev/fuse; see PrivilegedPods
	//                and FuseDeviceResource)
	//   - "kernel" → `mount -t overlay -o userxattr` (needs hostUsers:
	//                false or privileged; blocked on LXC nodes)
	//   - "auto"   → kernel first, fuse fallback
	// See docs/architecture/sandbox-backends.md §10.
	OverlayMode string `yaml:"overlay_mode,omitempty" json:"overlay_mode,omitempty"`

	// PrivilegedPods, when true, sets securityContext.privileged=true
	// on every sandbox container. This is the simple escape hatch for
	// dev clusters (LXC nodes, lab Kubernetes) where the unprivileged
	// path is unavailable. Production deployments should prefer the
	// unprivileged combo: FuseDeviceResource + HostUsers. Default:
	// false.
	PrivilegedPods bool `yaml:"privileged_pods,omitempty" json:"privileged_pods,omitempty"`

	// HostUsers, when non-nil, sets PodSpec.hostUsers on sandbox pods.
	// Pointer-to-false requests a user namespace (K8s 1.33+ beta-on,
	// 1.36 GA). nil means "don't set the field" (cluster default).
	HostUsers *bool `yaml:"host_users,omitempty" json:"host_users,omitempty"`

	// FuseDeviceResource is the extended-resource key advertised by a
	// FUSE device plugin (e.g. "smarter-devices/fuse" for the
	// smarter-device-manager project). When non-empty, sandbox pods
	// request a quantity of 1 on this resource so the kubelet plugs
	// /dev/fuse in without privileged mode. Default: "" (no plugin).
	FuseDeviceResource string `yaml:"fuse_device_resource,omitempty" json:"fuse_device_resource,omitempty"`

	// SandboxImage is the container image for sandbox pods. Must ship
	// the overlay entrypoint and the astonish node binary. Default:
	// "ghcr.io/sap/astonish-sandbox-base:latest".
	SandboxImage string `yaml:"sandbox_image,omitempty" json:"sandbox_image,omitempty"`

	// LayersPVCName / UppersPVCName name the RWX PVCs backing the
	// layer store and per-session uppers. Defaults:
	// "astonish-layers" / "astonish-uppers".
	LayersPVCName string `yaml:"layers_pvc_name,omitempty" json:"layers_pvc_name,omitempty"`
	UppersPVCName string `yaml:"uppers_pvc_name,omitempty" json:"uppers_pvc_name,omitempty"`

	// GC controls the deferred K8s storage reconciler for layers, orphan
	// upper directories, and stale evicted session uppers.
	GC KubernetesGCConfig `yaml:"gc,omitempty" json:"gc,omitempty"`

	// QPS / Burst tune the client-go rate limiter. Zero → defaults
	// (50 / 100). Busier fleets want higher values.
	QPS   float32 `yaml:"qps,omitempty" json:"qps,omitempty"`
	Burst int     `yaml:"burst,omitempty" json:"burst,omitempty"`
}

// KubernetesGCConfig captures operator-tunable knobs for the direct K8s
// storage reconciler. Zero values keep conservative production defaults.
type KubernetesGCConfig struct {
	// IntervalMinutes controls how often the reconciler runs. Default: 60.
	IntervalMinutes int `yaml:"interval_minutes,omitempty" json:"interval_minutes,omitempty"`

	// LayerGraceHours controls when unreferenced layer directories are eligible
	// for cleanup. Default: 24.
	LayerGraceHours int `yaml:"layer_grace_hours,omitempty" json:"layer_grace_hours,omitempty"`

	// OrphanUpperGraceMinutes controls when upper directories with no matching
	// sandbox session row are eligible for cleanup. Default: 60.
	OrphanUpperGraceMinutes int `yaml:"orphan_upper_grace_minutes,omitempty" json:"orphan_upper_grace_minutes,omitempty"`

	// EvictedUpperRetentionHours controls how long evicted, unpinned, inactive
	// session uppers remain resumable. Default: 336 (14 days).
	EvictedUpperRetentionHours int `yaml:"evicted_upper_retention_hours,omitempty" json:"evicted_upper_retention_hours,omitempty"`

	// EvictedUpperRetentionDisabled disables stale evicted upper cleanup while
	// leaving orphan cleanup and layer cleanup enabled.
	EvictedUpperRetentionDisabled bool `yaml:"evicted_upper_retention_disabled,omitempty" json:"evicted_upper_retention_disabled,omitempty"`

	// MaxUpperReclaimsPerCycle bounds how many upper directories one GC cycle can
	// delete across orphan and stale-evicted cleanup. Default: 500.
	MaxUpperReclaimsPerCycle int `yaml:"max_upper_reclaims_per_cycle,omitempty" json:"max_upper_reclaims_per_cycle,omitempty"`
}

// K8sGCInterval returns the direct K8s storage GC interval. Default: 1h.
func (c *SandboxKubernetesConfig) K8sGCInterval() time.Duration {
	if c.GC.IntervalMinutes > 0 {
		return time.Duration(c.GC.IntervalMinutes) * time.Minute
	}
	return time.Hour
}

// K8sLayerGracePeriod returns the grace period for unreferenced layers.
// Default: 24h.
func (c *SandboxKubernetesConfig) K8sLayerGracePeriod() time.Duration {
	if c.GC.LayerGraceHours > 0 {
		return time.Duration(c.GC.LayerGraceHours) * time.Hour
	}
	return 24 * time.Hour
}

// K8sOrphanUpperGracePeriod returns the grace period for upper directories
// that have no matching sandbox session row. Default: 1h.
func (c *SandboxKubernetesConfig) K8sOrphanUpperGracePeriod() time.Duration {
	if c.GC.OrphanUpperGraceMinutes > 0 {
		return time.Duration(c.GC.OrphanUpperGraceMinutes) * time.Minute
	}
	return time.Hour
}

// K8sEvictedUpperRetention returns how long stale evicted session uppers stay
// resumable. Default: 14 days. The second return value is false when cleanup is
// explicitly disabled.
func (c *SandboxKubernetesConfig) K8sEvictedUpperRetention() (time.Duration, bool) {
	if c.GC.EvictedUpperRetentionDisabled {
		return 0, false
	}
	if c.GC.EvictedUpperRetentionHours > 0 {
		return time.Duration(c.GC.EvictedUpperRetentionHours) * time.Hour, true
	}
	return 14 * 24 * time.Hour, true
}

// K8sMaxUpperReclaimsPerCycle returns the maximum upper directories reclaimed
// by one reconciler cycle. Default: 500.
func (c *SandboxKubernetesConfig) K8sMaxUpperReclaimsPerCycle() int {
	if c.GC.MaxUpperReclaimsPerCycle > 0 {
		return c.GC.MaxUpperReclaimsPerCycle
	}
	return 500
}

// BackendKind returns the configured backend, lower-cased, with "" and
// "incus" both normalising to "incus", and "kubernetes" aliased to "k8s".
// This is the canonical accessor that callers should use — do NOT read
// SandboxConfig.Backend directly.
func (c *SandboxConfig) BackendKind() string {
	b := strings.ToLower(strings.TrimSpace(c.Backend))
	switch b {
	case "", "incus":
		return "incus"
	case "kubernetes":
		return "k8s"
	default:
		return b
	}
}

// IsK8sBackend is a readability helper for the common case.
func (c *SandboxConfig) IsK8sBackend() bool {
	return c.BackendKind() == "k8s"
}

// IsOpenShellBackend is a readability helper for the OpenShell case.
func (c *SandboxConfig) IsOpenShellBackend() bool {
	return c.BackendKind() == "openshell"
}

// OpenShellGatewayTLS returns whether TLS is enabled for the OpenShell
// gateway connection. Default is true when GatewayTLS is nil.
func (c *SandboxOpenShellConfig) OpenShellGatewayTLS() bool {
	if c.GatewayTLS == nil {
		return true
	}
	return *c.GatewayTLS
}

// OpenShellIdleTimeout returns the idle timeout for OpenShell sandbox pods.
// Default: 4 hours. Returns 0 (disabled) when set to a negative value.
func (c *SandboxOpenShellConfig) OpenShellIdleTimeout() time.Duration {
	if c.IdleTimeoutMinutes == nil {
		return 4 * time.Hour // default
	}
	if *c.IdleTimeoutMinutes <= 0 {
		return 0 // disabled
	}
	return time.Duration(*c.IdleTimeoutMinutes) * time.Minute
}

// SecurityConfig controls security features like proactive secret detection.
type SecurityConfig struct {
	SecretScanner        SecretScannerConfig `yaml:"secret_scanner,omitempty" json:"secret_scanner,omitempty"`
	AllowPrivateNetworks bool                `yaml:"allow_private_networks" json:"allow_private_networks,omitempty"`
}

// SecretScannerConfig controls the proactive secret detection engine that
// scans user messages before sending them to the LLM provider.
type SecretScannerConfig struct {
	Enabled          *bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`                     // Default: false (nil means disabled)
	EntropyThreshold float64 `yaml:"entropy_threshold,omitempty" json:"entropy_threshold,omitempty"` // Shannon entropy bits/char. Default: 4.0
	MinTokenLength   int     `yaml:"min_token_length,omitempty" json:"min_token_length,omitempty"`   // Minimum chars for entropy/structural check. Default: 16
}

// IsSecretScannerEnabled returns true if the secret scanner should run.
// Default is false (nil means disabled).
func (c *SecurityConfig) IsSecretScannerEnabled() bool {
	if c.SecretScanner.Enabled == nil {
		return false
	}
	return *c.SecretScanner.Enabled
}

// IsPrivateNetworkAllowed returns true if SSRF private-IP blocking is disabled.
// Safe to call on a nil receiver.
func (s *SecurityConfig) IsPrivateNetworkAllowed() bool {
	if s == nil {
		return false
	}
	return s.AllowPrivateNetworks
}

// StorageConfig controls the data storage backend.
//
// When backend is "file" (the default, or when unset), all data is stored on
// the local filesystem using the existing file-based stores. This is the
// single-user "personal mode" that requires zero external dependencies.
//
// When backend is "postgres", data is stored in PostgreSQL with full
// multi-tenant isolation (database-per-org, schema-per-team). This enables
// the platform mode with organizations, teams, and shared knowledge.
type StorageConfig struct {
	// Backend selects the storage implementation: "file" (default), "sqlite", or "postgres".
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`

	// SQLite holds settings for the SQLite-backed platform mode.
	// Only used when backend is "sqlite".
	SQLite SQLiteConfig `yaml:"sqlite,omitempty" json:"sqlite,omitempty"`

	// Postgres holds connection settings for the platform database.
	// Only used when backend is "postgres".
	Postgres PostgresConfig `yaml:"postgres,omitempty" json:"postgres,omitempty"`

	// Auth configures authentication for platform mode (backend: postgres or sqlite).
	// In personal mode (backend: file), this is ignored — device auth is used instead.
	Auth PlatformAuthConfig `yaml:"auth,omitempty" json:"auth,omitempty"`
}

// SQLiteConfig holds settings for SQLite-backed platform mode.
type SQLiteConfig struct {
	// DataDir is the directory where all SQLite database files are stored.
	// Defaults to ~/.local/share/astonish/ if empty.
	DataDir string `yaml:"data_dir,omitempty" json:"data_dir,omitempty"`
}

// GetDataDir returns the SQLite data directory, falling back to
// $XDG_DATA_HOME/astonish/ or ~/.local/share/astonish/.
func (c *SQLiteConfig) GetDataDir() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	if envDir := os.Getenv("ASTONISH_DATA_DIR"); envDir != "" {
		return envDir
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return xdg + "/astonish"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/share/astonish"
}

// PostgresConfig holds PostgreSQL connection parameters for platform mode.
type PostgresConfig struct {
	// PlatformDSN is the connection string for the platform database.
	// This database stores cross-org data: users, organizations, OIDC
	// providers, and login sessions.
	//
	// The user in this DSN must have CREATEDB privilege to provision
	// per-org databases, or org databases must be pre-created.
	//
	// Format: "postgres://user:pass@host:port/astonish_{suffix}_platform?sslmode=require"
	PlatformDSN string `yaml:"platform_dsn,omitempty" json:"platform_dsn,omitempty"`

	// InstanceSuffix is a unique 6-character alphanumeric identifier for this
	// Astonish instance. It namespaces all databases on the PostgreSQL host:
	//   - Platform DB: astonish_{suffix}_platform
	//   - Org DBs:     astonish_{suffix}_{org_slug}
	//
	// Generated automatically on first setup. Multiple Astonish instances can
	// share the same PostgreSQL host by having different suffixes.
	// Empty string means legacy naming (astonish_platform, astonish_org_{slug}).
	InstanceSuffix string `yaml:"instance_suffix,omitempty" json:"instance_suffix,omitempty"`

	// MaxOpenConns is the maximum number of open connections per org pool.
	// Default: 25. Set to 0 for unlimited (not recommended).
	MaxOpenConns int `yaml:"max_open_conns,omitempty" json:"max_open_conns,omitempty"`

	// MaxIdleConns is the maximum number of idle connections per org pool.
	// Default: 5.
	MaxIdleConns int `yaml:"max_idle_conns,omitempty" json:"max_idle_conns,omitempty"`

	// ConnMaxLifetimeMinutes is the maximum lifetime of a connection in minutes.
	// Default: 30. Set to 0 for unlimited.
	ConnMaxLifetimeMinutes int `yaml:"conn_max_lifetime_minutes,omitempty" json:"conn_max_lifetime_minutes,omitempty"`
}

// IsPostgres returns true if the storage backend is PostgreSQL.
func (c *StorageConfig) IsPostgres() bool {
	return c.Backend == "postgres"
}

// IsSQLite returns true if the storage backend is SQLite.
func (c *StorageConfig) IsSQLite() bool {
	return c.Backend == "sqlite"
}

// IsFile returns true if the storage backend is file-based (default).
func (c *StorageConfig) IsFile() bool {
	return c.Backend == "" || c.Backend == "file"
}

// GetPlatformDSN returns the platform database connection string, falling back
// to the ASTONISH_PLATFORM_DSN environment variable if the config field is empty.
// This allows K8s deployments to inject the DSN via a Secret without putting
// credentials in the ConfigMap.
func (c *PostgresConfig) GetPlatformDSN() string {
	if c.PlatformDSN != "" {
		return c.PlatformDSN
	}
	return os.Getenv("ASTONISH_PLATFORM_DSN")
}

// GetMaxOpenConns returns the max open connections with a sensible default.
func (c *PostgresConfig) GetMaxOpenConns() int {
	if c.MaxOpenConns <= 0 {
		return 25
	}
	return c.MaxOpenConns
}

// GetMaxIdleConns returns the max idle connections with a sensible default.
func (c *PostgresConfig) GetMaxIdleConns() int {
	if c.MaxIdleConns <= 0 {
		return 5
	}
	return c.MaxIdleConns
}

// GetConnMaxLifetime returns the connection max lifetime with a sensible default.
func (c *PostgresConfig) GetConnMaxLifetime() time.Duration {
	if c.ConnMaxLifetimeMinutes <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.ConnMaxLifetimeMinutes) * time.Minute
}

// GenerateInstanceSuffix creates a random 6-character lowercase alphanumeric
// suffix used to namespace all databases for this Astonish instance.
func GenerateInstanceSuffix() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// PlatformDBName returns the database name for the platform database.
// If suffix is empty (legacy), returns "astonish_platform".
// Otherwise returns "astonish_{suffix}_platform".
func PlatformDBName(suffix string) string {
	if suffix == "" {
		return "astonish_platform"
	}
	return "astonish_" + suffix + "_platform"
}

// OrgDBName returns the database name for an organization.
// If suffix is empty (legacy), returns "astonish_org_{slug}".
// Otherwise returns "astonish_{suffix}_{slug}".
func OrgDBName(suffix, orgSlug string) string {
	slug := sanitizeDBSlug(orgSlug)
	if suffix == "" {
		return "astonish_org_" + slug
	}
	return "astonish_" + suffix + "_" + slug
}

// sanitizeDBSlug removes any characters that aren't lowercase alphanumeric or underscore.
func sanitizeDBSlug(s string) string {
	// Convert hyphens to underscores, strip everything else
	var b strings.Builder
	for _, r := range s {
		if r == '-' {
			b.WriteRune('_')
		} else if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PlatformAuthConfig controls authentication in platform (multi-tenant) mode.
//
// Two modes are supported:
//   - "builtin" (default): Email/password registration with bcrypt hashing.
//     JWT tokens are issued as httpOnly cookies. This mode requires no external
//     identity provider and works out of the box.
//   - "oidc": Delegates authentication to an external OpenID Connect provider
//     (SAP IAS, Azure AD, Okta, etc.). Users are auto-created on first login.
//     Team memberships can be auto-mapped from OIDC group claims.
//
// Both modes use JWT for session management. The JWT contains user ID, org slug,
// and default team slug as claims. A separate X-Astonish-Team header allows
// switching team context within the same org.
type PlatformAuthConfig struct {
	// Mode selects the authentication strategy: "builtin" (default) or "oidc".
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`

	// JWTSecret is the HMAC-SHA256 signing key for access and refresh tokens.
	// Required in platform mode. If empty, a random 32-byte key is generated
	// at startup (tokens won't survive daemon restarts).
	// Can be set via ASTONISH_JWT_SECRET environment variable.
	JWTSecret string `yaml:"jwt_secret,omitempty" json:"jwt_secret,omitempty"`

	// AccessTokenTTLMinutes controls the access token lifetime.
	// Default: 15 minutes. Short-lived for security.
	AccessTokenTTLMinutes int `yaml:"access_token_ttl_minutes,omitempty" json:"access_token_ttl_minutes,omitempty"`

	// RefreshTokenTTLDays controls the refresh token lifetime.
	// Default: 90 days. Used to obtain new access tokens without re-login.
	RefreshTokenTTLDays int `yaml:"refresh_token_ttl_days,omitempty" json:"refresh_token_ttl_days,omitempty"`

	// AllowRegistration controls whether new users can self-register.
	// Default: true. Set to false to require invitations.
	AllowRegistration *bool `yaml:"allow_registration,omitempty" json:"allow_registration,omitempty"`

	// RequireEmailVerification controls whether self-registered users must verify
	// their email address via a 6-digit code before their account becomes active.
	// Only applies to builtin auth sign-up; OIDC users are verified by the IdP.
	// Default: true.
	RequireEmailVerification *bool `yaml:"require_email_verification,omitempty" json:"require_email_verification,omitempty"`

	// DefaultOrgName is used when the first user registers and auto-creates an org.
	// Default: "Default Organization".
	DefaultOrgName string `yaml:"default_org_name,omitempty" json:"default_org_name,omitempty"`

	// DefaultOrgSlug is the URL-safe slug for the auto-created org.
	// Default: "default". Must be lowercase alphanumeric with hyphens.
	DefaultOrgSlug string `yaml:"default_org_slug,omitempty" json:"default_org_slug,omitempty"`

	// OIDC holds OpenID Connect provider settings. Only used when mode is "oidc".
	OIDC OIDCConfig `yaml:"oidc,omitempty" json:"oidc,omitempty"`

	// LoopbackBypass controls how requests from 127.0.0.1/::1 are authenticated.
	// Values:
	//   "always"     — loopback requests pass without any token (personal mode default)
	//   "with_token" — loopback requests must carry a valid JWT (platform mode default)
	//   "never"      — loopback requests go through full auth like remote requests
	// Default: "with_token" in platform mode, "always" in personal mode.
	LoopbackBypass string `yaml:"loopback_bypass,omitempty" json:"loopback_bypass,omitempty"`
}

// OIDCConfig holds settings for an external OpenID Connect identity provider.
type OIDCConfig struct {
	// IssuerURL is the OIDC discovery endpoint (e.g., "https://accounts.sap.com").
	// The .well-known/openid-configuration is appended automatically.
	IssuerURL string `yaml:"issuer_url,omitempty" json:"issuer_url,omitempty"`

	// ClientID is the OAuth 2.0 client ID registered with the provider.
	ClientID string `yaml:"client_id,omitempty" json:"client_id,omitempty"`

	// ClientSecret is the OAuth 2.0 client secret.
	// Can be stored in the credential store as "oidc.client_secret".
	ClientSecret string `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`

	// RedirectURL is the callback URL registered with the provider.
	// Default: auto-detected from the request (http(s)://host:port/api/auth/oidc/callback).
	RedirectURL string `yaml:"redirect_url,omitempty" json:"redirect_url,omitempty"`

	// Scopes to request. Default: ["openid", "profile", "email"].
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`

	// GroupsClaim is the JWT claim containing group memberships for team auto-mapping.
	// Common values: "groups" (Azure AD), "xs.groups" (SAP IAS), "cognito:groups" (AWS).
	// If empty, no automatic team mapping is performed.
	GroupsClaim string `yaml:"groups_claim,omitempty" json:"groups_claim,omitempty"`

	// TeamMapping maps OIDC group names to Astonish team slugs.
	// Example: {"engineering": "eng-team", "product": "product-team"}
	// Groups not in this map are ignored.
	TeamMapping map[string]string `yaml:"team_mapping,omitempty" json:"team_mapping,omitempty"`
}

// IsBuiltinAuth returns true if platform auth uses the built-in email/password mode.
func (c *PlatformAuthConfig) IsBuiltinAuth() bool {
	return c.Mode == "" || c.Mode == "builtin"
}

// IsOIDCAuth returns true if platform auth delegates to an OIDC provider.
func (c *PlatformAuthConfig) IsOIDCAuth() bool {
	return c.Mode == "oidc"
}

// GetAccessTokenTTL returns the access token TTL with a sensible default.
func (c *PlatformAuthConfig) GetAccessTokenTTL() time.Duration {
	if c.AccessTokenTTLMinutes > 0 {
		return time.Duration(c.AccessTokenTTLMinutes) * time.Minute
	}
	return 15 * time.Minute
}

// GetRefreshTokenTTL returns the refresh token TTL with a sensible default.
func (c *PlatformAuthConfig) GetRefreshTokenTTL() time.Duration {
	if c.RefreshTokenTTLDays > 0 {
		return time.Duration(c.RefreshTokenTTLDays) * 24 * time.Hour
	}
	return 90 * 24 * time.Hour
}

// IsRegistrationAllowed returns true if new users can self-register.
// Default: true (nil means allowed).
func (c *PlatformAuthConfig) IsRegistrationAllowed() bool {
	if c.AllowRegistration == nil {
		return true
	}
	return *c.AllowRegistration
}

// IsEmailVerificationRequired returns true if email verification is needed for sign-up.
// Default: true (nil means required). Only applies to builtin auth.
func (c *PlatformAuthConfig) IsEmailVerificationRequired() bool {
	if c.RequireEmailVerification == nil {
		return true
	}
	return *c.RequireEmailVerification
}

// GetDefaultOrgName returns the name for the auto-provisioned org.
func (c *PlatformAuthConfig) GetDefaultOrgName() string {
	if c.DefaultOrgName != "" {
		return c.DefaultOrgName
	}
	return "Default Organization"
}

// GetDefaultOrgSlug returns the slug for the auto-provisioned org.
func (c *PlatformAuthConfig) GetDefaultOrgSlug() string {
	if c.DefaultOrgSlug != "" {
		return c.DefaultOrgSlug
	}
	return "default"
}

// GetJWTSecret returns the JWT signing key, falling back to environment variable.
func (c *PlatformAuthConfig) GetJWTSecret() string {
	if c.JWTSecret != "" {
		return c.JWTSecret
	}
	return os.Getenv("ASTONISH_JWT_SECRET")
}

// GenerateJWTSecret creates a cryptographically secure random JWT signing key.
// Returns a 64-character hex string (32 random bytes).
func GenerateJWTSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read never returns an error on supported platforms,
		// but handle it defensively.
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// SandboxLimits defines resource limits for session containers.
type SandboxLimits struct {
	Memory    string `yaml:"memory,omitempty" json:"memory,omitempty"`
	CPU       int    `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Processes int    `yaml:"processes,omitempty" json:"processes,omitempty"`

	// Requests configures the K8s scheduler reservation (the "floor" for
	// capacity planning). On Kubernetes, pods request these resources from
	// the scheduler and are guaranteed at least this much under contention.
	// Values here should reflect the IDLE/TYPICAL footprint of a sandbox,
	// not the peak. The Limits fields above define the burst ceiling.
	//
	// On Incus this sub-struct is ignored — Incus has only cgroup ceilings
	// with implicit overcommit.
	//
	// Zero values mean "auto-derive from limits" using a built-in ratio
	// suitable for chat-mostly-idle workloads (cpu: 5% of limit, min 50m;
	// memory: 12.5% of limit, min 128Mi).
	Requests SandboxRequests `yaml:"requests,omitempty" json:"requests,omitempty"`
}

// SandboxRequests defines the K8s scheduler reservation for sandbox pods.
// These are the "idle floor" values — what the scheduler subtracts from
// node allocatable. Keep these low for high session density; the Limits
// fields above define the burst ceiling (cgroup throttle).
type SandboxRequests struct {
	// CPUMillis is the CPU request in millicores (e.g. 100 = 100m = 0.1 CPU).
	// Zero means auto-derive from SandboxLimits.CPU.
	CPUMillis int `yaml:"cpu_millis,omitempty" json:"cpu_millis,omitempty"`

	// MemoryMiB is the memory request in MiB (e.g. 256 = 256Mi).
	// Zero means auto-derive from SandboxLimits.Memory.
	MemoryMiB int `yaml:"memory_mib,omitempty" json:"memory_mib,omitempty"`
}

// SandboxPruneConfig controls periodic cleanup of orphaned containers.
type SandboxPruneConfig struct {
	OrphanCheckHours   int `yaml:"orphan_check_hours,omitempty" json:"orphan_check_hours,omitempty"`
	IdleTimeoutMinutes int `yaml:"idle_timeout_minutes,omitempty" json:"idle_timeout_minutes,omitempty"` // Stop idle containers after this many minutes (default: 10, 0 = disabled)
}

// MemoryConfig controls the semantic memory / RAG system.
type MemoryConfig struct {
	Enabled   *bool           `yaml:"enabled,omitempty" json:"enabled,omitempty"` // Default: true (nil means true)
	MemoryDir string          `yaml:"memory_dir,omitempty" json:"memory_dir,omitempty"`
	VectorDir string          `yaml:"vector_dir,omitempty" json:"vector_dir,omitempty"`
	Embedding EmbeddingConfig `yaml:"embedding,omitempty" json:"embedding,omitempty"`
	Chunking  ChunkingConfig  `yaml:"chunking,omitempty" json:"chunking,omitempty"`
	Search    SearchConfig    `yaml:"search,omitempty" json:"search,omitempty"`
	Sync      SyncConfig      `yaml:"sync,omitempty" json:"sync,omitempty"`
}

// EmbeddingConfig controls the embedding provider for memory search.
type EmbeddingConfig struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"` // "auto", "openai", "ollama", "openai-compat"
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	BaseURL  string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIKey   string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
}

// ChunkingConfig controls how memory files are split into chunks.
type ChunkingConfig struct {
	MaxChars int `yaml:"max_chars,omitempty" json:"max_chars,omitempty"` // Default: 1600
	Overlap  int `yaml:"overlap,omitempty" json:"overlap,omitempty"`     // Default: 320
}

// SearchConfig controls memory search defaults.
type SearchConfig struct {
	MaxResults int     `yaml:"max_results,omitempty" json:"max_results,omitempty"` // Default: 6
	MinScore   float64 `yaml:"min_score,omitempty" json:"min_score,omitempty"`     // Default: 0.35
}

// SyncConfig controls the file watcher for live reindexing.
type SyncConfig struct {
	Watch      *bool `yaml:"watch,omitempty" json:"watch,omitempty"`             // Default: true (nil means true)
	DebounceMs int   `yaml:"debounce_ms,omitempty" json:"debounce_ms,omitempty"` // Default: 1500
}

// IsMemoryEnabled returns whether the memory system is enabled.
// Defaults to true if not explicitly set.
func (c *MemoryConfig) IsMemoryEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// IsWatchEnabled returns whether the file watcher is enabled.
// Defaults to true if not explicitly set.
func (c *MemoryConfig) IsWatchEnabled() bool {
	if c.Sync.Watch == nil {
		return true
	}
	return *c.Sync.Watch
}

// GetMemoryDir returns the memory directory, defaulting to ~/.config/astonish/memory/.
func GetMemoryDir(cfg *MemoryConfig) (string, error) {
	if cfg != nil && cfg.MemoryDir != "" {
		return cfg.MemoryDir, nil
	}
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "memory"), nil
}

// GetVectorDir returns the vector storage directory, defaulting to ~/.config/astonish/memory/vectors/.
func GetVectorDir(cfg *MemoryConfig) (string, error) {
	if cfg != nil && cfg.VectorDir != "" {
		return cfg.VectorDir, nil
	}
	memDir, err := GetMemoryDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(memDir, "vectors"), nil
}

// WebServerConfig stores credentials and installation state for a standard MCP server.
// The server command, args, and env var names are defined in standard_servers.go.
// For servers that require an API key (Tavily, Brave, Firecrawl), the key is stored here.
// For keyless servers, only the Installed flag is set.
type WebServerConfig struct {
	APIKey    string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Installed bool   `yaml:"installed,omitempty" json:"installed,omitempty"`
}

// PerplexityWebSearchConfig configures the model-backed Perplexity/Sonar web search tool.
// Provider and Model refer to a normal configured model provider instance and model ID.
type PerplexityWebSearchConfig struct {
	Provider          string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model             string `yaml:"model,omitempty" json:"model,omitempty"`
	SearchContextSize string `yaml:"search_context_size,omitempty" json:"search_context_size,omitempty"`
	MaxResults        int    `yaml:"max_results,omitempty" json:"max_results,omitempty"`
}

// SessionConfig controls session persistence behavior.
type SessionConfig struct {
	// Storage type: "memory" (default) or "file"
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty"`
	// BaseDir overrides the default session storage directory.
	// Empty means ~/.config/astonish/sessions/
	BaseDir string `yaml:"base_dir,omitempty" json:"base_dir,omitempty"`
	// Compaction controls automatic context window compaction.
	Compaction CompactionConfig `yaml:"compaction,omitempty" json:"compaction,omitempty"`
	// Cleanup controls automatic session expiry.
	Cleanup SessionCleanupConfig `yaml:"cleanup,omitempty" json:"cleanup,omitempty"`
}

// SessionCleanupConfig controls automatic deletion of old sessions.
type SessionCleanupConfig struct {
	// MaxAgeDays is the maximum age (in days since last activity) before a session
	// is automatically deleted. 0 means disabled (sessions persist forever).
	// Default: 5 days. Use a pointer so explicit 0 can be distinguished from unset.
	MaxAgeDays *int `yaml:"max_age_days,omitempty" json:"max_age_days,omitempty"`
}

// CompactionConfig controls how and when context is compacted.
type CompactionConfig struct {
	// Enabled turns compaction on/off. Default true (nil means true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Threshold is the fraction of the context window that triggers compaction (0.0-1.0).
	// Default 0.8 (compact when 80% full).
	Threshold float64 `yaml:"threshold,omitempty" json:"threshold,omitempty"`
	// PreserveRecent is the number of most recent messages to keep intact (not summarized).
	// Default 4.
	PreserveRecent int `yaml:"preserve_recent,omitempty" json:"preserve_recent,omitempty"`
}

// IsCompactionEnabled returns whether compaction is enabled. Defaults to true.
func (c *CompactionConfig) IsCompactionEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// GetThreshold returns the compaction threshold (default 0.8).
func (c *CompactionConfig) GetThreshold() float64 {
	if c.Threshold <= 0 || c.Threshold > 1.0 {
		return 0.8
	}
	return c.Threshold
}

// GetPreserveRecent returns the number of recent messages to preserve (default 4).
func (c *CompactionConfig) GetPreserveRecent() int {
	if c.PreserveRecent <= 0 {
		return 4
	}
	return c.PreserveRecent
}

// EffectiveMaxAgeDays returns the session cleanup max age (default 5 days).
// Returns 0 if cleanup is disabled.
func (c *SessionCleanupConfig) EffectiveMaxAgeDays() int {
	if c.MaxAgeDays == nil {
		return 5 // default: enabled at 5 days
	}
	if *c.MaxAgeDays <= 0 {
		return 0 // explicitly disabled
	}
	return *c.MaxAgeDays
}

type ChatConfig struct {
	SystemPrompt string `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	MaxToolCalls int    `yaml:"max_tool_calls,omitempty" json:"max_tool_calls,omitempty"`
	MaxTools     int    `yaml:"max_tools,omitempty" json:"max_tools,omitempty"`
	AutoApprove  bool   `yaml:"auto_approve,omitempty" json:"auto_approve,omitempty"`
	WorkspaceDir string `yaml:"workspace_dir,omitempty" json:"workspace_dir,omitempty"`
	FlowSaveDir  string `yaml:"flow_save_dir,omitempty" json:"flow_save_dir,omitempty"`
}

type GeneralConfig struct {
	DefaultProvider string `yaml:"default_provider" json:"default_provider"`
	DefaultModel    string `yaml:"default_model" json:"default_model"`
	WebSearchTool   string `yaml:"web_search_tool" json:"web_search_tool"`
	WebExtractTool  string `yaml:"web_extract_tool" json:"web_extract_tool"`
	ContextLength   int    `yaml:"context_length,omitempty" json:"context_length,omitempty"` // Override context window size (tokens)
	Timezone        string `yaml:"timezone,omitempty" json:"timezone,omitempty"`             // IANA timezone (e.g. "America/New_York")
}

// DaemonConfig controls the background daemon service.
type DaemonConfig struct {
	// Port for the HTTP server. Default: 9393.
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// LogDir overrides the default log directory.
	// Empty means ~/.config/astonish/logs/
	LogDir string `yaml:"log_dir,omitempty" json:"log_dir,omitempty"`
	// Auth controls device-based authentication for the Studio web UI.
	// Auth is enabled by default in daemon mode.
	Auth StudioAuthConfig `yaml:"auth,omitempty" json:"auth,omitempty"`
}

// GetPort returns the daemon port, defaulting to 9393.
func (c *DaemonConfig) GetPort() int {
	if c.Port <= 0 {
		return 9393
	}
	return c.Port
}

// GetLogDir returns the log directory, defaulting to ~/.config/astonish/logs/.
func (c *DaemonConfig) GetLogDir() string {
	if c.LogDir != "" {
		return c.LogDir
	}
	configDir, err := GetConfigDir()
	if err != nil {
		return "logs"
	}
	return filepath.Join(configDir, "logs")
}

// StudioAuthConfig controls device-based authentication for the Studio web UI.
type StudioAuthConfig struct {
	// Disabled turns off authentication entirely. Default: false (auth is on).
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// SessionTTLDays controls how long an authorized session lasts.
	// Default: 90 days. Set to 0 to use the default.
	SessionTTLDays int `yaml:"session_ttl_days,omitempty" json:"session_ttl_days,omitempty"`
}

// IsAuthEnabled returns true if Studio authentication is enabled (default: true).
func (c *StudioAuthConfig) IsAuthEnabled() bool {
	return !c.Disabled
}

// GetSessionTTL returns the session TTL as a time.Duration.
func (c *StudioAuthConfig) GetSessionTTL() time.Duration {
	if c.SessionTTLDays > 0 {
		return time.Duration(c.SessionTTLDays) * 24 * time.Hour
	}
	return 90 * 24 * time.Hour
}

// ChannelsConfig controls communication channel integrations.
type ChannelsConfig struct {
	// Enabled controls whether channels are active. Default: false (nil means false).
	Enabled  *bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Telegram TelegramConfig `yaml:"telegram,omitempty" json:"telegram,omitempty"`
	Email    EmailConfig    `yaml:"email,omitempty" json:"email,omitempty"`
	Slack    SlackConfig    `yaml:"slack,omitempty" json:"slack,omitempty"`
	A2A      A2AConfig      `yaml:"a2a,omitempty" json:"a2a,omitempty"`
}

// IsChannelsEnabled returns true if channels are explicitly enabled.
func (c *ChannelsConfig) IsChannelsEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// SchedulerConfig controls the job scheduler.
type SchedulerConfig struct {
	// Enabled controls whether the scheduler is active. Default: true (nil means true).
	// Set to false to explicitly disable the scheduler.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IsSchedulerEnabled returns true if the scheduler is enabled.
// Defaults to true if not explicitly set (nil means true).
func (c *SchedulerConfig) IsSchedulerEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// Daemon mode constants control the runtime role of the binary.
// ASTONISH_MODE env var selects which subsystems are active.
const (
	// DaemonModeDefault runs everything: HTTP + scheduler + channels + fleet.
	// This is the standard single-instance mode (personal or platform).
	DaemonModeDefault = "default"

	// DaemonModeAPI runs only the HTTP server and chat execution.
	// No scheduler, no channels, no fleet monitors, no PID file.
	// Designed for horizontally-scaled Kubernetes API pods.
	DaemonModeAPI = "api"

	// DaemonModeWorker runs scheduler + channels + fleet monitors.
	// HTTP is still active (health probes, internal APIs) but not externally exposed.
	// Single replica. No PID file.
	DaemonModeWorker = "worker"
)

// GetDaemonMode returns the runtime mode from the ASTONISH_MODE env var.
// Returns DaemonModeDefault if unset or empty.
// Valid values: "default", "api", "worker".
func GetDaemonMode() string {
	mode := os.Getenv("ASTONISH_MODE")
	switch mode {
	case DaemonModeAPI, DaemonModeWorker:
		return mode
	default:
		return DaemonModeDefault
	}
}

// IsDaemonModeAPI returns true when running in API-only mode (no background workers).
func IsDaemonModeAPI() bool {
	return GetDaemonMode() == DaemonModeAPI
}

// IsDaemonModeWorker returns true when running in worker mode (background processing).
func IsDaemonModeWorker() bool {
	return GetDaemonMode() == DaemonModeWorker
}

// BrowserAppConfig controls the built-in browser automation module.
// All fields are optional — defaults are applied by browser.DefaultConfig().
type BrowserAppConfig struct {
	// Headless controls whether the browser runs in headless mode.
	// Default: false (headed mode with Xvfb on Linux servers for better stealth).
	// Headed mode produces a more realistic browser fingerprint that avoids
	// detection by strict anti-bot systems. If Xvfb is not available, Astonish
	// falls back to headless mode automatically.
	Headless *bool `yaml:"headless,omitempty" json:"headless,omitempty"`
	// ViewportWidth is the default viewport width in pixels. Default: 1920.
	ViewportWidth int `yaml:"viewport_width,omitempty" json:"viewport_width,omitempty"`
	// ViewportHeight is the default viewport height in pixels. Default: 1080.
	ViewportHeight int `yaml:"viewport_height,omitempty" json:"viewport_height,omitempty"`
	// NoSandbox disables Chrome's sandbox. Auto-detected (true when running as root).
	NoSandbox *bool `yaml:"no_sandbox,omitempty" json:"no_sandbox,omitempty"`
	// ChromePath overrides the Chromium binary path. Empty = auto-download via rod.
	ChromePath string `yaml:"chrome_path,omitempty" json:"chrome_path,omitempty"`
	// UserDataDir overrides the persistent browser profile directory.
	// Empty = ~/.config/astonish/browser/ (persistent profile).
	UserDataDir string `yaml:"user_data_dir,omitempty" json:"user_data_dir,omitempty"`
	// NavigationTimeout is the max seconds to wait for page loads. Default: 30.
	NavigationTimeout int `yaml:"navigation_timeout,omitempty" json:"navigation_timeout,omitempty"`
	// Proxy sets an HTTP or SOCKS proxy for all browser traffic.
	// Format: "http://user:pass@host:port" or "socks5://host:port".
	// Useful for routing through residential proxies to avoid datacenter IP blocks.
	Proxy string `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	// RemoteCDPURL connects to an external browser via Chrome DevTools Protocol
	// instead of launching a local Chrome instance. Use this with anti-detect
	// browsers (AdsPower, Dolphin Anty, Browserless, etc.) that handle
	// fingerprint spoofing at the binary level.
	// Format: "ws://localhost:9222/devtools/browser/<id>" or the CDP endpoint
	// provided by your anti-detect browser.
	// When set, all local launch options (Headless, ChromePath, NoSandbox, Proxy,
	// UserDataDir) are ignored since the external browser manages its own config.
	RemoteCDPURL string `yaml:"remote_cdp_url,omitempty" json:"remote_cdp_url,omitempty"`

	// FingerprintSeed is a deterministic seed for CloakBrowser's fingerprint
	// generation. Each seed produces a unique but consistent browser fingerprint
	// (canvas, WebGL, audio, fonts, TLS, etc.). Only effective when ChromePath
	// points to a CloakBrowser binary. Example: "42000".
	FingerprintSeed string `yaml:"fingerprint_seed,omitempty" json:"fingerprint_seed,omitempty"`
	// FingerprintPlatform overrides the OS platform reported by CloakBrowser.
	// Valid values: "windows", "macos", "linux". Only effective with CloakBrowser.
	FingerprintPlatform string `yaml:"fingerprint_platform,omitempty" json:"fingerprint_platform,omitempty"`

	// HandoffBindAddress controls network binding for browser handoff (human-in-the-loop).
	// "127.0.0.1" for local-only (default), "0.0.0.0" for remote access via SSH tunnel.
	HandoffBindAddress string `yaml:"handoff_bind_address,omitempty" json:"handoff_bind_address,omitempty"`
	// HandoffPort is the TCP port for the CDP handoff proxy. Default: 9222.
	HandoffPort int `yaml:"handoff_port,omitempty" json:"handoff_port,omitempty"`

	// KasmVNCPort is the port KasmVNC listens on inside the browser container. Default: 6901.
	KasmVNCPort int `yaml:"kasmvnc_port,omitempty" json:"kasmvnc_port,omitempty"`
	// KasmVNCPassword is the VNC password for handoff sessions. If empty, a random
	// password is generated per handoff session.
	KasmVNCPassword string `yaml:"kasmvnc_password,omitempty" json:"kasmvnc_password,omitempty"`
}

// AgentIdentityConfig holds the agent's persona for web portal registrations.
// When configured, the agent uses these details to fill registration forms
// and maintain a consistent identity across portal interactions.
type AgentIdentityConfig struct {
	// Name is the display name used for profile registrations.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Username is the base username for registrations. Portal-specific suffixes
	// may be added if the username is taken.
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	// Email is the agent's email address (should match email channel config).
	Email string `yaml:"email,omitempty" json:"email,omitempty"`
	// Bio is a short description for profile fields.
	Bio string `yaml:"bio,omitempty" json:"bio,omitempty"`
	// Website is an optional URL for profile fields.
	Website string `yaml:"website,omitempty" json:"website,omitempty"`
	// Locale is the language/locale preference (e.g. "en-US").
	Locale string `yaml:"locale,omitempty" json:"locale,omitempty"`
	// Timezone is the IANA timezone for profile settings (e.g. "America/New_York").
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

// IsConfigured returns true if at least a name or username is set.
func (c *AgentIdentityConfig) IsConfigured() bool {
	return c.Name != "" || c.Username != "" || c.Email != ""
}

// SubAgentAppConfig holds configuration for the sub-agent delegation system.
type SubAgentAppConfig struct {
	// Enabled controls whether task delegation is available. Default: true (nil means true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// MaxDepth is the maximum delegation nesting depth. Default: 2.
	MaxDepth int `yaml:"max_depth,omitempty" json:"max_depth,omitempty"`
	// MaxConcurrent is the maximum number of parallel sub-agents. Default: 5.
	MaxConcurrent int `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	// TaskTimeoutSec is the per-task timeout in seconds. Default: 300 (5 minutes).
	TaskTimeoutSec int `yaml:"task_timeout_sec,omitempty" json:"task_timeout_sec,omitempty"`
}

// IsSubAgentsEnabled returns whether the sub-agent system is enabled.
// Defaults to true if not explicitly set.
func (c *SubAgentAppConfig) IsSubAgentsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// TaskTimeout returns the per-task timeout as a time.Duration.
func (c *SubAgentAppConfig) TaskTimeout() time.Duration {
	if c.TaskTimeoutSec <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(c.TaskTimeoutSec) * time.Second
}

// SkillsConfig controls the skills system.
type SkillsConfig struct {
	// Enabled controls whether skills are loaded. Default: true (nil means true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// UserDir is the directory for user-defined skills. Default: ~/.config/astonish/skills/
	UserDir string `yaml:"user_dir,omitempty" json:"user_dir,omitempty"`
	// ExtraDirs are additional directories to search for skills.
	ExtraDirs []string `yaml:"extra_dirs,omitempty" json:"extra_dirs,omitempty"`
	// Allowlist restricts which skills are loaded. Empty means all eligible skills.
	Allowlist []string `yaml:"allowlist,omitempty" json:"allowlist,omitempty"`
}

// IsSkillsEnabled returns whether the skills system is enabled.
// Defaults to true if not explicitly set.
func (c *SkillsConfig) IsSkillsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// GetUserSkillsDir returns the user skills directory, defaulting to ~/.config/astonish/skills/
func (c *SkillsConfig) GetUserSkillsDir() string {
	if c.UserDir != "" {
		return c.UserDir
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "astonish", "skills")
}

// TelegramConfig holds configuration for the Telegram channel adapter.
type TelegramConfig struct {
	// Enabled controls whether the Telegram adapter is active. Default: false (nil means false).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// BotToken is the Telegram bot token from BotFather.
	BotToken string `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
	// AllowFrom is a list of allowed Telegram user IDs. Required — at least one
	// user ID must be specified. An empty list blocks all messages (safe default).
	AllowFrom []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
}

// IsTelegramEnabled returns true if Telegram is explicitly enabled.
// Note: After credential migration, the bot token may be in the encrypted
// credential store rather than in BotToken. Callers should resolve the token
// separately if this returns true.
func (c *TelegramConfig) IsTelegramEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// EmailConfig holds configuration for the Email channel adapter.
type EmailConfig struct {
	// Enabled controls whether the Email adapter is active. Default: false (nil means false).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Provider selects the implementation: "imap", "gmail", or "msgraph". Default: "imap".
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// IMAPServer is the IMAP server address (e.g., "imap.gmail.com:993").
	// Not used when Provider is "msgraph".
	IMAPServer string `yaml:"imap_server,omitempty" json:"imap_server,omitempty"`
	// SMTPServer is the SMTP server address (e.g., "smtp.gmail.com:587").
	// Not used when Provider is "msgraph".
	SMTPServer string `yaml:"smtp_server,omitempty" json:"smtp_server,omitempty"`
	// Address is the agent's email address.
	Address string `yaml:"address,omitempty" json:"address,omitempty"`
	// Username is the IMAP/SMTP login username. Often same as Address.
	// Not used when Provider is "msgraph".
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	// Password is the IMAP/SMTP password or app password.
	// After credential migration, this will be empty (stored in encrypted store).
	// Not used when Provider is "msgraph".
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	// Credential is the name of the credential in the encrypted credential store.
	// Required when Provider is "msgraph" — must reference an oauth_authorization_code
	// credential holding the Microsoft Graph OAuth2 tokens.
	Credential string `yaml:"credential,omitempty" json:"credential,omitempty"`
	// PollInterval is seconds between inbox checks. Default: 30.
	PollInterval int `yaml:"poll_interval,omitempty" json:"poll_interval,omitempty"`
	// AllowFrom is a list of email addresses allowed to trigger the agent.
	// Use ["*"] to allow anyone. An empty list blocks all inbound messages.
	AllowFrom []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
	// Folder is the IMAP folder to monitor. Default: "INBOX".
	Folder string `yaml:"folder,omitempty" json:"folder,omitempty"`
	// MarkRead marks processed emails as read. Default: true.
	MarkRead *bool `yaml:"mark_read,omitempty" json:"mark_read,omitempty"`
	// MaxBodyChars truncates long email bodies. Default: 50000.
	MaxBodyChars int `yaml:"max_body_chars,omitempty" json:"max_body_chars,omitempty"`
}

// IsEmailEnabled returns true if the Email channel is explicitly enabled.
func (c *EmailConfig) IsEmailEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// IsMarkRead returns whether processed emails should be marked as read.
// Defaults to true if not set.
func (c *EmailConfig) IsMarkRead() bool {
	if c.MarkRead == nil {
		return true
	}
	return *c.MarkRead
}

// GetPollInterval returns the poll interval in seconds, defaulting to 30.
func (c *EmailConfig) GetPollInterval() int {
	if c.PollInterval <= 0 {
		return 30
	}
	return c.PollInterval
}

// SlackConfig holds configuration for the Slack channel adapter.
type SlackConfig struct {
	// Enabled controls whether the Slack adapter is active. Default: false (nil means false).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Mode selects the transport: "socket" (default) or "events".
	// Socket Mode uses a WebSocket connection (no public URL needed).
	// Events API uses HTTP webhooks (requires public URL, more scalable).
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// BotToken is the bot token (xoxb-...) for the primary workspace.
	// In multi-workspace mode (OAuth), additional tokens are stored per workspace.
	BotToken string `yaml:"bot_token,omitempty" json:"bot_token,omitempty"`
	// AppToken is the app-level token (xapp-...) for Socket Mode.
	// Required only when Mode == "socket".
	AppToken string `yaml:"app_token,omitempty" json:"app_token,omitempty"`
	// SigningSecret is used to verify incoming HTTP requests in Events API mode.
	SigningSecret string `yaml:"signing_secret,omitempty" json:"signing_secret,omitempty"`
	// AppID is the Slack App ID used for App Manifest command synchronization.
	AppID string `yaml:"app_id,omitempty" json:"app_id,omitempty"`
	// ConfigToken is a Slack app configuration token used for App Manifest APIs.
	// Prefer storing it in the credential store as channels.slack.config_token.
	ConfigToken string `yaml:"config_token,omitempty" json:"config_token,omitempty"`
	// CommandURL is the HTTPS endpoint Slack should call for slash commands.
	// Optional for Socket Mode; required for Events API command delivery.
	CommandURL string `yaml:"command_url,omitempty" json:"command_url,omitempty"`
	// ClientID is the Slack App's client ID (for OAuth multi-workspace installs).
	ClientID string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	// ClientSecret is the Slack App's client secret (for OAuth multi-workspace installs).
	ClientSecret string `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`
	// AllowFrom is a list of allowed Slack user IDs. Empty blocks all (safe default).
	// In platform mode, this is dynamically refreshed from user_channels.
	AllowFrom []string `yaml:"allow_from,omitempty" json:"allow_from,omitempty"`
}

// IsSlackEnabled returns true if the Slack channel is explicitly enabled.
func (c *SlackConfig) IsSlackEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// GetMode returns the configured transport mode, defaulting to "socket".
func (c *SlackConfig) GetMode() string {
	if c.Mode == "" {
		return "socket"
	}
	return c.Mode
}

// A2AConfig holds configuration for the A2A (Agent-to-Agent) protocol channel.
type A2AConfig struct {
	// Enabled controls whether the A2A channel is active. Default: false (nil means false).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// BaseURL is the external base URL where the A2A endpoint is reachable.
	// Used in the Agent Card's URL field. Defaults to the daemon's listen address.
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	// TaskTTL is how long completed tasks are retained. Default: "72h".
	TaskTTL string `yaml:"task_ttl,omitempty" json:"task_ttl,omitempty"`
	// DefaultAudience is the expected "aud" claim in incoming A2A JWTs when not overridden per-issuer.
	DefaultAudience string `yaml:"default_audience,omitempty" json:"default_audience,omitempty"`
	// AutoLinkByEmail enables automatic user linking by matching the token's email claim.
	AutoLinkByEmail bool `yaml:"auto_link_by_email,omitempty" json:"auto_link_by_email,omitempty"`
	// RequireActorClaim requires the "act" claim in incoming JWTs for delegation flows.
	RequireActorClaim bool `yaml:"require_actor_claim,omitempty" json:"require_actor_claim,omitempty"`
	// TrustedIssuers lists the trusted token issuers for A2A authentication.
	TrustedIssuers []TrustedIssuerConfig `yaml:"trusted_issuers,omitempty" json:"trusted_issuers,omitempty"`
	// AllowedAgents lists the allowed A2A agents.
	AllowedAgents []AllowedAgentConfig `yaml:"allowed_agents,omitempty" json:"allowed_agents,omitempty"`
}

// TrustedIssuerConfig holds YAML configuration for a trusted A2A token issuer.
type TrustedIssuerConfig struct {
	Name      string `yaml:"name" json:"name"`
	Issuer    string `yaml:"issuer" json:"issuer"`
	JWKSURL   string `yaml:"jwks_url" json:"jwks_url"`
	Audience  string `yaml:"audience" json:"audience"`
	UserClaim string `yaml:"user_claim,omitempty" json:"user_claim,omitempty"` // default: "sub"
}

// AllowedAgentConfig holds YAML configuration for an allowed A2A agent.
type AllowedAgentConfig struct {
	Name      string `yaml:"name" json:"name"`
	ActorSub  string `yaml:"actor_sub" json:"actor_sub"`
	Issuer    string `yaml:"issuer" json:"issuer"` // references TrustedIssuerConfig.Name
	RateLimit int    `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	MaxTasks  int    `yaml:"max_tasks,omitempty" json:"max_tasks,omitempty"`
}

// IsA2AEnabled returns true if the A2A channel is explicitly enabled.
func (c *A2AConfig) IsA2AEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

type ProviderConfig map[string]string

// GetProviderType returns the provider type for a given provider instance.
// For new format: checks explicit "type" field
// For old format (backward compatible): uses instance name if it matches known provider type
// Returns empty string if neither is valid (caller should handle error)
func GetProviderType(instanceName string, instance ProviderConfig) string {
	if instance == nil {
		return ""
	}

	if providerType, ok := instance["type"]; ok && providerType != "" {
		return providerType
	}

	knownTypes := []string{
		"anthropic", "gemini", "groq", "litellm", "lm_studio",
		"ollama", "openai", "openrouter", "poe", "sap_ai_core", "xai",
	}

	for _, knownType := range knownTypes {
		if instanceName == knownType {
			return instanceName
		}
	}

	return ""
}

// GetConfigDir returns the Astonish configuration directory.
// When running under sudo, it resolves the real (non-root) user's home
// directory via SUDO_USER so that config, sessions, and other data files
// are consistent regardless of whether the command was escalated.
func GetConfigDir() (string, error) {
	// Check for SUDO_USER first: when escalated via sudo, $HOME is /root
	// but we want the invoking user's config directory.
	if os.Getuid() == 0 {
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
			if u, err := user.Lookup(sudoUser); err == nil {
				return filepath.Join(u.HomeDir, ".config", "astonish"), nil
			}
		}
	}
	// Honor XDG_CONFIG_HOME on all platforms for portability and test isolation.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "astonish"), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "astonish"), nil
}

func GetConfigPath() (string, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

func GetAgentsDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "agents"), nil
}

// GetModelsDir returns the directory for locally downloaded ML models (e.g., embedding models).
// Defaults to ~/.config/astonish/models/.
func GetModelsDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "models"), nil
}

// GetFleetsDir returns the directory for fleet YAML definitions.
// Defaults to ~/.config/astonish/fleets/.
func GetFleetsDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "fleets"), nil
}

// GetFleetPlansDir returns the directory for custom fleet plan YAML definitions.
// Defaults to ~/.config/astonish/fleet_plans/.
func GetFleetPlansDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "fleet_plans"), nil
}

// GetReportsDir returns the directory for drill/test report artifacts.
// Defaults to ~/.config/astonish/reports/.
func GetReportsDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "reports"), nil
}

// GetSessionsDir returns the session storage directory.
// If the config specifies a custom base_dir, that is used; otherwise
// it defaults to ~/.config/astonish/sessions/.
func GetSessionsDir(cfg *SessionConfig) (string, error) {
	if cfg != nil && cfg.BaseDir != "" {
		return cfg.BaseDir, nil
	}
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "sessions"), nil
}

// GetWorkspacesDir returns the directory for per-session fleet workspaces.
// Fleet sessions create isolated git clones here (one per session).
// Defaults to ~/.config/astonish/workspaces/.
func GetWorkspacesDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "workspaces"), nil
}

func LoadAppConfig() (*AppConfig, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &AppConfig{
			Providers: make(map[string]ProviderConfig),
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}

	return &cfg, nil
}

func SaveAppConfig(cfg *AppConfig) error {
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
