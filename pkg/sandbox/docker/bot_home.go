package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	if b.Backend != sandbox.BackendKindDocker || !validBotHomeVolume(b) {
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
	homeMissing := false
	if err := db.validateBotHome(ctx, b); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such volume") && ctx.Err() == nil {
			homeMissing = true
		} else {
			return err
		}
	}
	attached, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+b.Ref)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(attached)) != 0 {
		return errors.New("sandbox/docker: bot home is attached; storage retained")
	}
	// A private workspace is never forced away while a storage helper is attached.
	private := workspaceVolume(b)
	raw, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", private)
	if e == nil {
		var v []struct {
			Name, Driver    string
			Labels, Options map[string]string
		}
		if json.Unmarshal(raw, &v) != nil || len(v) != 1 || v[0].Name != private || v[0].Driver != "local" || len(v[0].Options) != 0 || v[0].Labels[botHomeScopeLabel] != b.Scope.Key() || v[0].Labels[botHomeVersionLabel] != "workspace-1" {
			return errors.New("private workspace ownership mismatch; retained")
		}
		if _, e = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", private); e != nil {
			return e
		}
	} else if !isDockerNotFoundError(e) && !strings.Contains(strings.ToLower(e.Error()), "no such volume") {
		return e
	}
	if homeMissing {
		return ctx.Err()
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", b.Ref)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such volume") && ctx.Err() == nil {
		return nil
	}
	return err
}

var _ sandbox.BotHomeSessionCleanupBackend = (*DockerBackend)(nil)

type botCleanupContainer struct {
	ID, Name string
	Config   struct{ Labels map[string]string }
	Mounts   []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

func (db *DockerBackend) inspectBotCleanupContainer(ctx context.Context, ref string) (*botCleanupContainer, error) {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", ref)
	if err != nil {
		return nil, err
	}
	var c botCleanupContainer
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.ID == "" {
		return nil, errors.New("sandbox/docker: missing immutable container identity")
	}
	return &c, nil
}

// ReconcileBotHomeSessions is only for service-authorized retirement, never raw
// volume deletion. Labels alone are not ownership: require the recovery binding,
// scoped registry owner, canonical mounts and exact immutable container identity.
func (db *DockerBackend) ReconcileBotHomeSessions(ctx context.Context, b *sandbox.BotHomeBinding, authorize func(context.Context) error) error {
	if err := validateBotHomeReference(b); err != nil {
		return err
	}
	if authorize == nil {
		return errors.New("sandbox/docker: cleanup authorization required")
	}
	if err := authorize(ctx); err != nil {
		return err
	}
	if db.cfg.Sessions == nil {
		return errors.New("sandbox/docker: cleanup registry required")
	}
	if err := db.validateBotHome(ctx, b); err != nil && !(ctx.Err() == nil && strings.Contains(strings.ToLower(err.Error()), "no such volume")) {
		return err
	}
	ids := map[string]bool{}
	attachments := map[string]string{}
	attached, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--no-trunc", "--filter=volume="+b.Ref)
	if err != nil {
		return err
	}
	for _, ref := range strings.Fields(string(attached)) {
		c, err := db.inspectBotCleanupContainer(ctx, ref)
		if err != nil {
			return err
		}
		id := c.Config.Labels["astonish.session_id"]
		if id == "" {
			return errors.New("sandbox/docker: unknown home attachment; retained")
		}
		if c.Name != "/"+containerName(id) || (attachments[id] != "" && attachments[id] != c.ID) {
			return errors.New("sandbox/docker: ambiguous home attachment; retained")
		}
		attachments[id] = c.ID
		ids[id] = true
	}
	// A previous attempt may already have removed the container but failed on its
	// overlay or private recovery files. Discover only exact-binding sidecars; no
	// global Docker prune, registry enumeration or adoption of unregistered runs.
	entries, err := os.ReadDir(db.cfg.UppersDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(db.cfg.UppersDir, entry.Name(), dockerSessionMetadataFile))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		var m dockerSessionMetadata
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("sandbox/docker: cannot attribute recovery storage: %w", err)
		}
		if m.BotHome.Equal(b) {
			ids[entry.Name()] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	// Lock all candidates in deterministic order, then validate all before deleting
	// any. These are the same locks Create/Start/Destroy use across backend instances.
	var unlocks []func()
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	for _, id := range ordered {
		// Bot execution sessions are UUIDs. Reject path traversal and truncation aliases.
		if parsed, e := uuid.Parse(id); e != nil || parsed.String() != id {
			return errors.New("sandbox/docker: invalid Bot session identity; retained")
		}
		unlock, e := db.lockSession(ctx, id)
		if e != nil {
			return e
		}
		unlocks = append(unlocks, unlock)
	}
	refs := map[string]string{}
	for _, id := range ordered {
		ref, e := db.validateBotCleanupSession(ctx, b, id)
		if e != nil {
			return e
		}
		if initial := attachments[id]; initial != "" && initial != ref {
			return errors.New("sandbox/docker: discovered attachment changed; retained")
		}
		refs[id] = ref
	}
	for _, id := range ordered {
		if err := authorize(ctx); err != nil {
			return err
		}
		ref, e := db.validateBotCleanupSession(ctx, b, id)
		if e != nil {
			return e
		}
		if ref != refs[id] {
			return errors.New("sandbox/docker: attachment changed during cleanup; retained")
		}
		// A durable receipt may outlive its registry deletion. Validation above
		// permits that case only when container AND overlay are already absent;
		// never use the receipt alone to delete a Docker resource.
		rec, err := db.cfg.Sessions.GetSession(id)
		if err != nil {
			return err
		}
		if rec == nil {
			if _, err := db.validateBotCleanupSession(ctx, b, id); err != nil {
				return err
			}
			if err := os.RemoveAll(db.upperPath(id)); err != nil {
				return err
			}
			sessionLaunchEnvs.Delete(sessionEnvKey{db, id})
			continue
		}
		if err := db.destroySessionLocked(ctx, id, ref); err != nil {
			return err
		}
	}
	// Includes unknown attachments created since discovery. Raw deletion performs
	// another reference check and Docker itself refuses in-use volume removal.
	attached, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+b.Ref)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(attached)) != 0 {
		return errors.New("sandbox/docker: home attachment remains; retained")
	}
	return ctx.Err()
}

func (db *DockerBackend) validateBotCleanupSession(ctx context.Context, b *sandbox.BotHomeBinding, id string) (string, error) {
	m, err := db.readSessionMetadata(id)
	if err != nil {
		return "", err
	}
	if m == nil || !m.BotHome.Equal(b) || m.UserID != b.Scope.UserID || m.OrgSlug != b.Scope.OrgID || m.TeamSlug != b.Scope.TeamID {
		return "", errors.New("sandbox/docker: cleanup recovery ownership mismatch; retained")
	}
	db.cfg.Sessions.SetSessionScope(id, b.Scope.OrgID, b.Scope.TeamID)
	rec, err := db.cfg.Sessions.GetSession(id)
	if err != nil {
		return "", err
	}
	if rec == nil && m.CleanupPending {
		// This receipt authorizes only final local cleanup, not container or
		// volume destruction without a registry owner. Replacements fail closed.
		if _, err := db.inspectBotCleanupContainer(ctx, containerName(id)); err == nil {
			return "", errors.New("sandbox/docker: container exists after cleanup receipt; retained")
		} else if !isDockerNotFoundError(err) {
			return "", err
		}
		if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", overlayVolumeName(id)); err == nil {
			return "", errors.New("sandbox/docker: overlay exists after cleanup receipt; retained")
		} else if !isDockerNotFoundError(err) && !strings.Contains(strings.ToLower(err.Error()), "no such volume") {
			return "", err
		}
		return "", ctx.Err()
	}
	if rec == nil || rec.SessionID != id || rec.CreatedBy != b.Scope.UserID || rec.Backend != string(sandbox.BackendKindDocker) || rec.ContainerName != containerName(id) {
		return "", errors.New("sandbox/docker: cleanup registry ownership mismatch; retained")
	}
	c, err := db.inspectBotCleanupContainer(ctx, containerName(id))
	if err != nil {
		if isDockerNotFoundError(err) {
			return "", nil // never remove by name after observing absence
		}
		return "", err
	}
	labels := c.Config.Labels
	if c.Name != "/"+containerName(id) || labels["astonish.session_id"] != id || labels[botHomeScopeLabel] != b.Scope.Key() || labels["astonish.org"] != b.Scope.OrgID || labels["astonish.team"] != b.Scope.TeamID {
		return "", errors.New("sandbox/docker: cleanup container identity mismatch; retained")
	}
	home, overlay, upper, layers, private := false, false, false, false, false
	for _, mount := range c.Mounts {
		switch mount.Destination {
		case botHomeMount:
			home = mount.Type == "volume" && mount.Name == b.Ref && mount.RW
		case botPrivateMount:
			// Older sessions have four mounts. New sessions additionally mount
			// only the exact private workspace for this Home (including restore
			// generation); its path alone is never cleanup authority.
			if private || mount.Type != "volume" || mount.Name != workspaceVolume(b) || !mount.RW {
				return "", errors.New("sandbox/docker: cleanup private mount mismatch; retained")
			}
			private = true
		case mountOverlay:
			overlay = mount.Type == "volume" && mount.Name == overlayVolumeName(id) && mount.RW
		case mountUppers:
			upper = mount.Type == "bind" && filepath.Clean(mount.Source) == filepath.Clean(db.upperPath(id)) && mount.RW
		case mountLayers:
			layers = mount.Type == "bind" && filepath.Clean(mount.Source) == filepath.Clean(db.cfg.LayersDir) && !mount.RW
		default:
			return "", errors.New("sandbox/docker: unexpected cleanup mount; retained")
		}
	}
	expectedMounts := 4
	if private {
		expectedMounts++
	}
	if !home || !overlay || !upper || !layers || len(c.Mounts) != expectedMounts {
		return "", errors.New("sandbox/docker: cleanup mount mismatch; retained")
	}
	return c.ID, nil
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
	if binding.Backend != sandbox.BackendKindDocker || !validBotHomeVolume(binding) {
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
