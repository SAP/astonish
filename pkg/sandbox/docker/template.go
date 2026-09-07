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

	// Run each build step.
	for i, step := range spec.Steps {
		result, err := db.Exec(ctx, sess.SessionID, sandbox.ExecSpec{
			Command: []string{"sh", "-c", step},
		})
		if err != nil {
			return nil, fmt.Errorf("sandbox/docker: BuildTemplate step %d: %w", i, err)
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("sandbox/docker: BuildTemplate step %d exited %d: %s",
				i, result.ExitCode, string(result.Stderr))
		}
	}

	// Capture the upper directory as a new template layer.
	return db.captureUpperAsLayer(ctx, buildSessionID, spec.TemplateID)
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
	layerDir := db.layerDir(templateID)
	err := os.RemoveAll(layerDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox/docker: DeleteTemplate %q: %w", templateID, err)
	}
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
		return nil, fmt.Errorf("sandbox/docker: capture layer: %w", err)
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
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "STAGING=%q\n", mountLayers+"/__staging-"+builderID)
	fmt.Fprintf(&b, "LAYERS_DIR=%q\n", mountLayers)
	b.WriteString("trap 'rm -rf \"$STAGING\"' EXIT\n")
	b.WriteString("mkdir -p \"$STAGING/rootfs\"\n")
	b.WriteString("tar --numeric-owner --xattrs --acls --sort=name --mtime=@0 \\\n")
	fmt.Fprintf(&b, "    -C %s -cf /tmp/astn-layer.tar .\n", mountUpper)
	b.WriteString("SHA=$(sha256sum /tmp/astn-layer.tar | awk '{print $1}')\n")
	b.WriteString("tar --numeric-owner --xattrs --acls -C \"$STAGING/rootfs\" -xf /tmp/astn-layer.tar\n")
	b.WriteString("rm -f /tmp/astn-layer.tar\n")
	b.WriteString("if [ -d \"$LAYERS_DIR/$SHA\" ]; then\n")
	b.WriteString("  rm -rf \"$STAGING\"\n")
	b.WriteString("else\n")
	b.WriteString("  mv \"$STAGING\" \"$LAYERS_DIR/$SHA\"\n")
	b.WriteString("fi\n")
	b.WriteString("SIZE=$(du -sb \"$LAYERS_DIR/$SHA/rootfs\" | awk '{print $1}')\n")
	b.WriteString("echo \"SHA=$SHA\"\n")
	b.WriteString("echo \"SIZE=$SIZE\"\n")
	return b.String()
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
