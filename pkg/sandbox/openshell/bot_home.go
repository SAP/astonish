package openshell

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

const botHomeMountPath = "/home/astonish"
const botHomeVolumeName = "astonish-bot-home"
const botHomeScopeLabel = "astonish.io/bot-home"
const botHomeSessionLabel = "astonish.io/bot-session"

var claimNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// EnsureBotHome binds operator-managed retained storage. It does not create or
// delete PVCs. Kubernetes must support scoped sub_path mounts; missing/disabled
// support is an error, never a fallback to the ephemeral workspace.
func (b *OpenShellBackend) EnsureBotHome(ctx context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if !b.cfg.BotHomeMountsEnabled || b.cfg.BotHomeClaimName == "" {
		return nil, fmt.Errorf("openshell: retained PVC sub_path mounts must be configured: %w", sandbox.ErrBotHomeUnsupported)
	}
	claim := b.cfg.BotHomeClaimName
	if len(claim) > 253 || !claimNamePattern.MatchString(claim) || strings.Contains(claim, "..") {
		return nil, fmt.Errorf("openshell: invalid bot home claim name")
	}
	return &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindOpenShell, Ref: claim + "/bot-homes/" + scope.Key()}, nil
}

func (b *OpenShellBackend) validateBotHome(ctx context.Context, binding *sandbox.BotHomeBinding) error {
	if binding == nil {
		return nil
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	expected, err := b.EnsureBotHome(ctx, binding.Scope)
	if err != nil {
		return err
	}
	if !expected.Equal(binding) {
		return fmt.Errorf("openshell: bot home binding does not match configured retained storage")
	}
	return nil
}

// composeBotHome adds exactly one RW scoped mount to the freshly rendered cert
// envelope. Never mount the shared claim root or shadow supervisor/workspace CAs.
func (b *OpenShellBackend) composeBotHome(ctx context.Context, binding *sandbox.BotHomeBinding, env map[string]string, driver map[string]any) (map[string]any, error) {
	if binding == nil {
		return driver, nil
	}
	if err := b.validateBotHome(ctx, binding); err != nil {
		return nil, err
	}
	for _, cert := range b.cfg.AppConfig.CertBundles {
		p := path.Clean(cert.MountPath)
		if cert.Name == botHomeVolumeName || p == botHomeMountPath || strings.HasPrefix(p, botHomeMountPath+"/") || strings.HasPrefix(botHomeMountPath, p+"/") || p == "/" {
			return nil, fmt.Errorf("openshell: cert bundle conflicts with retained bot home")
		}
		if cert.ClaimName == b.cfg.BotHomeClaimName {
			return nil, fmt.Errorf("openshell: cert bundle must not expose the bot home claim")
		}
	}
	if driver != nil {
		// Preserve the caller's cert envelope, even if validation below fails.
		raw, err := json.Marshal(driver)
		if err != nil {
			return nil, fmt.Errorf("openshell: invalid driver config: %w", err)
		}
		var cloned map[string]any
		if err := json.Unmarshal(raw, &cloned); err != nil {
			return nil, err
		}
		driver = cloned
	} else {
		driver = map[string]any{"kubernetes": map[string]any{"volumes": []any{}, "containers": map[string]any{"agent": map[string]any{"volume_mounts": []any{}}}}}
	}
	kube, ok := driver["kubernetes"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("openshell: missing kubernetes driver envelope")
	}
	containers, ok := kube["containers"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("openshell: missing driver containers")
	}
	agent, ok := containers["agent"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("openshell: missing driver agent")
	}
	volumes, ok := kube["volumes"].([]any)
	if !ok {
		return nil, fmt.Errorf("openshell: invalid driver volumes")
	}
	mounts, ok := agent["volume_mounts"].([]any)
	if !ok {
		return nil, fmt.Errorf("openshell: invalid driver mounts")
	}
	for _, value := range volumes {
		volume, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("openshell: invalid driver volume")
		}
		pvc, _ := volume["persistent_volume_claim"].(map[string]any)
		if volume["name"] == botHomeVolumeName || pvc["claim_name"] == b.cfg.BotHomeClaimName {
			return nil, fmt.Errorf("openshell: driver volume conflicts with retained bot home")
		}
	}
	for _, value := range mounts {
		mount, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("openshell: invalid driver mount")
		}
		p, ok := mount["mount_path"].(string)
		if !ok || !path.IsAbs(p) || path.Clean(p) != p {
			return nil, fmt.Errorf("openshell: invalid driver mount path")
		}
		if mount["name"] == botHomeVolumeName || p == botHomeMountPath || strings.HasPrefix(p, botHomeMountPath+"/") || strings.HasPrefix(botHomeMountPath, p+"/") || p == "/" {
			return nil, fmt.Errorf("openshell: driver mount conflicts with retained bot home")
		}
	}
	if env == nil {
		return nil, fmt.Errorf("openshell: missing home environment")
	}
	kube["volumes"] = append(volumes, map[string]any{"name": botHomeVolumeName, "persistent_volume_claim": map[string]any{"claim_name": b.cfg.BotHomeClaimName, "read_only": false}})
	agent["volume_mounts"] = append(mounts, map[string]any{"name": botHomeVolumeName, "mount_path": botHomeMountPath, "sub_path": "bot-homes/" + binding.Scope.Key(), "read_only": false})
	env["HOME"] = botHomeMountPath
	return driver, nil
}

func botHomeLabels(labels map[string]string, sessionID string, binding *sandbox.BotHomeBinding) {
	if binding == nil {
		return
	}
	// Label values are limited to 63 bytes; retain 240 bits of the hash.
	labels[botHomeScopeLabel] = binding.Scope.Key()[:60]
	labels[botHomeSessionLabel] = fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))[:60]
}

// The gateway returns its persisted template. Refuse adoption if the mounts,
// HOME or scoped ownership markers cannot be established (including old gateways).
func validateBotHomeAdoption(st *SandboxStatus, driver map[string]any, labels map[string]string) error {
	if st == nil || st.GatewayID == "" || st.State == SandboxStateGone || st.State == SandboxStateFailed ||
		st.Env["HOME"] != botHomeMountPath || !reflect.DeepEqual(st.DriverConfig, driver) ||
		st.Labels[botHomeScopeLabel] != labels[botHomeScopeLabel] || st.Labels[botHomeSessionLabel] != labels[botHomeSessionLabel] {
		return fmt.Errorf("openshell: existing sandbox does not prove retained bot home mounts and ownership")
	}
	return nil
}

// The session registry's opaque StorageKey preserves the complete scope and
// backend reference, not just a subdirectory or an execution UUID.
func botHomeToStore(binding *sandbox.BotHomeBinding) *store.BotHomeBinding {
	if binding == nil {
		return nil
	}
	raw, _ := json.Marshal(binding)
	return &store.BotHomeBinding{Backend: string(binding.Backend), StorageKey: string(raw)}
}

func botHomeFromStore(rec *store.SandboxSession) (*sandbox.BotHomeBinding, error) {
	if rec.BotHomeBinding == nil {
		return nil, nil
	}
	var binding sandbox.BotHomeBinding
	if err := json.Unmarshal([]byte(rec.BotHomeBinding.StorageKey), &binding); err != nil {
		return nil, fmt.Errorf("openshell: invalid stored bot home: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if string(binding.Backend) != rec.BotHomeBinding.Backend {
		return nil, fmt.Errorf("openshell: stored bot home backend mismatch")
	}
	return &binding, nil
}

func (b *OpenShellBackend) checkBotHomeStatus(ctx context.Context, sessionID string, binding *sandbox.BotHomeBinding, st *SandboxStatus) error {
	env := map[string]string{}
	driver, err := applyCertBundles(b.cfg.AppConfig, env)
	if err != nil {
		return err
	}
	driver, err = b.composeBotHome(ctx, binding, env, driver)
	if err != nil {
		return err
	}
	labels := map[string]string{}
	botHomeLabels(labels, sessionID, binding)
	return validateBotHomeAdoption(st, driver, labels)
}

var _ sandbox.BotHomeBackend = (*OpenShellBackend)(nil)
