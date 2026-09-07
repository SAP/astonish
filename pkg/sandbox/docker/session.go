// session.go — Docker session lifecycle methods for DockerBackend.
//
// Each session maps to a single Docker container named
// "astonish-session-<short12>". The container runs the sandbox-base image
// whose entrypoint (astonish-sandbox-entrypoint) composes the OverlayFS
// rootfs from the template layers and the session's upper directory, then
// chroots into it and starts the astonish node daemon.
//
// Overlay volume layout (host → container):
//
//	<LayersDir>/<layerID>/      → /mnt/astonish-layers/<layerID>/:ro
//	<UppersDir>/<sessionID>/    → /mnt/astonish-uppers/<sessionID>/:rw
//
// The sandbox-base entrypoint reads ASTONISH_LAYER_CHAIN (comma-separated)
// and ASTONISH_SESSION_ID to compose the overlay at start.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// CreateSession materialises a new sandbox container. Idempotent on SessionID:
// if the container already exists the call is a no-op and the existing session
// is returned.
func (db *DockerBackend) CreateSession(ctx context.Context, spec sandbox.SessionSpec) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Normalise template ID.
	templateID := spec.TemplateID
	if templateID == "" {
		templateID = sandbox.BaseTemplateID
	}

	cname := containerName(spec.SessionID)

	// Idempotency: if the container already exists, return current state.
	state, err := db.containerState(ctx, cname)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: CreateSession inspect: %w", err)
	}
	if state != sandbox.SessionStateGone {
		// Re-record after Studio restart so browser resolve can find the
		// already-running container (registry is file-backed but older
		// rows may lack PodName).
		db.recordSession(spec, cname, templateID)
		return &sandbox.Session{
			SessionID:  spec.SessionID,
			Type:       spec.Type,
			TemplateID: templateID,
			OrgSlug:    spec.OrgSlug,
			TeamSlug:   spec.TeamSlug,
			State:      state,
			BackendRef: cname,
			Labels:     spec.Labels,
			CreatedAt:  time.Now().UTC(),
		}, nil
	}

	layerChain, err := db.resolveLayerChain(spec.LayerChain, templateID)
	if err != nil {
		return nil, err
	}

	upperDir := db.upperPath(spec.SessionID)
	if err := os.MkdirAll(upperDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox/docker: create persist dir %q: %w", upperDir, err)
	}
	if err := os.WriteFile(filepath.Join(upperDir, layerChainFile), []byte(layerChain+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("sandbox/docker: persist layer chain: %w", err)
	}

	if spec.OrgSlug != "" {
		if err := db.EnsureOrgNetwork(ctx, spec.OrgSlug); err != nil {
			return nil, err
		}
	}

	db.removeOverlayVolume(ctx, spec.SessionID)
	args := db.dockerRunArgs(spec, cname, layerChain, upperDir)
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, args...); err != nil {
		return nil, fmt.Errorf("sandbox/docker: docker run for session %s: %w", spec.SessionID, err)
	}

	sess := &sandbox.Session{
		SessionID:  spec.SessionID,
		Type:       spec.Type,
		TemplateID: templateID,
		OrgSlug:    spec.OrgSlug,
		TeamSlug:   spec.TeamSlug,
		State:      sandbox.SessionStateCreating,
		BackendRef: cname,
		Labels:     spec.Labels,
		CreatedAt:  time.Now().UTC(),
	}
	db.recordSession(spec, cname, templateID)
	return sess, nil
}

// StartSession resumes a stopped session. If the container was removed on
// Stop (k8s-style evict), it is recreated from the persisted upper tarball
// and layer-chain file. Idempotent if already running.
func (db *DockerBackend) StartSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cname := containerName(sessionID)
	state, err := db.containerState(ctx, cname)
	if err != nil {
		return fmt.Errorf("sandbox/docker: StartSession inspect %s: %w", sessionID, err)
	}
	switch state {
	case sandbox.SessionStateRunning, sandbox.SessionStateCreating:
		return nil
	case sandbox.SessionStateGone:
		return db.recreateFromPersist(ctx, sessionID)
	}
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "start", cname); err != nil {
		return fmt.Errorf("sandbox/docker: docker start %s: %w", cname, err)
	}
	return nil
}

// StopSession persists the overlay upper layer, then removes the container
// and its overlay volume. The persist dir keeps upper.tar.zst for resume.
// Idempotent if already gone.
func (db *DockerBackend) StopSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cname := containerName(sessionID)
	state, err := db.containerState(ctx, cname)
	if err != nil {
		return fmt.Errorf("sandbox/docker: StopSession inspect %s: %w", sessionID, err)
	}
	if state == sandbox.SessionStateGone {
		return nil
	}
	if state == sandbox.SessionStateRunning || state == sandbox.SessionStateCreating {
		_ = db.persistUpper(ctx, sessionID)
	}
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", "-f", "-v", cname); err != nil && !isDockerNotFoundError(err) {
		return fmt.Errorf("sandbox/docker: docker rm %s: %w", cname, err)
	}
	db.removeOverlayVolume(ctx, sessionID)
	return nil
}

// DestroySession permanently removes the container and its upper layer.
// Idempotent: no error if the session never existed.
func (db *DockerBackend) DestroySession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cname := containerName(sessionID)

	// Remove container (force-stops first if running) and the overlay volume.
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", "-f", "-v", cname)
	if err != nil {
		// "No such container" is not an error for idempotency.
		if !strings.Contains(err.Error(), "No such container") &&
			!strings.Contains(err.Error(), "no such container") {
			return fmt.Errorf("sandbox/docker: docker rm %s: %w", cname, err)
		}
	}
	db.removeOverlayVolume(ctx, sessionID)

	// Remove upper layer directory.
	upperDir := db.upperPath(sessionID)
	if err := os.RemoveAll(upperDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox/docker: remove upper dir %q: %w", upperDir, err)
	}
	if db.cfg.Sessions != nil {
		_ = db.cfg.Sessions.Remove(sessionID)
	}
	return nil
}

// SessionState returns the current observed state of the session.
func (db *DockerBackend) SessionState(ctx context.Context, sessionID string) (sandbox.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.SessionStateGone, err
	}
	cname := containerName(sessionID)
	return db.containerState(ctx, cname)
}

// WaitForSessionReady blocks until the container is running and the
// astonish node daemon inside is ready to accept exec commands.
// For Docker, containers start very quickly (typically <2s), so we poll
// the container status with a short interval.
func (db *DockerBackend) WaitForSessionReady(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cname := containerName(sessionID)
	const pollInterval = 500 * time.Millisecond
	const maxWait = 120 * time.Second

	deadline := time.Now().Add(maxWait)
	for {
		state, err := db.containerState(ctx, cname)
		if err != nil {
			return fmt.Errorf("sandbox/docker: WaitForSessionReady inspect %s: %w", sessionID, err)
		}
		if state == sandbox.SessionStateRunning {
			if db.overlayReady(ctx, sessionID) {
				return nil
			}
		}
		if state == sandbox.SessionStateGone {
			return fmt.Errorf("sandbox/docker: session %s terminated unexpectedly", sessionID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox/docker: session %s not ready after %s (state: %s)", sessionID, maxWait, state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// ListSessions returns all Docker containers matching the astonish.session_id
// label, filtered by the provided SessionFilter.
func (db *DockerBackend) ListSessions(ctx context.Context, filter sandbox.SessionFilter) ([]*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// docker ps --all --filter label=astonish.session_id --format json
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"ps", "--all",
		"--filter", "label=astonish.session_id",
		"--format", "{{json .}}",
	)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: ListSessions docker ps: %w", err)
	}

	var sessions []*sandbox.Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var info dockerPSEntry
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			continue // skip malformed output
		}
		sess := info.toSession()
		if sess == nil {
			continue
		}
		// Apply filters.
		if filter.Type != "" && sess.Type != filter.Type {
			continue
		}
		if filter.OrgSlug != "" && sess.OrgSlug != filter.OrgSlug {
			continue
		}
		if filter.TeamSlug != "" && sess.TeamSlug != filter.TeamSlug {
			continue
		}
		if filter.State != "" && sess.State != filter.State {
			continue
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// dockerPSEntry is the JSON shape of `docker ps --format "{{json .}}"`.
// Only the fields we use are decoded; Docker may emit additional fields.
type dockerPSEntry struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	Status  string `json:"Status"`
	State   string `json:"State"`
	Labels  string `json:"Labels"` // comma-separated key=value pairs
	Created string `json:"Created"`
}

func (e *dockerPSEntry) toSession() *sandbox.Session {
	labels := parseDockerLabels(e.Labels)
	sessionID := labels["astonish.session_id"]
	if sessionID == "" {
		return nil
	}
	st := dockerStateToSessionState(e.State)
	return &sandbox.Session{
		SessionID:  sessionID,
		Type:       sandbox.SessionType(labels["astonish.type"]),
		OrgSlug:    labels["astonish.org"],
		TeamSlug:   labels["astonish.team"],
		State:      st,
		BackendRef: e.Names,
		Labels:     labels,
	}
}

// containerState inspects a container by name and returns its SessionState.
// Returns SessionStateGone if the container does not exist.
func (db *DockerBackend) containerState(ctx context.Context, cname string) (sandbox.SessionState, error) {
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"inspect", "--format", "{{.State.Status}}", cname,
	)
	if err != nil {
		// Docker returns exit code 1 if the container doesn't exist.
		if isDockerNotFoundError(err) {
			return sandbox.SessionStateGone, nil
		}
		return sandbox.SessionStateGone, fmt.Errorf("docker inspect %s: %w", cname, err)
	}
	return dockerStateToSessionState(strings.TrimSpace(string(out))), nil
}

// dockerStateToSessionState maps Docker container status strings to SessionState.
func dockerStateToSessionState(dockerState string) sandbox.SessionState {
	switch strings.ToLower(strings.TrimSpace(dockerState)) {
	case "running":
		return sandbox.SessionStateRunning
	case "created", "restarting":
		return sandbox.SessionStateCreating
	case "paused":
		return sandbox.SessionStateStopped
	case "exited", "dead":
		return sandbox.SessionStateStopped
	case "removing":
		return sandbox.SessionStateEvicting
	default:
		return sandbox.SessionStateGone
	}
}

// parseDockerLabels parses a comma-separated "key=value,key=value" label string
// as returned by `docker ps --format "{{json .}}"`.
func parseDockerLabels(raw string) map[string]string {
	labels := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.IndexByte(part, '=')
		if idx < 0 {
			labels[part] = ""
		} else {
			labels[part[:idx]] = part[idx+1:]
		}
	}
	return labels
}

// buildLayerChain produces the ASTONISH_LAYER_CHAIN value for the container
// entrypoint. Uses the pre-resolved chain if non-empty, otherwise falls back
// to the template ID as a single-element chain.
func buildLayerChain(resolved []string, templateID string) string {
	if len(resolved) > 0 {
		return strings.Join(resolved, ",")
	}
	if templateID != "" && templateID != sandbox.BaseTemplateID {
		return templateID
	}
	return ""
}

func (db *DockerBackend) resolveLayerChain(resolved []string, templateID string) (string, error) {
	chain := buildLayerChain(resolved, templateID)
	if chain == "" {
		if _, err := os.Stat(db.layerRootfs(sandbox.BaseTemplateID)); err == nil {
			return sandbox.BaseTemplateID, nil
		}
		return "", fmt.Errorf("sandbox/docker: no overlay layer chain (build @base first)")
	}
	for _, id := range strings.Split(chain, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := os.Stat(db.layerRootfs(id)); err != nil {
			return "", fmt.Errorf("sandbox/docker: layer %q rootfs missing at %s: %w", id, db.layerRootfs(id), err)
		}
	}
	return chain, nil
}

func layersMountOpt(spec sandbox.SessionSpec) string {
	if spec.Labels != nil && spec.Labels["astonish.io/purpose"] == "template-builder" {
		return ""
	}
	return ":ro"
}

func (db *DockerBackend) dockerRunArgs(spec sandbox.SessionSpec, cname, layerChain, upperDir string) []string {
	args := []string{
		"run", "-d",
		"--name", cname,
		"--privileged",
		"--device", "/dev/fuse",
		"-v", db.cfg.LayersDir + ":" + mountLayers + layersMountOpt(spec),
		"-v", upperDir + ":" + mountUppers,
		"-v", overlayVolumeName(spec.SessionID) + ":" + mountOverlay,
		"-e", envSessionID + "=" + spec.SessionID,
		"-e", envLayerChain + "=" + layerChain,
		"-e", envUpperDir + "=" + mountUpper,
		"-e", envWorkDir + "=" + mountWork,
		"-e", envLayersDir + "=" + mountLayers,
		"-e", envUppersDir + "=" + mountUppers,
		"-e", envHandoff + "=/bin/sleep",
		"-e", envHandoffArg + "=infinity",
	}
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range db.cfg.Labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args,
		"--label", "astonish.session_id="+spec.SessionID,
		"--label", "astonish.type="+string(spec.Type),
		"--label", "astonish.org="+spec.OrgSlug,
		"--label", "astonish.team="+spec.TeamSlug,
	)
	for k, v := range spec.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if spec.OrgSlug != "" {
		args = append(args, "--network", orgNetworkName(spec.OrgSlug))
	}
	if spec.Limits.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", spec.Limits.CPUs))
	}
	if spec.Limits.MemoryMiB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.Limits.MemoryMiB))
	}
	if spec.Limits.PIDs > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", spec.Limits.PIDs))
	}
	args = append(args, db.cfg.SandboxImage, "sleep", "infinity")
	return args
}

func (db *DockerBackend) overlayReady(ctx context.Context, sessionID string) bool {
	res, err := db.Exec(ctx, sessionID, sandbox.ExecSpec{
		Command: []string{"/bin/sh", "-c", "test -x /bin/sh"},
	})
	return err == nil && res != nil && res.ExitCode == 0
}

func persistUpperScript() string {
	return strings.Join([]string{
		"set -eu",
		"SESSION_DIR=" + quoteShell(mountUppers),
		`TMP="$SESSION_DIR/upper.tar.zst.tmp"`,
		`FINAL="$SESSION_DIR/upper.tar.zst"`,
		`mkdir -p "$SESSION_DIR"`,
		`rm -f "$TMP"`,
		"tar --numeric-owner --xattrs --acls -I 'zstd --adapt -T0' \\",
		"  -C " + quoteShell(mountUpper) + ` -cf "$TMP" .`,
		`test -s "$TMP"`,
		`mv "$TMP" "$FINAL"`,
	}, "\n")
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (db *DockerBackend) persistUpper(ctx context.Context, sessionID string) error {
	cname := containerName(sessionID)
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"exec", cname, "/bin/sh", "-c", persistUpperScript())
	return err
}

func (db *DockerBackend) removeOverlayVolume(ctx context.Context, sessionID string) {
	vol := overlayVolumeName(sessionID)
	_, _ = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", "-f", vol)
}

// pruneDanglingOverlayVolumes deletes unused overlay volumes left behind by
// `docker rm -f` without `-v` (anonymous 64-hex names) and our named
// `*-overlay` volumes whose container is already gone.
func (db *DockerBackend) pruneDanglingOverlayVolumes(ctx context.Context) {
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "ls", "-qf", "dangling=true")
	if err != nil {
		return
	}
	for _, name := range strings.Split(string(out), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !isPrunableOverlayVolume(name) {
			continue
		}
		_, _ = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", "-f", name)
	}
}

func isPrunableOverlayVolume(name string) bool {
	if strings.HasSuffix(name, "-overlay") && strings.HasPrefix(name, "astonish-session-") {
		return true
	}
	if len(name) != 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (db *DockerBackend) recordSession(spec sandbox.SessionSpec, cname, templateID string) {
	if db.cfg.Sessions == nil {
		return
	}
	_ = db.cfg.Sessions.PutSession(&store.SandboxSession{
		SessionID:     spec.SessionID,
		ChatSessionID: spec.SessionID,
		Backend:       string(sandbox.BackendKindDocker),
		ContainerName: cname,
		// Browser/PDF resolvers historically required PodName (K8s field).
		// Docker has no pod; reuse the container name so those lookups work.
		PodName:      cname,
		TemplateID:   templateID,
		State:        store.SandboxSessionStateRunning,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		LastActiveAt: time.Now().UTC(),
	})
}

func (db *DockerBackend) recreateFromPersist(ctx context.Context, sessionID string) error {
	upperDir := db.upperPath(sessionID)
	raw, err := os.ReadFile(filepath.Join(upperDir, layerChainFile))
	if err != nil {
		return fmt.Errorf("sandbox/docker: session %s has no persist dir; call CreateSession first", sessionID)
	}
	chain := strings.TrimSpace(string(raw))
	if chain == "" {
		return fmt.Errorf("sandbox/docker: session %s persist is missing layer chain", sessionID)
	}
	spec := sandbox.SessionSpec{SessionID: sessionID, Type: sandbox.SessionTypeChat, LayerChain: strings.Split(chain, ",")}
	db.removeOverlayVolume(ctx, sessionID)
	cname := containerName(sessionID)
	args := db.dockerRunArgs(spec, cname, chain, upperDir)
	if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, args...); err != nil {
		return fmt.Errorf("sandbox/docker: recreate session %s: %w", sessionID, err)
	}
	templateID := spec.TemplateID
	if templateID == "" {
		templateID = sandbox.BaseTemplateID
	}
	db.recordSession(spec, cname, templateID)
	return nil
}
