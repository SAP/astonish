package openshell

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReadBotHomeArtifact uses a short-lived gateway sandbox, never the daemon's
// filesystem or an existing run. A persisted template alone is not mount proof:
// the command also checks the actual read-only mount and process/home ownership.
func (b *OpenShellBackend) ReadBotHomeArtifact(ctx context.Context, binding *sandbox.BotHomeBinding, relative string, limit int64) (data []byte, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, fmt.Errorf("openshell: home binding is required")
	}
	if err = b.validateBotHome(ctx, binding); err != nil {
		return nil, err
	}
	if err = sandbox.ValidateBotHomeArtifactPath(relative); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > sandbox.MaxBotHomeArtifactBytes {
		return nil, fmt.Errorf("openshell: invalid artifact byte limit")
	}
	if b.gateway == nil {
		return nil, fmt.Errorf("openshell: artifact helper requires gateway: %w", sandbox.ErrBotHomeUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	env := map[string]string{}
	driver, err := applyCertBundles(b.cfg.AppConfig, env)
	if err != nil {
		return nil, err
	}
	driver, err = b.composeBotHome(ctx, binding, env, driver)
	if err != nil {
		return nil, err
	}
	kube := driver["kubernetes"].(map[string]any)
	for _, v := range kube["volumes"].([]any) {
		vol := v.(map[string]any)
		if vol["name"] == botHomeVolumeName {
			vol["persistent_volume_claim"].(map[string]any)["read_only"] = true
		}
	}
	for _, v := range kube["containers"].(map[string]any)["agent"].(map[string]any)["volume_mounts"].([]any) {
		mount := v.(map[string]any)
		if mount["name"] == botHomeVolumeName {
			mount["read_only"] = true
		}
	}
	name := "astn-home-read-" + strings.ToLower(rand.Text())
	labels := map[string]string{"astonish.io/purpose": "bot-home-read"}
	botHomeLabels(labels, name, binding)
	policy := defaultSandboxPolicy(b.cfg.AppConfig)
	policy.Process = &ProcessSpec{RunAsUser: "sandbox", RunAsGroup: "sandbox"}
	policy.Filesystem.IncludeWorkdir = false
	// Do not let the default /home writable rule trigger recursive preparation
	// or confer write access to the retained mount.
	policy.Filesystem.ReadWrite = []string{"/tmp", "/dev/null"}
	policy.Filesystem.ReadOnly = append(policy.Filesystem.ReadOnly, botHomeMountPath, "/proc/self/mountinfo")
	// No artifact needs network access.
	policy.NetworkPolicies = nil
	req := CreateSandboxRequest{Name: name, Image: b.cfg.SandboxImage, Env: env, Labels: labels, Policy: policy, DriverConfig: driver}
	created, createErr := b.gateway.CreateSandbox(ctx, req)
	// Cleanup is independently bounded and survives request cancellation. An
	// ambiguous failed create is deleted only if its persisted ownership matches.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cleanupCancel()
		if createErr != nil {
			st, lookupErr := b.gateway.GetSandboxStatus(cleanupCtx, name)
			if status.Code(lookupErr) == codes.NotFound {
				return
			}
			if lookupErr != nil {
				err = errors.Join(err, fmt.Errorf("openshell: cannot resolve helper creation: %w", lookupErr))
				data = nil
				return
			}
			if proofErr := validateBotHomeAdoption(st, driver, labels); proofErr != nil {
				err = errors.Join(err, proofErr)
				data = nil
				return
			}
		}
		if cleanupErr := b.gateway.DeleteSandbox(cleanupCtx, name); cleanupErr != nil && status.Code(cleanupErr) != codes.NotFound {
			err = errors.Join(err, fmt.Errorf("openshell: cleanup artifact helper: %w", cleanupErr))
			data = nil
		}
	}()
	if createErr != nil {
		return nil, createErr
	}
	if created == nil || created.SandboxID != name {
		return nil, fmt.Errorf("openshell: helper create returned unexpected identity")
	}
	for {
		st, statusErr := b.gateway.GetSandboxStatus(ctx, name)
		if statusErr != nil {
			return nil, statusErr
		}
		if err = validateBotHomeAdoption(st, driver, labels); err != nil {
			return nil, err
		}
		if created.GatewayID != "" && st.GatewayID != created.GatewayID {
			return nil, fmt.Errorf("openshell: helper UUID changed")
		}
		if st.State == SandboxStateRunning {
			result, execErr := b.gateway.ExecCommand(ctx, st.GatewayID, ExecRequest{Command: []string{"python3", "-I", "-c", botHomeArtifactReadScript, relative, strconv.FormatInt(limit, 10), binding.Scope.Key()}, Env: map[string]string{"HOME": botHomeMountPath}, WorkDir: "/"})
			if execErr != nil {
				return nil, execErr
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if result == nil || result.ExitCode != 0 {
				return nil, fmt.Errorf("openshell: artifact helper rejected file or runtime mount/ownership")
			}
			if int64(len(result.Stdout)) > limit {
				return nil, fmt.Errorf("openshell: artifact exceeds byte limit")
			}
			return result.Stdout, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// DeleteBotHome deliberately performs no gateway mutation. Listing one page is
// not proof of detachment; creating a subPath helper can recreate an absent
// directory. Neither an atomic detach lease nor an existing-only mount operation
// is exposed by the gateway contract. Do not turn this into rm -rf on a shared PVC.
func (b *OpenShellBackend) DeleteBotHome(ctx context.Context, binding *sandbox.BotHomeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding == nil {
		return fmt.Errorf("openshell: home binding is required")
	}
	if err := b.validateBotHome(ctx, binding); err != nil {
		return err
	}
	return fmt.Errorf("openshell: purge requires complete gateway inventory, atomic attachment exclusion and existing-only scoped storage access; current gateway API provides none: %w", sandbox.ErrBotHomeUnsupported)
}

// Reject symlinks entirely, including intermediate directories: opening each
// component relative to a pinned directory fd prevents rename/symlink races.
// O_NONBLOCK prevents a substituted FIFO from hanging before fstat rejects it.
const botHomeArtifactReadScript = `import os, pwd, stat, sys
home = "/home/astonish"
relative, limit, key = sys.argv[1], int(sys.argv[2]), sys.argv[3]
user = pwd.getpwnam("sandbox")
assert os.geteuid() == user.pw_uid and os.getegid() == user.pw_gid
assert os.environ.get("HOME") == home and os.path.realpath(home) == home
assert os.stat(home).st_uid == user.pw_uid and os.stat(home).st_gid == user.pw_gid
with open("/proc/self/mountinfo") as mounts:
    matches = [line.split() for line in mounts if line.split()[4] == home]
assert len(matches) == 1
mount = matches[0]
assert mount[3].endswith("/bot-homes/" + key) and "ro" in mount[5].split(",")
resolved = os.path.realpath(os.path.join(home, relative))
assert os.path.commonpath([home, resolved]) == home and resolved != home
parts = relative.split("/")
assert all(p not in ("", ".", "..") for p in parts)
fd = os.open(home, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
try:
    for part in parts[:-1]:
        child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        os.close(fd)
        fd = child
    leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    with os.fdopen(leaf, "rb") as source:
        info = os.fstat(source.fileno())
        assert stat.S_ISREG(info.st_mode) and info.st_size <= limit
        data = source.read(limit + 1)
        assert len(data) <= limit
        sys.stdout.buffer.write(data)
finally:
    os.close(fd)
`

var _ sandbox.BotHomeArtifactBackend = (*OpenShellBackend)(nil)
var _ sandbox.BotHomeDeleteBackend = (*OpenShellBackend)(nil)
