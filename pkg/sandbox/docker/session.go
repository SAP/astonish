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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// CreateSession keeps the named volume authoritative across container loss.
func (db *DockerBackend) CreateSession(ctx context.Context, spec sandbox.SessionSpec) (*sandbox.Session, error) {
	unlock, err := db.lockSession(ctx, spec.SessionID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return db.createSessionLocked(ctx, spec)
}

func (db *DockerBackend) createSessionLocked(ctx context.Context, spec sandbox.SessionSpec) (*sandbox.Session, error) {
	cname := containerName(spec.SessionID)
	state, err := db.settledContainerState(ctx, cname)
	if err != nil {
		return nil, err
	}
	// Read durable configuration before resolving a potentially newer template.
	meta, err := db.readSessionMetadata(spec.SessionID)
	if err != nil {
		return nil, err
	}
	if meta == nil && state != sandbox.SessionStateGone {
		meta, err = db.adoptContainerMetadata(ctx, spec, cname)
		if err != nil {
			return nil, err
		}
	}
	if meta != nil {
		spec, err = db.recoverySpec(meta, spec, state == sandbox.SessionStateGone)
		if err != nil {
			return nil, err
		}
	} else {
		if raw, readErr := os.ReadFile(filepath.Join(db.upperPath(spec.SessionID), layerChainFile)); readErr == nil {
			if spec.Type == "" {
				return nil, fmt.Errorf("sandbox/docker: legacy recovery requires current authorized launch configuration; storage retained")
			}
			chain := strings.TrimSpace(string(raw))
			if chain == "" {
				return nil, fmt.Errorf("sandbox/docker: empty persisted layer chain")
			}
			spec.LayerChain = strings.Split(chain, ",")
			if db.cfg.Sessions != nil {
				rec, e := db.cfg.Sessions.GetSession(spec.SessionID)
				if e != nil {
					return nil, e
				}
				if rec != nil {
					if spec.UserID != "" && rec.CreatedBy != "" && spec.UserID != rec.CreatedBy {
						return nil, fmt.Errorf("sandbox/docker: legacy recovery owner mismatch; storage retained")
					}
					if rec.TemplateID != "" {
						spec.TemplateID = rec.TemplateID
					}
					if rec.CreatedBy != "" {
						spec.UserID = rec.CreatedBy
					}
					if rec.Image != "" {
						spec.Image = rec.Image
					}
				}
			}
			if spec.TemplateID == "" {
				return nil, fmt.Errorf("sandbox/docker: legacy recovery requires authorized template configuration; storage retained")
			}
		} else if !os.IsNotExist(readErr) {
			return nil, readErr
		}
		if spec.TemplateID == "" {
			spec.TemplateID = sandbox.BaseTemplateID
		}
		chain, e := db.resolveLayerChain(spec.LayerChain, spec.TemplateID)
		if e != nil {
			return nil, e
		}
		spec.LayerChain = strings.Split(chain, ",")
		if spec.Image == "" {
			spec.Image = db.cfg.SandboxImage
		}
		meta = newDockerSessionMetadata(spec)
	}
	if spec.Env != nil {
		// Refresh authorized credentials in memory, but an idempotent request does
		// not change the existing container's launch environment or required keys.
		sessionLaunchEnvs.Store(sessionEnvKey{db, spec.SessionID}, cloneEnv(spec.Env))
	}
	if state == sandbox.SessionStateGone {
		// Only launch configuration changes durable requirements. Write before
		// Docker runs so a crash after launch cannot omit a required credential.
		// On an ambiguous launch failure retain this fail-closed recovery recipe.
		meta.EnvKeys = newDockerSessionMetadata(spec).EnvKeys
	}
	if err := db.writeSessionMetadata(meta); err != nil {
		return nil, err
	}
	chain := strings.Join(spec.LayerChain, ",")
	if err := os.WriteFile(filepath.Join(db.upperPath(spec.SessionID), layerChainFile), []byte(chain+"\n"), 0600); err != nil {
		return nil, err
	}
	if state == sandbox.SessionStateGone {
		if _, err := db.resolveLayerChain(spec.LayerChain, spec.TemplateID); err != nil {
			return nil, err
		}
		if spec.OrgSlug != "" {
			if err := db.EnsureOrgNetwork(ctx, spec.OrgSlug); err != nil {
				return nil, err
			}
		}
		if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath, db.dockerRunArgs(spec, cname, chain, db.upperPath(spec.SessionID))...); err != nil {
			return nil, fmt.Errorf("sandbox/docker: create session %s: %w", spec.SessionID, err)
		}
		state = sandbox.SessionStateCreating
	}
	if err := db.recordSession(spec, cname, spec.TemplateID); err != nil {
		return nil, err
	}
	return &sandbox.Session{SessionID: spec.SessionID, Type: spec.Type, TemplateID: spec.TemplateID, OrgSlug: spec.OrgSlug, TeamSlug: spec.TeamSlug, State: state, BackendRef: cname, Labels: spec.Labels, CreatedAt: time.Now().UTC()}, nil
}

// StartSession resumes the existing container, including a paused container.
func (db *DockerBackend) StartSession(ctx context.Context, sessionID string) error {
	unlock, err := db.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := db.requireRegisteredSession(sessionID); err != nil {
		return err
	}
	cname := containerName(sessionID)
	state, err := db.settledContainerState(ctx, cname)
	if err != nil {
		return err
	}
	switch state {
	case sandbox.SessionStateRunning, sandbox.SessionStateCreating:
		return nil
	case sandbox.SessionStateGone:
		return db.recreateFromPersistLocked(ctx, sessionID)
	}
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{.State.Status}}", cname)
	if err != nil {
		return err
	}
	verb := "start"
	if strings.TrimSpace(string(raw)) == "paused" {
		verb = "unpause"
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, verb, cname)
	return err
}

// StopSession archives ready live state, then stops without removing storage.
func (db *DockerBackend) StopSession(ctx context.Context, sessionID string) error {
	unlock, err := db.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	cname := containerName(sessionID)
	state, err := db.settledContainerState(ctx, cname)
	if err != nil {
		return err
	}
	if state == sandbox.SessionStateGone || state == sandbox.SessionStateStopped {
		return nil
	}
	// Docker reports running before PID 1 has finished composing the overlay.
	// Readiness is required even in that window, not just while restarting.
	if err := db.WaitForSessionReady(ctx, sessionID); err != nil {
		return err
	}
	if err := db.persistUpper(ctx, sessionID); err != nil {
		return fmt.Errorf("sandbox/docker: persist upper for session %s: %w", sessionID, err)
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "stop", cname)
	return err
}

// DestroySession is permanent. Keep recovery/registry authority on failure.
func (db *DockerBackend) DestroySession(ctx context.Context, sessionID string) error {
	unlock, err := db.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := db.requireRegisteredSession(sessionID); err != nil {
		return err
	}
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", "-f", "-v", containerName(sessionID))
	if err != nil && !isDockerNotFoundError(err) {
		return err
	}
	if err := db.removeOverlayVolume(ctx, sessionID); err != nil {
		return err
	}
	if err := os.RemoveAll(db.upperPath(sessionID)); err != nil {
		return err
	}
	if db.cfg.Sessions != nil {
		if err := db.cfg.Sessions.Remove(sessionID); err != nil {
			return err
		}
	}
	sessionLaunchEnvs.Delete(sessionEnvKey{db, sessionID})
	return nil
}

// SessionState returns the current observed state of the session.
func (db *DockerBackend) SessionState(ctx context.Context, sessionID string) (sandbox.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.SessionStateGone, err
	}
	if err := db.requireRegisteredSession(sessionID); err != nil {
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
		CreatedAt:  parseDockerCreated(e.Created),
	}
}

// parseDockerCreated parses docker ps Created. A missing or unparseable value
// is treated as now so the orphan reaper's one-hour guard does not delete a
// container whose age is unknown.
func parseDockerCreated(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05 +0000 UTC"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Now().UTC()
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
		"container", "run", "--detach",
		eqFlag("--name", cname),
		"--privileged",
		eqFlag("--device", "/dev/fuse"),
		eqFlag("--volume", db.cfg.LayersDir+":"+mountLayers+layersMountOpt(spec)),
		eqFlag("--mount", "type=bind,source="+upperDir+",target="+mountUppers),
		eqFlag("--volume", overlayVolumeName(spec.SessionID)+":"+mountOverlay),
		eqFlag("--env", envSessionID+"="+spec.SessionID),
		eqFlag("--env", envLayerChain+"="+layerChain),
		eqFlag("--env", envUpperDir+"="+mountUpper),
		eqFlag("--env", envWorkDir+"="+mountWork),
		eqFlag("--env", envLayersDir+"="+mountLayers),
		eqFlag("--env", envUppersDir+"="+mountUppers),
		eqFlag("--env", envHandoff+"=/bin/sleep"),
		eqFlag("--env", envHandoffArg+"=infinity"),
	}
	for k, v := range spec.Env {
		args = append(args, eqFlag("--env", k+"="+v))
	}
	for k, v := range db.cfg.Labels {
		args = append(args, eqFlag("--label", k+"="+v))
	}
	args = append(args,
		eqFlag("--label", "astonish.session_id="+spec.SessionID),
		eqFlag("--label", "astonish.type="+string(spec.Type)),
		eqFlag("--label", "astonish.org="+spec.OrgSlug),
		eqFlag("--label", "astonish.team="+spec.TeamSlug),
	)
	for k, v := range spec.Labels {
		args = append(args, eqFlag("--label", k+"="+v))
	}
	if spec.OrgSlug != "" {
		args = append(args, eqFlag("--network", orgNetworkName(spec.OrgSlug)))
	}
	if spec.Limits.CPUs > 0 {
		args = append(args, eqFlag("--cpus", fmt.Sprintf("%d", spec.Limits.CPUs)))
	}
	if spec.Limits.MemoryMiB > 0 {
		args = append(args, eqFlag("--memory", fmt.Sprintf("%dm", spec.Limits.MemoryMiB)))
	}
	if spec.Limits.PIDs > 0 {
		args = append(args, eqFlag("--pids-limit", fmt.Sprintf("%d", spec.Limits.PIDs)))
	}
	image := spec.Image
	if image == "" {
		image = db.cfg.SandboxImage
	}
	args = append(args, image, "sleep", "infinity")
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

func (db *DockerBackend) removeOverlayVolume(ctx context.Context, sessionID string) error {
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "rm", "-f", overlayVolumeName(sessionID))
	if err != nil && !isDockerNotFoundError(err) {
		return fmt.Errorf("sandbox/docker: remove overlay volume: %w", err)
	}
	return nil
}

func (db *DockerBackend) recordSession(spec sandbox.SessionSpec, cname, templateID string) error {
	if db.cfg.Sessions == nil {
		return nil
	}
	rec, err := db.cfg.Sessions.GetSession(spec.SessionID)
	if err != nil {
		return err
	}
	if rec == nil {
		rec = &store.SandboxSession{SessionID: spec.SessionID, ChatSessionID: spec.SessionID, CreatedAt: time.Now().UTC()}
	} else {
		copy := *rec
		rec = &copy
	}
	rec.Backend = string(sandbox.BackendKindDocker)
	rec.ContainerName = cname
	rec.PodName = cname
	if rec.TemplateID == "" {
		rec.TemplateID = templateID
	}
	if rec.CreatedBy == "" {
		rec.CreatedBy = spec.UserID
	}
	if rec.Image == "" {
		rec.Image = spec.Image
	}
	rec.State = store.SandboxSessionStateRunning
	rec.UpdatedAt = time.Now().UTC()
	rec.LastActiveAt = rec.UpdatedAt
	return db.cfg.Sessions.PutSession(rec)
}

func (db *DockerBackend) recreateFromPersist(ctx context.Context, sessionID string) error {
	unlock, err := db.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	return db.recreateFromPersistLocked(ctx, sessionID)
}

func (db *DockerBackend) recreateFromPersistLocked(ctx context.Context, sessionID string) error {
	if _, err := os.Stat(filepath.Join(db.upperPath(sessionID), layerChainFile)); err != nil {
		return fmt.Errorf("sandbox/docker: session %s has no recovery configuration; call CreateSession first: %w", sessionID, err)
	}
	spec := sandbox.SessionSpec{SessionID: sessionID}
	if env, ok := sessionLaunchEnvs.Load(sessionEnvKey{db, sessionID}); ok {
		spec.Env = cloneEnv(env.(map[string]string))
	}
	_, err := db.createSessionLocked(ctx, spec)
	return err
}

// Lifecycle locks span backend instances targeting the same CLI/context/name.
// Docker commands inherit current process env, so a construction-time lock-only
// snapshot would not identify the runtime they actually execute. Keep runtime
// selection stable during operations; these locks are process-local, not a
// distributed storage/writer protocol.
type sessionLifecycleLock struct {
	token chan struct{}
	refs  int
}

var sessionLifecycleLocks = struct {
	sync.Mutex
	entries map[string]*sessionLifecycleLock
}{entries: make(map[string]*sessionLifecycleLock)}

type sessionEnvKey struct {
	backend   *DockerBackend
	sessionID string
}

var sessionLaunchEnvs sync.Map // secrets are memory-only, never serialized

func (db *DockerBackend) lockSession(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime := db.cfg.ContainerRuntimePath
	if resolved, err := exec.LookPath(runtime); err == nil {
		runtime = resolved
	}
	if absolute, err := filepath.Abs(runtime); err == nil {
		runtime = absolute
	}
	if resolved, err := filepath.EvalSymlinks(runtime); err == nil {
		runtime = resolved
	}
	key := runtime + "\x00" + os.Getenv("DOCKER_HOST") + "\x00" + os.Getenv("DOCKER_CONTEXT") + "\x00" + containerName(id)
	sessionLifecycleLocks.Lock()
	entry := sessionLifecycleLocks.entries[key]
	if entry == nil {
		entry = &sessionLifecycleLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		sessionLifecycleLocks.entries[key] = entry
	}
	entry.refs++
	sessionLifecycleLocks.Unlock()
	releaseRef := func() {
		sessionLifecycleLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(sessionLifecycleLocks.entries, key)
		}
		sessionLifecycleLocks.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			releaseRef()
			return nil, err
		}
		return func() { entry.token <- struct{}{}; releaseRef() }, nil
	}
}

func (db *DockerBackend) settledContainerState(ctx context.Context, cname string) (sandbox.SessionState, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		state, err := db.containerState(ctx, cname)
		if err != nil || state != sandbox.SessionStateEvicting {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

const dockerSessionMetadataFile = "docker-session.json"
const dockerSessionMetadataVersion = 1

// A private recovery recipe, NOT an authorization record. Env values and
// credentials are deliberately excluded. EnvKeys requires fresh authorized env
// after a controller restart instead of silently launching a degraded session.
type dockerSessionMetadata struct {
	Version    int                    `json:"version"`
	SessionID  string                 `json:"session_id"`
	Type       sandbox.SessionType    `json:"type"`
	UserID     string                 `json:"user_id,omitempty"`
	OrgSlug    string                 `json:"org_slug,omitempty"`
	TeamSlug   string                 `json:"team_slug,omitempty"`
	TemplateID string                 `json:"template_id"`
	LayerChain []string               `json:"layer_chain"`
	Image      string                 `json:"image"`
	Labels     map[string]string      `json:"labels,omitempty"`
	Limits     sandbox.ResourceLimits `json:"limits"`
	EnvKeys    []string               `json:"required_env_keys,omitempty"`
}

func cloneEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	copy := make(map[string]string, len(env))
	for k, v := range env {
		copy[k] = v
	}
	return copy
}
func newDockerSessionMetadata(spec sandbox.SessionSpec) *dockerSessionMetadata {
	m := &dockerSessionMetadata{Version: dockerSessionMetadataVersion, SessionID: spec.SessionID, Type: spec.Type, UserID: spec.UserID, OrgSlug: spec.OrgSlug, TeamSlug: spec.TeamSlug, TemplateID: spec.TemplateID, LayerChain: append([]string(nil), spec.LayerChain...), Image: spec.Image, Labels: cloneEnv(spec.Labels), Limits: spec.Limits}
	for key := range spec.Env {
		m.EnvKeys = append(m.EnvKeys, key)
	}
	sort.Strings(m.EnvKeys)
	return m
}
func (db *DockerBackend) readSessionMetadata(id string) (*dockerSessionMetadata, error) {
	raw, err := os.ReadFile(filepath.Join(db.upperPath(id), dockerSessionMetadataFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m dockerSessionMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("sandbox/docker: invalid recovery metadata: %w", err)
	}
	if m.Version != dockerSessionMetadataVersion || m.SessionID != id || m.TemplateID == "" || len(m.LayerChain) == 0 || m.Image == "" {
		return nil, fmt.Errorf("sandbox/docker: incompatible recovery metadata for %s; storage retained", id)
	}
	return &m, nil
}
func (db *DockerBackend) writeSessionMetadata(m *dockerSessionMetadata) error {
	dir := db.upperPath(m.SessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".docker-session-*")
	if err != nil {
		return err
	}
	// Remove the staging file on failure; after a successful rename the old
	// temporary name no longer exists, so this leaves the durable sidecar intact.
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(dir, dockerSessionMetadataFile))
}
func (db *DockerBackend) recoverySpec(m *dockerSessionMetadata, current sandbox.SessionSpec, recreating bool) (sandbox.SessionSpec, error) {
	if (current.UserID != "" && m.UserID != "" && current.UserID != m.UserID) || (current.OrgSlug != "" && current.OrgSlug != m.OrgSlug) || (current.TeamSlug != "" && current.TeamSlug != m.TeamSlug) {
		return current, fmt.Errorf("sandbox/docker: recovery identity mismatch; storage retained")
	}
	env := current.Env
	if env == nil {
		if cached, ok := sessionLaunchEnvs.Load(sessionEnvKey{db, current.SessionID}); ok {
			env = cloneEnv(cached.(map[string]string))
		}
	}
	if recreating {
		for _, key := range m.EnvKeys {
			if _, ok := env[key]; !ok {
				return current, fmt.Errorf("sandbox/docker: recovery requires fresh authorized environment for session %s; call CreateSession with current credentials", current.SessionID)
			}
		}
	}
	return sandbox.SessionSpec{SessionID: m.SessionID, Type: m.Type, UserID: m.UserID, OrgSlug: m.OrgSlug, TeamSlug: m.TeamSlug, TemplateID: m.TemplateID, LayerChain: append([]string(nil), m.LayerChain...), Image: m.Image, Labels: cloneEnv(m.Labels), Limits: m.Limits, Env: env}, nil
}

// Adopt structural legacy launch configuration from Docker without serializing
// its environment. Image defaults are excluded when finding required env keys.
func (db *DockerBackend) adoptContainerMetadata(ctx context.Context, current sandbox.SessionSpec, cname string) (*dockerSessionMetadata, error) {
	var info struct {
		Config struct {
			Image  string
			Env    []string
			Labels map[string]string
		}
		HostConfig struct {
			NanoCpus  int64
			Memory    int64
			PidsLimit *int64
		}
	}
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", cname)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("sandbox/docker: cannot safely adopt legacy container configuration: %w", err)
	}
	labels := info.Config.Labels
	if labels["astonish.session_id"] != current.SessionID {
		return nil, fmt.Errorf("sandbox/docker: legacy container identity mismatch")
	}
	spec := sandbox.SessionSpec{SessionID: current.SessionID, Type: sandbox.SessionType(labels["astonish.type"]), OrgSlug: labels["astonish.org"], TeamSlug: labels["astonish.team"], UserID: current.UserID, TemplateID: current.TemplateID, Image: info.Config.Image, Labels: labels}
	if db.cfg.Sessions != nil {
		rec, e := db.cfg.Sessions.GetSession(current.SessionID)
		if e != nil {
			return nil, e
		}
		if rec != nil {
			spec.UserID = rec.CreatedBy
			spec.TemplateID = rec.TemplateID
		}
	}
	vars := map[string]string{}
	for _, item := range info.Config.Env {
		if k, v, ok := strings.Cut(item, "="); ok {
			vars[k] = v
		}
	}
	chain := vars[envLayerChain]
	if chain == "" {
		return nil, fmt.Errorf("sandbox/docker: legacy container lacks layer chain; storage retained")
	}
	spec.LayerChain = strings.Split(chain, ",")
	if spec.TemplateID == "" {
		return nil, fmt.Errorf("sandbox/docker: legacy recovery requires authorized template configuration; storage retained")
	}
	spec.Limits.CPUs = int(info.HostConfig.NanoCpus / 1e9)
	spec.Limits.MemoryMiB = int(info.HostConfig.Memory / (1024 * 1024))
	if info.HostConfig.PidsLimit != nil && *info.HostConfig.PidsLimit > 0 {
		spec.Limits.PIDs = int(*info.HostConfig.PidsLimit)
	}
	var defaults []string
	raw, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "image", "inspect", "--format", "{{json .Config.Env}}", info.Config.Image)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: cannot establish legacy environment requirements: %w", err)
	}
	if err := json.Unmarshal(raw, &defaults); err != nil {
		return nil, err
	}
	for _, item := range defaults {
		if k, v, ok := strings.Cut(item, "="); ok && vars[k] == v {
			delete(vars, k)
		}
	}
	for _, key := range []string{envSessionID, envLayerChain, envUpperDir, envWorkDir, envLayersDir, envUppersDir, envHandoff, envHandoffArg} {
		delete(vars, key)
	}
	spec.Env = vars
	m := newDockerSessionMetadata(spec)
	if _, err := db.recoverySpec(m, current, false); err != nil {
		return nil, err
	}
	return m, nil
}
