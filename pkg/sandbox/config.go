package sandbox

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

// DefaultSandboxConfig returns sensible defaults for the sandbox system.
func DefaultSandboxConfig() config.SandboxConfig {
	enabled := true
	return config.SandboxConfig{
		Enabled: &enabled,
		Limits: config.SandboxLimits{
			Memory:    "2GB",
			CPU:       2,
			Processes: 500,
		},
		Network: "bridged",
		Prune: config.SandboxPruneConfig{
			OrphanCheckHours:   6,
			IdleTimeoutMinutes: 60,
		},
	}
}

// IsSandboxEnabled returns whether sandbox is enabled. Defaults to true when nil.
func IsSandboxEnabled(c *config.SandboxConfig) bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// validMemoryPattern matches both Incus-style memory limit strings (e.g.
// "512MB", "2GB") and K8s-style binary suffixes (e.g. "256Mi", "2Gi").
var validMemoryPattern = regexp.MustCompile(`(?i)^\d+\s*(kB|MB|GB|TB|EB|PB|Ki|Mi|Gi|Ti|Pi|Ei)$`)

// ValidateSandboxConfig checks the sandbox configuration for errors.
func ValidateSandboxConfig(c *config.SandboxConfig) error {
	if c.Network != "" && c.Network != "bridged" && c.Network != "none" {
		return fmt.Errorf("sandbox.network must be 'bridged' or 'none', got %q", c.Network)
	}
	if c.Limits.Memory != "" && !validMemoryPattern.MatchString(strings.TrimSpace(c.Limits.Memory)) {
		return fmt.Errorf("sandbox.limits.memory must be a valid size (e.g. '2GB', '512MB', '2Gi', '256Mi'), got %q", c.Limits.Memory)
	}
	if c.Limits.CPU < 0 {
		return fmt.Errorf("sandbox.limits.cpu must be >= 0, got %d", c.Limits.CPU)
	}
	if c.Limits.Processes < 0 {
		return fmt.Errorf("sandbox.limits.processes must be >= 0, got %d", c.Limits.Processes)
	}
	if c.Limits.Requests.CPUMillis < 0 {
		return fmt.Errorf("sandbox.limits.requests.cpu_millis must be >= 0, got %d", c.Limits.Requests.CPUMillis)
	}
	if c.Limits.Requests.MemoryMiB < 0 {
		return fmt.Errorf("sandbox.limits.requests.memory_mib must be >= 0, got %d", c.Limits.Requests.MemoryMiB)
	}
	if c.Limits.Requests.CPUMillis > 0 && c.Limits.CPU > 0 && c.Limits.Requests.CPUMillis > c.Limits.CPU*1000 {
		return fmt.Errorf("sandbox.limits.requests.cpu_millis (%d) must not exceed sandbox.limits.cpu (%d) × 1000", c.Limits.Requests.CPUMillis, c.Limits.CPU)
	}
	if c.Limits.Requests.MemoryMiB > 0 && c.Limits.Memory != "" {
		limitMiB := parseMemoryToMiB(c.Limits.Memory)
		if limitMiB > 0 && c.Limits.Requests.MemoryMiB > limitMiB {
			return fmt.Errorf("sandbox.limits.requests.memory_mib (%d) must not exceed sandbox.limits.memory (%q = %d MiB)", c.Limits.Requests.MemoryMiB, c.Limits.Memory, limitMiB)
		}
	}
	if c.Prune.OrphanCheckHours < 0 {
		return fmt.Errorf("sandbox.prune.orphan_check_hours must be >= 0, got %d", c.Prune.OrphanCheckHours)
	}
	if c.Prune.IdleTimeoutMinutes < 0 {
		return fmt.Errorf("sandbox.prune.idle_timeout_minutes must be >= 0, got %d", c.Prune.IdleTimeoutMinutes)
	}
	return nil
}

// sandboxCfg stores the active sandbox configuration at package level.
// Set via SetSandboxConfig during sandbox initialization. Used by
// IsPrivileged.
var sandboxCfg *config.SandboxConfig

type sandboxConfigProvider struct{}

func (sandboxConfigProvider) IsPrivileged() bool {
	if sandboxCfg != nil && sandboxCfg.Privileged != nil {
		return *sandboxCfg.Privileged
	}
	return false
}

// SetSandboxConfig stores the sandbox configuration at package level for
// use by container creation functions.
func SetSandboxConfig(c *config.SandboxConfig) {
	sandboxCfg = c
}

// IsPrivileged determines whether containers should run in privileged mode.
// Resolution order:
//  1. Explicit user config (sandbox.privileged: true/false) — always honored
//  2. Default: false (unprivileged) on all platforms.
//
// Unprivileged containers use user namespaces to map container root (UID 0) to
// an unprivileged host UID (e.g., 100000+), preventing container-to-host escapes.
// The overlay system pre-seeds the Incus idmap state so that UID shifting is
// skipped at container start time (the template snapshot already has shifted UIDs).
//
// Users can set sandbox.privileged: true for environments that require it
// (e.g., nested LXC on Proxmox where /proc can't mount in double-nested
// user namespaces).
func IsPrivileged() bool {
	return sandboxConfigProvider{}.IsPrivileged()
}

// containerSecurityConfig returns the security-related config keys for a
// container. Docker OverlayFS ignores Incus-style syscall keys; this map
// is retained for privileged-mode tests.
func containerSecurityConfig() map[string]string {
	if IsPrivileged() {
		return map[string]string{"security.privileged": "true"}
	}
	return map[string]string{"security.privileged": "false"}
}

// EffectiveLimits returns the limits with defaults filled in for any zero values.
func EffectiveLimits(c *config.SandboxConfig) config.SandboxLimits {
	defaults := DefaultSandboxConfig().Limits
	l := c.Limits
	if l.Memory == "" {
		l.Memory = defaults.Memory
	}
	if l.CPU == 0 {
		l.CPU = defaults.CPU
	}
	if l.Processes == 0 {
		l.Processes = defaults.Processes
	}
	return l
}

// EffectiveNetwork returns the network mode with default filled in.
func EffectiveNetwork(c *config.SandboxConfig) string {
	if c.Network == "" {
		return "bridged"
	}
	return c.Network
}

// EffectiveIdleTimeout returns the idle timeout duration for sandbox containers.
// Uses the configured value if > 0, otherwise falls back to the default (10 min).
func EffectiveIdleTimeout(c *config.SandboxConfig) time.Duration {
	if c.Prune.IdleTimeoutMinutes > 0 {
		return time.Duration(c.Prune.IdleTimeoutMinutes) * time.Minute
	}
	defaults := DefaultSandboxConfig()
	return time.Duration(defaults.Prune.IdleTimeoutMinutes) * time.Minute
}
