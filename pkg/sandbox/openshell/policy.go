package openshell

import (
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
)

// defaultSandboxPolicy returns the SandboxPolicySpec applied to every sandbox
// created by the Astonish backend.
//
// Key design choices:
//   - Landlock compatibility defaults to "best_effort" so the supervisor
//     degrades gracefully on kernels without Landlock LSM. Configurable via
//     SandboxOpenShellConfig.LandlockCompatibility.
//   - System paths are read-only (executables, libraries, config).
//   - Workspace, temp, and runtime paths are read-write.
//   - /dev/pts is read-write — required for PTY allocation inside sandboxes
//     running on kernel 6.10+ (Landlock ABI v5 restricts
//     LANDLOCK_ACCESS_FS_IOCTL_DEV). The supervisor pre-opens PathFds for
//     this path before landlock_restrict_self(). /dev/ptmx is NOT listed
//     because it's a symlink (supervisor refuses to chown symlinks).
//   - /root is NOT included — the sandboxed process runs as user "sandbox"
//     with home directory at /sandbox.
//   - Network: the supervisor's proxy denies all egress when no
//     NetworkPolicies are set. We ALWAYS populate the policy with resolved
//     presets so that sandboxes have usable internet access.
//   - Operators can extend paths via FilesystemPolicy.ExtraReadOnly/ExtraReadWrite.
func defaultSandboxPolicy(cfg config.SandboxOpenShellConfig) *SandboxPolicySpec {
	// Landlock compatibility mode — default to "best_effort" which degrades
	// gracefully on kernels without Landlock support.
	landlockCompat := cfg.LandlockCompatibility
	if landlockCompat == "" {
		landlockCompat = "best_effort"
	}

	policy := &SandboxPolicySpec{
		Version: 1,
		Landlock: &LandlockSpec{
			Compatibility: landlockCompat,
		},
		Filesystem: &FilesystemSpec{
			IncludeWorkdir: true,
			ReadOnly: []string{
				"/usr",
				"/bin",
				"/sbin",
				"/lib",
				"/lib64",
				"/etc",
				"/opt",
				// Device nodes needed by standard library functions.
				"/dev/urandom",
			},
			ReadWrite: []string{
				"/sandbox",
				"/tmp",
				"/var/tmp",
				"/home",
				"/run",
				// /dev/null must be read-write — shell redirections (>/dev/null)
				// require write access, not just read.
				"/dev/null",
				// PTY device nodes — required for shell_command's interactive
				// terminal support (password prompts, interactive CLIs).
				// The supervisor pre-opens PathFds for these paths BEFORE
				// calling landlock_restrict_self(), so ioctl on /dev/pts/ptmx
				// is permitted even under Landlock ABI v5 (kernel 6.10+)
				// which restricts LANDLOCK_ACCESS_FS_IOCTL_DEV.
				//
				// NOTE: /dev/ptmx is typically a symlink to /dev/pts/ptmx —
				// the supervisor refuses to chown symlinks (privilege escalation
				// risk). We list the real paths instead:
				//   /dev/pts      — devpts mountpoint (slave PTY devices)
				//   /dev/pts/ptmx — actual PTY master device file
				"/dev/pts",
			},
		},
	}

	// Append operator-supplied extra filesystem paths.
	if len(cfg.FilesystemPolicy.ExtraReadOnly) > 0 {
		policy.Filesystem.ReadOnly = append(policy.Filesystem.ReadOnly, cfg.FilesystemPolicy.ExtraReadOnly...)
	}
	if len(cfg.FilesystemPolicy.ExtraReadWrite) > 0 {
		policy.Filesystem.ReadWrite = append(policy.Filesystem.ReadWrite, cfg.FilesystemPolicy.ExtraReadWrite...)
	}
	// Cert-bundle mount paths are also added in applyCertBundles at create
	// time; listing them here keeps defaultSandboxPolicy self-contained for
	// callers that only use the policy helper.
	for _, b := range cfg.CertBundles {
		if p := strings.TrimSpace(b.MountPath); p != "" {
			policy.Filesystem.ReadOnly = append(policy.Filesystem.ReadOnly, p)
		}
	}

	// Always populate NetworkPolicies — empty = deny-all in OpenShell.
	// ResolvePresets expands the configured presets + extra endpoints into
	// a concrete list of allowed host:port entries.
	endpoints := ResolvePresets(cfg.NetworkPolicy)
	if len(endpoints) > 0 {
		policy.NetworkPolicies = map[string]*NetworkPolicySpec{
			"egress": {
				Name:      "astonish-egress",
				Endpoints: endpoints,
				Binaries:  []string{"/**"},
			},
		}
	}

	return policy
}

// botHomePolicy leaves normal sessions unchanged. The supervisor prepares the
// explicit writable home for the image's sandbox user before privilege drop.
func botHomePolicy(policy *SandboxPolicySpec, binding *sandbox.BotHomeBinding) *SandboxPolicySpec {
	if binding != nil {
		policy.Filesystem.ReadWrite = append(policy.Filesystem.ReadWrite, botHomeMountPath)
		policy.Process = &ProcessSpec{RunAsUser: "sandbox", RunAsGroup: "sandbox"}
	}
	return policy
}

// mergeSessionNetworkAllows appends SessionSpec allow endpoints into the
// astonish-egress rule (creating the rule if presets produced none).
func mergeSessionNetworkAllows(policy *SandboxPolicySpec, extra []sandbox.NetworkAllowEndpoint) *SandboxPolicySpec {
	if policy == nil || len(extra) == 0 {
		return policy
	}
	converted := make([]EndpointSpec, 0, len(extra))
	for _, ep := range extra {
		if ep.Host == "" {
			continue
		}
		converted = append(converted, EndpointSpec{Host: ep.Host, Port: ep.Port})
	}
	if len(converted) == 0 {
		return policy
	}
	if policy.NetworkPolicies == nil {
		policy.NetworkPolicies = map[string]*NetworkPolicySpec{}
	}
	egress, ok := policy.NetworkPolicies["egress"]
	if !ok || egress == nil {
		egress = &NetworkPolicySpec{
			Name:     "astonish-egress",
			Binaries: []string{"/**"},
		}
		policy.NetworkPolicies["egress"] = egress
	}
	egress.Endpoints = append(egress.Endpoints, converted...)
	return policy
}
