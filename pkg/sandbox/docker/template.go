// template.go — Template build, save, refresh, and delete for DockerBackend.
//
// Templates in the Docker backend are overlay layer directories stored under
// LayersDir. Each template has a content-addressed directory named by the
// SHA-256 of its rootfs tar (mirroring the K8s CephFS approach but on a local
// filesystem or bind-mounted volume).
//
// BuildTemplate:
//  1. Starts a throwaway container on top of the parent layer chain.
//  2. Runs each step command via Exec.
//  3. On success, captures the container's upper directory as a new layer by
//     copying it to LayersDir/<sha256>.
//  4. Removes the throwaway container.
//
// SaveSessionAsTemplate: copies the session's upper directory as a new layer.
// RefreshTemplate: re-runs the build steps (same as BuildTemplate but with the
// same template ID).
// DeleteTemplate: removes the layer directory.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// SeedBaseLayerFromImage exports the sandbox image filesystem into
// layers/@base/rootfs so the overlay entrypoint has a lowerdir. Idempotent
// if that directory already exists and is non-empty.
func (db *DockerBackend) SeedBaseLayerFromImage(ctx context.Context) error {
	rootfs := db.layerRootfs(sandbox.BaseTemplateID)
	if entries, err := os.ReadDir(rootfs); err == nil && len(entries) > 0 {
		return nil
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return fmt.Errorf("sandbox/docker: mkdir %s: %w", rootfs, err)
	}

	image := db.cfg.SandboxImage
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "pull", image); err != nil {
		return fmt.Errorf("sandbox/docker: docker pull %s: %w", image, err)
	}

	tmpName := "astonish-seed-base"
	_, _ = runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", "-f", tmpName)
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "create", "--name", tmpName, image); err != nil {
		return fmt.Errorf("sandbox/docker: docker create seed: %w", err)
	}
	defer func() {
		_, _ = runDocker(context.Background(), db.cfg.ContainerRuntimePath, "rm", "-f", tmpName)
	}()

	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "export", tmpName)
	tarCmd := exec.CommandContext(ctx, "tar", "-C", rootfs, "-xf", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tarCmd.Stdin = stdout
	var exportErr, tarErr error
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sandbox/docker: docker export: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("sandbox/docker: tar extract seed: %w", err)
	}
	tarErr = tarCmd.Wait()
	exportErr = cmd.Wait()
	if exportErr != nil {
		return fmt.Errorf("sandbox/docker: docker export: %w", exportErr)
	}
	if tarErr != nil {
		return fmt.Errorf("sandbox/docker: extract seed rootfs: %w", tarErr)
	}
	return nil
}

// ReseedBaseLayerFromImage deletes the current @base layer and re-exports the
// sandbox image. Used by `astonish sandbox refresh` / reset.
func (db *DockerBackend) ReseedBaseLayerFromImage(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	baseDir := db.layerDir(sandbox.BaseTemplateID)
	if err := os.RemoveAll(baseDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox/docker: remove @base: %w", err)
	}
	return db.SeedBaseLayerFromImage(ctx)
}

// ensureParentLayers seeds @base from the sandbox image when a parent chain
// references it but the overlay rootfs is missing (first Docker build after
// an Incus install).
func (db *DockerBackend) ensureParentLayers(ctx context.Context, parents []string) error {
	needsBase := len(parents) == 0
	for _, id := range parents {
		if id == "" || id == sandbox.BaseTemplateID {
			needsBase = true
			break
		}
	}
	if !needsBase {
		return nil
	}
	if db.LayerReady(sandbox.BaseTemplateID) {
		return nil
	}
	if err := db.SeedBaseLayerFromImage(ctx); err != nil {
		return fmt.Errorf("sandbox/docker: seed @base overlay before build: %w", err)
	}
	return nil
}

// BuildTemplate creates a new template layer by provisioning a throwaway
// container, running the build steps, and capturing the upper directory.
func (db *DockerBackend) BuildTemplate(ctx context.Context, spec sandbox.TemplateBuildSpec) (*sandbox.TemplateArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Build a temporary session to run the steps in.
	buildSessionID := "build-" + spec.TemplateID + "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["astonish.io/purpose"] = "template-builder"
	db.pruneDanglingOverlayVolumes(ctx)
	if err := db.ensureParentLayers(ctx, spec.ParentLayers); err != nil {
		return nil, err
	}
	buildSpec := sandbox.SessionSpec{
		SessionID:  buildSessionID,
		Type:       sandbox.SessionTypeChat,
		LayerChain: spec.ParentLayers,
		Labels:     labels,
	}

	sess, err := db.CreateSession(ctx, buildSpec)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: BuildTemplate create session: %w", err)
	}
	defer func() {
		// Best-effort cleanup: remove the throwaway container + upper dir.
		_ = db.DestroySession(context.Background(), buildSessionID)
	}()

	if err := db.WaitForSessionReady(ctx, sess.SessionID); err != nil {
		return nil, fmt.Errorf("sandbox/docker: BuildTemplate wait ready: %w", err)
	}

	report := spec.Progress
	if report == nil {
		report = func(string) {}
	}

	// fuse-overlayfs rejects apt's _apt sandbox user (uid 42) and dpkg
	// postinsts that try to start systemd services (docker.io). Persist
	// apt/dpkg policy in the overlay before the first package step.
	report("Preparing overlay apt/dpkg policy...")
	if err := db.execBuildStep(ctx, sess.SessionID, -1, overlayAptPrepScript()); err != nil {
		return nil, err
	}

	for i, step := range spec.Steps {
		report(fmt.Sprintf("Running step %d/%d: %s", i+1, len(spec.Steps), truncateStep(step)))
		if err := db.execBuildStep(ctx, sess.SessionID, i, step); err != nil {
			return nil, err
		}
	}

	report("Capturing overlay layer...")
	return db.captureUpperAsLayer(ctx, sess.SessionID, spec.TemplateID)
}

func captureLayerError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no space left") {
		return fmt.Errorf("sandbox/docker: capture layer: no space left on device (need room for the overlay snapshot on the layers volume). Free Docker disk: docker volume prune -f && docker system prune, or increase the Docker Desktop disk image: %w", err)
	}
	return fmt.Errorf("sandbox/docker: capture layer: %w", err)
}

func truncateStep(step string) string {
	step = strings.TrimSpace(step)
	if len(step) <= 80 {
		return step
	}
	return step[:77] + "..."
}

func (db *DockerBackend) execBuildStep(ctx context.Context, sessionID string, i int, step string) error {
	result, err := db.Exec(ctx, sessionID, sandbox.ExecSpec{
		Command: []string{"sh", "-c", step},
		Env: map[string]string{
			"DEBIAN_FRONTEND":          "noninteractive",
			"NEEDRESTART_MODE":         "l",
			"APT_LISTCHANGES_FRONTEND": "none",
		},
	})
	if err != nil {
		if i < 0 {
			return fmt.Errorf("sandbox/docker: BuildTemplate apt prep: %w", err)
		}
		return fmt.Errorf("sandbox/docker: BuildTemplate step %d: %w", i, err)
	}
	if result == nil || result.ExitCode == 0 {
		return nil
	}
	if i < 0 {
		return fmt.Errorf("sandbox/docker: BuildTemplate apt prep exited %d: %s",
			result.ExitCode, formatExecOutput(result.Stdout, result.Stderr))
	}
	return fmt.Errorf("sandbox/docker: BuildTemplate step %d exited %d: %s",
		i, result.ExitCode, formatExecOutput(result.Stdout, result.Stderr))
}

// overlayAptPrepScript makes apt/dpkg usable inside a fuse-overlayfs chroot.
// APT::Sandbox::User=root avoids EOPNOTSUPP from the _apt uid-42 sandbox.
// policy-rc.d 101 blocks service starts (docker.io postinst).
func overlayAptPrepScript() string {
	return strings.Join([]string{
		"set -e",
		"avail=$(df -Pm / | awk 'NR==2 { print $4 }')",
		`echo "overlay free space: ${avail}MB"`,
		`if [ -n "$avail" ] && [ "$avail" -lt 2048 ]; then`,
		`  echo "E: overlay has ${avail}MB free; the core package install needs about 2GB. Increase Docker Desktop disk or run: docker system prune" >&2`,
		"  exit 100",
		"fi",
		"mkdir -p /etc/apt/apt.conf.d /etc/dpkg/dpkg.cfg.d /usr/sbin",
		`cat > /etc/apt/apt.conf.d/99astonish-overlay <<'EOF'`,
		`APT::Sandbox::User "root";`,
		`Dpkg::Use-Pty "false";`,
		"EOF",
		"echo force-unsafe-io > /etc/dpkg/dpkg.cfg.d/docker-unsafe-io",
		`printf '%s\n' '#!/bin/sh' 'exit 101' > /usr/sbin/policy-rc.d`,
		"chmod 0755 /usr/sbin/policy-rc.d",
	}, "\n")
}

func formatExecOutput(stdout, stderr []byte) string {
	if errs := extractAptErrorLines(stderr, stdout); errs != "" {
		return errs
	}
	// Prefer stderr: apt writes E: there, and stdout is often a huge package list.
	combined := strings.TrimSpace(string(stderr))
	if combined == "" {
		combined = strings.TrimSpace(string(stdout))
	} else if tail := strings.TrimSpace(string(stdout)); tail != "" {
		combined = combined + "\n" + tail
	}
	if combined == "" {
		return "(no output)"
	}
	const max = 2500
	if len(combined) > max {
		return combined[:max] + "..."
	}
	return combined
}

func extractAptErrorLines(stderr, stdout []byte) string {
	var errs []string
	seen := map[string]bool{}
	for _, src := range [][]byte{stderr, stdout} {
		for _, line := range strings.Split(string(src), "\n") {
			trim := strings.TrimSpace(line)
			if trim == "" || seen[trim] {
				continue
			}
			if strings.HasPrefix(trim, "E:") || strings.HasPrefix(trim, "Err:") ||
				strings.Contains(trim, "dpkg: error") ||
				strings.Contains(trim, "not enough free space") {
				seen[trim] = true
				errs = append(errs, trim)
			}
		}
	}
	if len(errs) == 0 {
		return ""
	}
	return strings.Join(errs, "\n")
}

// SaveSessionAsTemplate captures the upper layer of a running session and
// stores it as a new immutable template layer. This is the "save-as" primitive.
func (db *DockerBackend) SaveSessionAsTemplate(ctx context.Context, sessionID string) (*sandbox.TemplateArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return db.captureUpperAsLayer(ctx, sessionID, sessionID)
}

// RefreshTemplate re-runs a template's build steps. The template registry
// (SandboxTemplateStore) holds the steps; we expect callers to pass a new
// TemplateBuildSpec with the same TemplateID. This just delegates to BuildTemplate.
func (db *DockerBackend) RefreshTemplate(ctx context.Context, templateID string) (*sandbox.TemplateArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// RefreshTemplate is called by higher-level orchestration code that has
	// already resolved the build spec. Without the spec we can only return
	// ErrNotImplementedYet-equivalent. Callers that need a full refresh
	// should call BuildTemplate directly with the updated spec.
	return nil, fmt.Errorf("sandbox/docker: RefreshTemplate: call BuildTemplate with an updated TemplateBuildSpec instead")
}

// DeleteTemplate removes a template's layer directory.
func (db *DockerBackend) DeleteTemplate(ctx context.Context, templateID string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = force
	if templateID == "" || templateID == sandbox.BaseTemplateID || templateID == "base" {
		return fmt.Errorf("sandbox/docker: refusing to delete %q", templateID)
	}
	layerDir := db.layerDir(templateID)
	err := os.RemoveAll(layerDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox/docker: DeleteTemplate %q: %w", templateID, err)
	}
	return nil
}

// AliasLayer creates LayersDir/<name> as a relative symlink to the
// content-addressed layer so CLI template names resolve in LayerChain.
func (db *DockerBackend) AliasLayer(name, layerID string) error {
	if name == "" || name == sandbox.BaseTemplateID || name == "base" {
		return fmt.Errorf("sandbox/docker: cannot alias layer as %q", name)
	}
	if layerID == "" {
		return fmt.Errorf("sandbox/docker: layer id is required")
	}
	if _, err := os.Stat(db.layerDir(layerID)); err != nil {
		return fmt.Errorf("sandbox/docker: layer %q: %w", layerID, err)
	}
	dest := db.layerDir(name)
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("sandbox/docker: replace alias %q: %w", name, err)
	}
	if err := os.Symlink(layerID, dest); err != nil {
		return fmt.Errorf("sandbox/docker: alias %q -> %s: %w", name, layerID, err)
	}
	return nil
}

// ReplaceBaseFromSession flattens a running session's composed overlay
// (/sandbox/rootfs in the image namespace) over layers/@base/rootfs.
func (db *DockerBackend) ReplaceBaseFromSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sessionID == "" {
		return fmt.Errorf("sandbox/docker: session id is required")
	}
	cname := containerName(sessionID)
	rootfs := db.layerRootfs(sandbox.BaseTemplateID)
	tmp := rootfs + ".new"
	if err := os.RemoveAll(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox/docker: clear staged @base: %w", err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("sandbox/docker: mkdir staged @base: %w", err)
	}

	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "exec", cname, "tar", "-C", mountRootfs, "-cf", "-", ".")
	tarCmd := exec.CommandContext(ctx, "tar", "-C", tmp, "-xf", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tarCmd.Stdin = stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sandbox/docker: flatten tar: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("sandbox/docker: flatten extract: %w", err)
	}
	tarErr := tarCmd.Wait()
	exportErr := cmd.Wait()
	if exportErr != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("sandbox/docker: flatten tar: %w", exportErr)
	}
	if tarErr != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("sandbox/docker: flatten extract: %w", tarErr)
	}

	old := rootfs + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(rootfs, old); err != nil && !os.IsNotExist(err) {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("sandbox/docker: park old @base: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(rootfs), 0o755); err != nil {
		_ = os.Rename(old, rootfs)
		return err
	}
	if err := os.Rename(tmp, rootfs); err != nil {
		_ = os.Rename(old, rootfs)
		return fmt.Errorf("sandbox/docker: install new @base: %w", err)
	}
	_ = os.RemoveAll(old)
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// captureUpperAsLayer runs the k8s-equivalent tar pipeline inside the
// container (image namespace, not chroot) so the layer lands at
// LayersDir/<sha>/rootfs via the bind-mounted layers volume.
func (db *DockerBackend) captureUpperAsLayer(ctx context.Context, sessionID, templateID string) (*sandbox.TemplateArtifact, error) {
	_ = templateID
	cname := containerName(sessionID)
	builderID := fmt.Sprintf("%d", time.Now().UnixNano())
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"exec", cname, "/bin/sh", "-c", buildCaptureScript(builderID))
	if err != nil {
		return nil, captureLayerError(err)
	}
	sha, size, err := parseCaptureOutput(out)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: capture layer output: %w", err)
	}
	return &sandbox.TemplateArtifact{
		LayerID:    sha,
		SizeBytes:  size,
		CephFSPath: db.layerDir(sha),
		CreatedAt:  time.Now().UTC(),
	}, nil
}

func buildCaptureScript(builderID string) string {
	return sandbox.OverlayCaptureScript(mountLayers, mountUpper, builderID)
}

func parseCaptureOutput(stdout []byte) (sha string, size int64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "SHA="):
			sha = strings.TrimPrefix(line, "SHA=")
		case strings.HasPrefix(line, "SIZE="):
			n, perr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "SIZE=")), 10, 64)
			if perr != nil {
				return "", 0, fmt.Errorf("SIZE= is not an integer: %w", perr)
			}
			size = n
		}
	}
	if sha == "" {
		return "", 0, fmt.Errorf("SHA= line missing")
	}
	if len(sha) != 64 {
		return "", 0, fmt.Errorf("SHA= value %q is not 64-char hex", sha)
	}
	return sha, size, nil
}
