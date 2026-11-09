package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/sandbox"
)

const botHomeMount = "/mnt/astonish-bot-home"
const botHomeScopeLabel = "astonish.bot-home.scope"
const botHomeVersionLabel = "astonish.bot-home.version"

func botHomeVolume(scope sandbox.BotHomeScope) string { return "astonish-bot-home-" + scope.Key() }

var _ sandbox.BotHomeBackend = (*DockerBackend)(nil)
var _ sandbox.BotHomeArtifactBackend = (*DockerBackend)(nil)
var _ sandbox.BotHomeDeleteBackend = (*DockerBackend)(nil)

const botHomeReaderLabel = "astonish.bot-home.reader"

func validateBotHomeReference(b *sandbox.BotHomeBinding) error {
	if b == nil {
		return errors.New("sandbox/docker: missing bot home binding")
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if b.Backend != sandbox.BackendKindDocker || b.Ref != botHomeVolume(b.Scope) {
		return errors.New("sandbox/docker: invalid bot home binding")
	}
	return nil
}

// DeleteBotHome never detaches containers or forces deletion. Docker's volume
// reference check also closes a concurrent attachment race after our preflight.
func (db *DockerBackend) DeleteBotHome(ctx context.Context, b *sandbox.BotHomeBinding) error {
	if err := validateBotHomeReference(b); err != nil {
		return err
	}
	if err := db.validateBotHome(ctx, b); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such volume") && ctx.Err() == nil {
			return nil
		}
		return err
	}
	attached, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+b.Ref)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(attached)) != 0 {
		return errors.New("sandbox/docker: bot home is attached; storage retained")
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", b.Ref)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such volume") && ctx.Err() == nil {
		return nil
	}
	return err
}

// The descriptor, not merely a pre-open pathname, is checked for containment.
// No home-provided code runs: entrypoint, script and executable are fixed. A
// racing symlink cannot redirect bytes to an image file outside the volume.
const botHomeReadScript = `set -eu
p=$(readlink -f -- "/mnt/astonish-bot-home/$1")
case "$p" in /mnt/astonish-bot-home/*) ;; *) exit 41;; esac
test -f "$p"
exec 3<"$p"
p=$(readlink -f /proc/self/fd/3)
case "$p" in /mnt/astonish-bot-home/*) ;; *) exit 42;; esac
test -f /proc/self/fd/3
test "$(stat -Lc %s /proc/self/fd/3)" -le "$2"
head -c "$(( $2 + 1 ))" <&3
`

// artifactBuffer bounds CLI output as well as the container-side read. Returning
// an error stops exec's copy loop; the operation context bounds process lifetime.
type artifactBuffer struct {
	buffer    bytes.Buffer
	remaining int64
}

func (b *artifactBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *artifactBuffer) String() string { return b.buffer.String() }

func (b *artifactBuffer) Write(p []byte) (int, error) {
	if int64(len(p)) > b.remaining {
		return 0, errors.New("sandbox/docker: artifact exceeds byte limit")
	}
	b.remaining -= int64(len(p))
	return b.buffer.Write(p)
}

func (db *DockerBackend) ReadBotHomeArtifact(ctx context.Context, b *sandbox.BotHomeBinding, path string, maxBytes int64) (data []byte, err error) {
	if err = validateBotHomeReference(b); err != nil {
		return nil, err
	}
	if err = sandbox.ValidateBotHomeArtifactPath(path); err != nil {
		return nil, err
	}
	if maxBytes < 1 || maxBytes > sandbox.MaxBotHomeArtifactBytes {
		return nil, errors.New("sandbox/docker: invalid artifact byte limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	name := "astonish-home-reader-" + uuid.NewString()
	// Exact nonce ownership is checked even when create returns an ambiguous
	// transport error. Never remove an unrelated container by a broad filter.
	defer func() {
		defer func() {
			if err != nil {
				data = nil
			}
		}()
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		raw, e := runDocker(cleanup, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Config.Labels}}", name)
		if e != nil {
			if !isDockerNotFoundError(e) {
				err = errors.Join(err, e)
			}
			return
		}
		var labels map[string]string
		if json.Unmarshal(raw, &labels) != nil || labels[botHomeReaderLabel] != name || labels[botHomeScopeLabel] != b.Scope.Key() {
			err = errors.Join(err, errors.New("sandbox/docker: reader cleanup ownership mismatch"))
			return
		}
		_, e = runDocker(cleanup, db.cfg.ContainerRuntimePath, "rm", "-fv", name)
		err = errors.Join(err, e)
		if err != nil {
			data = nil
		}
	}()
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "container", "create", "--name="+name,
		"--label="+botHomeReaderLabel+"="+name, "--label="+botHomeScopeLabel+"="+b.Scope.Key(),
		"--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=16", "--memory=64m", "--cpus=0.5", "--user=0:0", "--log-driver=none",
		"--mount=type=volume,source="+b.Ref+",target="+botHomeMount+",readonly,volume-nocopy",
		"--entrypoint=/bin/sh", db.cfg.SandboxImage, "-c", botHomeReadScript, "home-reader", path, strconv.FormatInt(maxBytes, 10))
	if err != nil {
		return nil, err
	}
	// A removed/recreated volume must not be silently adopted by docker create.
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	mountJSON, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Mounts}}", name)
	if e != nil {
		return nil, e
	}
	var mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
	if json.Unmarshal(mountJSON, &mounts) != nil || len(mounts) != 1 || mounts[0].Type != "volume" || mounts[0].Name != b.Ref || mounts[0].Destination != botHomeMount || mounts[0].RW {
		return nil, errors.New("sandbox/docker: reader mount mismatch")
	}
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "start", "--attach", name)
	stdout := &artifactBuffer{remaining: maxBytes}
	stderr := &artifactBuffer{remaining: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("sandbox/docker: artifact read: %w: %s", err, stderr.String())
	}
	// docker start --attach does not consistently propagate the process exit.
	raw, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{.State.ExitCode}}", name)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(string(raw)) != "0" {
		return nil, errors.New("sandbox/docker: confined artifact read failed")
	}
	return stdout.Bytes(), nil
}

// EnsureBotHome provisions only a local, independently retained named volume.
// Inspect after create also detects a concurrent conflicting adoption. Never
// accept driver options (which could turn a named volume into a host bind).
func (db *DockerBackend) EnsureBotHome(ctx context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	binding := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindDocker, Ref: botHomeVolume(scope)}
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", binding.Ref)
	if err != nil {
		if !isDockerNotFoundError(err) && !strings.Contains(strings.ToLower(err.Error()), "no such volume") {
			return nil, err
		}
		_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "create", "--driver=local", "--label="+botHomeScopeLabel+"="+scope.Key(), "--label="+botHomeVersionLabel+"=1", binding.Ref)
		if err != nil {
			return nil, err
		}
	}
	if err := db.validateBotHome(ctx, binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func (db *DockerBackend) validateBotHome(ctx context.Context, binding *sandbox.BotHomeBinding) error {
	if binding == nil {
		return nil
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if binding.Backend != sandbox.BackendKindDocker || binding.Ref != botHomeVolume(binding.Scope) {
		return fmt.Errorf("sandbox/docker: invalid bot home binding")
	}
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", binding.Ref)
	if err != nil {
		return fmt.Errorf("sandbox/docker: retained bot home unavailable: %w", err)
	}
	var info []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return err
	}
	if len(info) != 1 || info[0].Name != binding.Ref || info[0].Driver != "local" || len(info[0].Options) != 0 || info[0].Labels[botHomeScopeLabel] != binding.Scope.Key() || info[0].Labels[botHomeVersionLabel] != "1" {
		return fmt.Errorf("sandbox/docker: bot home volume ownership or driver mismatch; storage retained")
	}
	return nil
}

func (db *DockerBackend) validateBotHomeContainer(ctx context.Context, id string, binding *sandbox.BotHomeBinding) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Mounts}}", containerName(id))
	if err != nil {
		return err
	}
	var mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return err
	}
	for _, m := range mounts {
		if m.Destination == botHomeMount {
			if binding != nil && m.Type == "volume" && m.Name == binding.Ref && m.RW {
				return nil
			}
			return fmt.Errorf("sandbox/docker: conflicting bot home container mount")
		}
	}
	if binding != nil {
		return fmt.Errorf("sandbox/docker: missing bot home container mount")
	}
	return nil
}

// Mount after composition, never below the overlay before it is mounted. Both
// source and target are fixed; reject symlink targets and mismatched existing
// mounts. chroot exec uses /root as HOME. The separate volume never enters the
// upper-layer capture or run-volume destruction path.
func botHomeMountScript() string {
	return strings.Join([]string{
		"set -eu",
		"exec 9>/run/astonish-bot-home.lock",
		"flock -x 9",
		"mountpoint -q " + botHomeMount,
		"mountpoint -q /sandbox/rootfs",
		"test -x /sandbox/rootfs/bin/sh",
		"test ! -L /sandbox/rootfs/root",
		"mkdir -p /sandbox/rootfs/root",
		"if mountpoint -q /sandbox/rootfs/root; then",
		"  test \"$(stat -c '%d:%i' /sandbox/rootfs/root)\" = \"$(stat -c '%d:%i' " + botHomeMount + ")\"",
		"else",
		"  chmod 0700 " + botHomeMount,
		"  mount --bind " + botHomeMount + " /sandbox/rootfs/root",
		"fi",
	}, "\n")
}

func (db *DockerBackend) prepareBotHomeExec(ctx context.Context, id string) error {
	m, err := db.readSessionMetadata(id)
	if err != nil {
		return err
	}
	if m == nil || m.BotHome == nil {
		return nil
	}
	if err := db.validateBotHome(ctx, m.BotHome); err != nil {
		return err
	}
	if err := db.validateBotHomeContainer(ctx, id, m.BotHome); err != nil {
		return err
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "exec", containerName(id), "/bin/sh", "-c", botHomeMountScript())
	if err != nil {
		return fmt.Errorf("sandbox/docker: bot home mount not ready: %w", err)
	}
	return nil
}
