// Package k8s — pod lifecycle.
//
// This file implements the sandbox.Backend session-lifecycle methods
// against a kubernetes.Interface client:
//
//   - CreateSession  — build a PodSpec from a SessionSpec, submit it to
//                      the API server, persist metadata to the session
//                      registry. Idempotent on SessionID.
//   - StartSession   — resume a previously-stopped session. In the
//                      current slice this is a no-op when the pod is
//                      running and a not-implemented stub for the
//                      evicted→running transition (which requires the
//                      upper-tar resume codepath; landing separately).
//   - StopSession    — tear down the pod while preserving persisted
//                      metadata. In the current slice this deletes the
//                      pod and leaves the session row in state=evicted;
//                      the tar-stream eviction of the upper layer is a
//                      later slice.
//   - DestroySession — delete pod + registry row. Idempotent; absent
//                      sessions succeed without error.
//   - SessionState   — map the pod's phase/conditions to sandbox.SessionState.
//                      Returns SessionStateGone for absent pods.
//   - ListSessions   — enumerate pods by label selector and materialise
//                      sandbox.Session views.
//
// The eviction/resume tar-stream plumbing (§5.5), the in-container
// overlay entrypoint, and real readiness gating are intentionally NOT
// included in this slice. This slice makes the backend API-useful
// against a real (or fake) cluster and provides the contract surface
// subsequent slices build on.
//
// Reference: docs/architecture/sandbox-backends.md §5.3.

package k8s

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// Label and annotation keys used on sandbox pods. These are part of the
// public contract with operators (kubectl get pods -l astonish.io/...).
// See docs/architecture/sandbox-backends.md §5.2 and §5.3.
const (
	labelOrg       = "astonish.io/org"
	labelTeam      = "astonish.io/team"
	labelSessionID = "astonish.io/session-id"
	labelType      = "astonish.io/type"
	labelTemplate  = "astonish.io/template"

	typeSession         = "session"
	typeFleet           = "fleet"
	typeTemplateBuilder = "template-builder"

	// labelPurpose distinguishes special-purpose sessions (e.g., team-template
	// editors) from regular chat/fleet sessions. This label controls
	// volume-mount behaviour: team-template-editor sessions mount the layers
	// PVC read-write so SaveSessionAsTemplate can stage artifacts directly.
	labelPurpose = "astonish.io/purpose"

	// purposeTeamTemplateEditor is the label value for team-template editor
	// sessions. Pods with this purpose get RW access to the layers PVC.
	purposeTeamTemplateEditor = "team-template-editor"

	annotationCreatedBy  = "astonish.io/created-by"
	annotationCreatedAt  = "astonish.io/created-at"
	annotationLayerChain = "astonish.io/layer-chain"
)

// Volume names used inside sandbox pods.
const (
	volumeLayers  = "layers"  // RO CephFS layer store
	volumeUppers  = "uppers"  // RW CephFS upper store (eviction/resume)
	volumeOverlay = "overlay" // node-local emptyDir, hosts both upperdir and workdir
)

// Mount paths inside the sandbox container.
//
// IMPORTANT: upper and work MUST be subdirectories of the SAME mount
// point (mountOverlay). fuse-overlayfs performs renameat(workdir, ...,
// upperdir, ...) during directory copy-up. If upper and work reside on
// separate bind-mounts — even from the same underlying device — the
// kernel returns EXDEV because renameat refuses to cross mount
// boundaries. A single emptyDir guarantees they share a mount.
const (
	mountLayers  = "/mnt/astonish-layers"
	mountUppers  = "/mnt/astonish-uppers"
	mountOverlay = "/var/astonish/overlay"
	mountUpper   = mountOverlay + "/upper"
	mountWork    = mountOverlay + "/work"
)

// containerName is the fixed name of the sandbox container inside a
// session pod. exec.go and files.go target this container via
// PodExecOptions.Container.
const containerName = "sandbox"

var safeSessionPathRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

const sessionLayerChainPrefix = "k8s-layer-chain:"

func upperDirNameForSession(sessionID string) (string, error) {
	if sessionID == "" {
		return "", errors.New("session ID is required")
	}
	if sessionID == "." || sessionID == ".." || strings.Contains(sessionID, "/") || !safeSessionPathRE.MatchString(sessionID) {
		return "", fmt.Errorf("session ID %q is not safe for upper persistence", sessionID)
	}
	return sessionID, nil
}

// podNameForSession returns the deterministic pod name used by the K8s
// backend. Exposed here (rather than inlined) so that session.go,
// exec.go, files.go, and fleet.go agree on the naming scheme.
//
// The scheme is "astn-sess-" + the first 27 chars of the session ID
// (lowercased, DNS-sanitised) to stay within Kubernetes' 253-char
// DNS-1123 limit while remaining trivially reversible for operators
// reading kubectl output. Reference:
// docs/architecture/sandbox-backends.md §5.3 step 2.
func podNameForSession(sessionID string) string {
	const prefix = "astn-sess-"
	const maxIDLen = 27
	clean := toDNSLabel(sessionID)
	if len(clean) > maxIDLen {
		clean = clean[:maxIDLen]
	}
	// Re-trim trailing '-' in case truncation left one.
	for len(clean) > 0 && clean[len(clean)-1] == '-' {
		clean = clean[:len(clean)-1]
	}
	return prefix + clean
}

// toDNSLabel lower-cases the input and replaces any character that is
// not [a-z0-9-] with '-'. Leading/trailing '-' are stripped. The result
// is guaranteed to be a valid DNS-1123 label fragment.
func toDNSLabel(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// PodSpec builder
// ---------------------------------------------------------------------------

// buildPodManifest materialises a SessionSpec into a *corev1.Pod.
// Exposed at package scope to keep it testable without a live client.
//
// Spec reference: docs/architecture/sandbox-backends.md §5.3 step 3.
func (b *K8sBackend) buildPodManifest(spec sandbox.SessionSpec) (*corev1.Pod, error) {
	if spec.SessionID == "" {
		return nil, errors.New("SessionSpec.SessionID is required")
	}
	if _, err := upperDirNameForSession(spec.SessionID); err != nil {
		return nil, err
	}
	if spec.TemplateID == "" {
		// Empty TemplateID means the canonical base layer, populated by
		// the seed Job. Mirrors the Incus pool's "" → @base convention.
		spec.TemplateID = sandbox.BaseTemplateID
	}
	if len(spec.LayerChain) == 0 {
		// Accept a single-layer chain (template itself acts as the
		// only lower) so the skeleton is useful before the layer-
		// store slice lands.
		spec.LayerChain = []string{spec.TemplateID}
	}
	if len(spec.LayerChain) > b.cfg.MaxChainDepth {
		return nil, fmt.Errorf("layer chain depth %d exceeds MaxChainDepth %d",
			len(spec.LayerChain), b.cfg.MaxChainDepth)
	}

	name := podNameForSession(spec.SessionID)

	labels := map[string]string{
		labelType:      typeSession,
		labelSessionID: toDNSLabel(spec.SessionID),
		labelTemplate:  toDNSLabel(spec.TemplateID),
	}
	if spec.OrgSlug != "" {
		labels[labelOrg] = toDNSLabel(spec.OrgSlug)
	}
	if spec.TeamSlug != "" {
		labels[labelTeam] = toDNSLabel(spec.TeamSlug)
	}
	// Caller-supplied labels are merged LAST so tests can pin values.
	for k, v := range spec.Labels {
		labels[k] = v
	}

	annotations := map[string]string{
		annotationCreatedAt:  time.Now().UTC().Format(time.RFC3339),
		annotationLayerChain: strings.Join(spec.LayerChain, ","),
	}
	if spec.UserID != "" {
		annotations[annotationCreatedBy] = spec.UserID
	}

	// RuntimeClassName, SecurityContext, HostUsers, FUSE device
	// resource, and overlay-mode env vars are owned by
	// applyPodSecurityHardening at the bottom of this function so the
	// rules stay aligned across session / fleet / template pods.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   b.cfg.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            containerName,
					Image:           b.cfg.SandboxImage,
					ImagePullPolicy: imagePullPolicy(b.cfg.SandboxImage),
					Env: []corev1.EnvVar{
						{Name: "ASTONISH_SESSION_ID", Value: spec.SessionID},
						{Name: "ASTONISH_LAYER_CHAIN", Value: strings.Join(spec.LayerChain, ",")},
						{Name: "ASTONISH_UPPER_DIR", Value: mountUpper},
						{Name: "ASTONISH_WORK_DIR", Value: mountWork},
						{Name: "ASTONISH_LAYERS_DIR", Value: mountLayers},
						{Name: "ASTONISH_UPPERS_DIR", Value: mountUppers},
						// PID 1 sleeps after overlay composition; tool calls
						// arrive via Backend.Exec ("kubectl exec -- astonish node")
						// with proper stdin/stdout attachment per invocation.
						{Name: "ASTONISH_HANDOFF", Value: "/bin/sleep"},
						{Name: "ASTONISH_HANDOFF_ARGS", Value: "infinity"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: volumeLayers, MountPath: mountLayers, ReadOnly: spec.Labels[labelPurpose] != purposeTeamTemplateEditor},
						{Name: volumeUppers, MountPath: mountUppers, SubPath: spec.SessionID},
						{Name: volumeOverlay, MountPath: mountOverlay},
					},
					Resources: buildResourceRequirements(spec.Limits),
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: volumeLayers,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: b.cfg.LayersPVCName,
							ReadOnly:  spec.Labels[labelPurpose] != purposeTeamTemplateEditor,
						},
					},
				},
				{
					Name: volumeUppers,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: b.cfg.UppersPVCName,
						},
					},
				},
				{Name: volumeOverlay, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}

	b.applyPodSecurityHardening(pod)
	return pod, nil
}

// buildResourceRequirements translates sandbox.ResourceLimits into
// a corev1.ResourceRequirements block with separate Requests (scheduler
// reservation) and Limits (cgroup ceiling).
//
// On K8s, if only Limits are set without Requests, the kubelet defaults
// Requests = Limits → Guaranteed QoS → full reservation. That is never
// what we want for interactive sandbox sessions (which idle 99% of the
// time). So we always set explicit Requests.
//
// Auto-derivation when RequestCPUMillis/RequestMemoryMiB are zero:
//
//	CPU request  = max(50m,  ceil(limit*1000/20))  → 5% of ceiling, min 50m
//	Mem request  = max(128Mi, ceil(limit/8))       → 12.5% of ceiling, min 128Mi
//
// This gives ~20:1 CPU packing ratio and ~8:1 memory packing ratio for
// idle sessions while guaranteeing burst to the full ceiling when load
// spikes.
func buildResourceRequirements(lim sandbox.ResourceLimits) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{}

	// --- Limits (cgroup ceiling, matches Incus semantics) ---
	limits := corev1.ResourceList{}
	if lim.CPUs > 0 {
		limits[corev1.ResourceCPU] = *resource.NewQuantity(int64(lim.CPUs), resource.DecimalSI)
	}
	if lim.MemoryMiB > 0 {
		limits[corev1.ResourceMemory] = *resource.NewQuantity(int64(lim.MemoryMiB)*1024*1024, resource.BinarySI)
	}
	if lim.DiskMiB > 0 {
		limits[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(lim.DiskMiB)*1024*1024, resource.BinarySI)
	}
	if len(limits) > 0 {
		reqs.Limits = limits
	}

	// --- Requests (scheduler reservation, idle floor) ---
	requests := corev1.ResourceList{}

	cpuMillis := lim.RequestCPUMillis
	if cpuMillis == 0 && lim.CPUs > 0 {
		// Auto-derive: 5% of ceiling, minimum 50m.
		cpuMillis = lim.CPUs * 1000 / 20
		if cpuMillis < 50 {
			cpuMillis = 50
		}
	}
	if cpuMillis > 0 {
		requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(cpuMillis), resource.DecimalSI)
	}

	memMiB := lim.RequestMemoryMiB
	if memMiB == 0 && lim.MemoryMiB > 0 {
		// Auto-derive: 12.5% of ceiling, minimum 128Mi.
		memMiB = lim.MemoryMiB / 8
		if memMiB < 128 {
			memMiB = 128
		}
	}
	if memMiB > 0 {
		requests[corev1.ResourceMemory] = *resource.NewQuantity(int64(memMiB)*1024*1024, resource.BinarySI)
	}

	if len(requests) > 0 {
		reqs.Requests = requests
	}

	return reqs
}

// ---------------------------------------------------------------------------
// Session lifecycle (real implementations)
// ---------------------------------------------------------------------------

// CreateSession builds a PodSpec from the supplied SessionSpec, submits
// it via the API server, and records the session in the registry.
// Idempotent on SessionID: a second call with the same ID returns the
// existing session without recreation.
func (b *K8sBackend) CreateSession(ctx context.Context, spec sandbox.SessionSpec) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.client == nil {
		return nil, fmt.Errorf("CreateSession: %w", ErrNotImplementedYet)
	}
	if spec.SessionID == "" {
		return nil, errors.New("CreateSession: SessionSpec.SessionID is required")
	}

	// Idempotency: if the registry row exists and the pod still exists,
	// return it. If the row represents an evicted session, preserve the row
	// and resume instead of deleting the metadata that points to persisted
	// upper data.
	if existing, err := b.sessions.GetSession(spec.SessionID); err == nil && existing != nil {
		podName := existing.PodName
		if podName == "" {
			podName = podNameForSession(spec.SessionID)
		}
		_, getErr := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if getErr == nil {
			return sessionFromStore(existing, spec.Type), nil
		}
		if !apierrors.IsNotFound(getErr) {
			return nil, fmt.Errorf("CreateSession: verify pod: %w", getErr)
		}
		if existing.State == store.SandboxSessionStateEvicted || existing.State == store.SandboxSessionStateResuming {
			if err := b.resumeSessionPod(ctx, existing, spec); err != nil {
				return nil, fmt.Errorf("CreateSession: resume evicted session: %w", err)
			}
			resumed, err := b.sessions.GetSession(spec.SessionID)
			if err != nil {
				return nil, fmt.Errorf("CreateSession: load resumed session: %w", err)
			}
			return sessionFromStore(resumed, spec.Type), nil
		}
		// The pod is gone but the session was not cleanly evicted. Recreate
		// from the supplied spec. This may lose volatile upper data, but it is
		// safer than returning a dead pod binding.
		_ = b.sessions.Remove(spec.SessionID)
	}

	return b.createSessionPod(ctx, spec, nil)
}

func (b *K8sBackend) createSessionPod(ctx context.Context, spec sandbox.SessionSpec, existing *store.SandboxSession) (*sandbox.Session, error) {
	if spec.TemplateID == "" {
		spec.TemplateID = sandbox.BaseTemplateID
	}
	pod, err := b.buildPodManifest(spec)
	if err != nil {
		return nil, fmt.Errorf("CreateSession: build manifest: %w", err)
	}

	created, err := b.client.CoreV1().Pods(b.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("CreateSession: create pod: %w", err)
	}
	if apierrors.IsAlreadyExists(err) {
		created, err = b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("CreateSession: fetch existing pod: %w", err)
		}
	}

	now := time.Now().UTC()
	rec := existing
	if rec == nil {
		rec = &store.SandboxSession{
			SessionID:     spec.SessionID,
			ChatSessionID: spec.SessionID,
			CreatedAt:     now,
			LastActiveAt:  now,
		}
	}
	if rec.ChatSessionID == "" {
		rec.ChatSessionID = spec.SessionID
	}
	if rec.TemplateID == "" {
		rec.TemplateID = spec.TemplateID
	}
	if rec.CreatedBy == "" {
		rec.CreatedBy = spec.UserID
	}
	rec.Backend = string(sandbox.BackendKindK8s)
	rec.TemplateID = spec.TemplateID
	rec.State = store.SandboxSessionStateCreating
	rec.PodName = created.Name
	rec.NodeName = created.Spec.NodeName
	rec.UpdatedAt = now
	if rec.LastActiveAt.IsZero() {
		rec.LastActiveAt = now
	}
	if err := b.sessions.PutSession(rec); err != nil {
		_ = b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, created.Name, metav1.DeleteOptions{})
		return nil, fmt.Errorf("CreateSession: persist registry: %w", err)
	}

	return sessionFromStore(rec, spec.Type), nil
}

// StartSession resumes a stopped or evicted session by recreating the pod
// with the same session ID and the layer-chain annotation saved at create
// time. The sandbox entrypoint restores the persisted upper tarball for that
// exact session before mounting the overlay.
func (b *K8sBackend) StartSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.client == nil {
		return fmt.Errorf("StartSession: %w", ErrNotImplementedYet)
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("StartSession: lookup: %w", err)
	}
	if rec == nil {
		return fmt.Errorf("StartSession: session %q not found", sessionID)
	}

	if err := b.resumeSessionPod(ctx, rec, sandbox.SessionSpec{SessionID: sessionID, Type: sandbox.SessionTypeChat}); err != nil {
		return fmt.Errorf("StartSession: recreate pod: %w", err)
	}
	return nil
}

func (b *K8sBackend) resumeSessionPod(ctx context.Context, rec *store.SandboxSession, override sandbox.SessionSpec) error {
	if rec == nil {
		return errors.New("session record is required")
	}
	originalLayerChain := persistedLayerChain(rec)
	if override.SessionID == "" {
		override.SessionID = rec.SessionID
	}
	if override.SessionID == "" {
		return errors.New("session ID is required")
	}
	if override.SessionID != rec.SessionID {
		return fmt.Errorf("session ID mismatch: registry has %q, requested %q", rec.SessionID, override.SessionID)
	}

	state, err := b.SessionState(ctx, override.SessionID)
	if err != nil {
		return fmt.Errorf("query state: %w", err)
	}
	switch state {
	case sandbox.SessionStateRunning, sandbox.SessionStateCreating, sandbox.SessionStateResuming:
		return nil
	case sandbox.SessionStateStopped:
		name := rec.PodName
		if name == "" {
			name = podNameForSession(override.SessionID)
		}
		deleteErr := b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return fmt.Errorf("delete stopped pod: %w", deleteErr)
		}
	case sandbox.SessionStateGone:
		// Recreate below.
	default:
		return nil
	}

	spec := override
	spec.SessionID = rec.SessionID
	if spec.Type == "" {
		spec.Type = sandbox.SessionTypeChat
	}
	if spec.TemplateID == "" {
		spec.TemplateID = rec.TemplateID
	}
	if spec.UserID == "" {
		spec.UserID = rec.CreatedBy
	}
	// Once a session has been evicted, resume it against the exact lower
	// chain captured at eviction time. A caller may be using a newer team or
	// base template now; replaying the old upper onto that new lower risks an
	// inconsistent filesystem view.
	if originalLayerChain != "" {
		spec.LayerChain = splitLayerChain(originalLayerChain)
	} else if len(spec.LayerChain) == 0 {
		spec.LayerChain = layerChainFromSessionRecord(rec)
	}
	if spec.TemplateID == "" {
		spec.TemplateID = sandbox.BaseTemplateID
	}

	now := time.Now().UTC()
	rec.State = store.SandboxSessionStateResuming
	rec.PodName = ""
	rec.NodeName = ""
	rec.UpdatedAt = now
	if err := b.sessions.PutSession(rec); err != nil {
		return fmt.Errorf("mark resuming: %w", err)
	}

	if _, err := b.createSessionPod(ctx, spec, rec); err != nil {
		return err
	}
	return nil
}

func layerChainFromSessionRecord(rec *store.SandboxSession) []string {
	return splitLayerChain(persistedLayerChain(rec))
}

func persistedLayerChain(rec *store.SandboxSession) string {
	if rec == nil || !strings.HasPrefix(rec.UpperLayerID, sessionLayerChainPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(rec.UpperLayerID, sessionLayerChainPrefix))
}

func encodePersistedLayerChain(chainText string) string {
	chainText = strings.TrimSpace(chainText)
	if chainText == "" {
		return ""
	}
	return sessionLayerChainPrefix + chainText
}

func splitLayerChain(chainText string) []string {
	parts := strings.Split(chainText, ",")
	chain := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			chain = append(chain, part)
		}
	}
	return chain
}

// StopSession pauses a running session by first persisting its writable upper
// layer to the session-specific directory on the uppers PVC, then deleting the
// pod. If persistence fails, the pod is left running so data is not lost.
func (b *K8sBackend) StopSession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.client == nil {
		return fmt.Errorf("StopSession: %w", ErrNotImplementedYet)
	}
	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("StopSession: lookup: %w", err)
	}
	if rec == nil {
		return nil // idempotent
	}
	if rec.PodName == "" {
		return nil
	}

	now := time.Now().UTC()
	rec.State = store.SandboxSessionStateEvicting
	rec.UpdatedAt = now
	if err := b.sessions.PutSession(rec); err != nil {
		return fmt.Errorf("StopSession: mark evicting: %w", err)
	}

	pod, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, rec.PodName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		rec.State = store.SandboxSessionStateRunning
		rec.UpdatedAt = time.Now().UTC()
		_ = b.sessions.PutSession(rec)
		return fmt.Errorf("StopSession: get pod before persist: %w", err)
	}
	capturedLayerChain := rec.UpperLayerID
	if err == nil {
		capturedLayerChain = encodePersistedLayerChain(pod.Annotations[annotationLayerChain])
	}

	if err := b.persistUpperTarball(ctx, rec.PodName, sessionID); err != nil {
		rec.State = store.SandboxSessionStateRunning
		rec.UpdatedAt = time.Now().UTC()
		_ = b.sessions.PutSession(rec)
		return fmt.Errorf("StopSession: persist upper: %w", err)
	}

	err = b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, rec.PodName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("StopSession: delete pod: %w", err)
	}
	rec.State = store.SandboxSessionStateEvicted
	rec.PodName = ""
	rec.NodeName = ""
	rec.UpperLayerID = capturedLayerChain
	rec.UpdatedAt = time.Now().UTC()
	if err := b.sessions.PutSession(rec); err != nil {
		return fmt.Errorf("StopSession: persist registry: %w", err)
	}
	return nil
}

func (b *K8sBackend) persistUpperTarball(ctx context.Context, podName, sessionID string) error {
	if _, err := upperDirNameForSession(sessionID); err != nil {
		return err
	}
	script := buildPersistUpperScript(mountUppers, "")
	res, err := b.execInPod(ctx, podName, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("persist upper exited %d: stderr=%s", res.ExitCode, truncate(string(res.Stderr), 512))
	}
	return nil
}

func buildPersistUpperScript(uppersPath, sessionDir string) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	sessionPath := strings.TrimRight(uppersPath, "/")
	if sessionDir != "" {
		sessionPath += "/" + sessionDir
	}
	fmt.Fprintf(&b, "SESSION_DIR=%q\n", sessionPath)
	b.WriteString("TMP=\"$SESSION_DIR/upper.tar.zst.tmp\"\n")
	b.WriteString("FINAL=\"$SESSION_DIR/upper.tar.zst\"\n")
	b.WriteString("mkdir -p \"$SESSION_DIR\"\n")
	b.WriteString("rm -f \"$TMP\"\n")
	b.WriteString("tar --numeric-owner --xattrs --acls -I 'zstd --adapt -T0' \\\n")
	fmt.Fprintf(&b, "  --exclude=%s/mnt/astonish-uppers \\\n", mountUpper)
	fmt.Fprintf(&b, "  -C %s -cf \"$TMP\" .\n", mountUpper)
	b.WriteString("test -s \"$TMP\"\n")
	b.WriteString("mv \"$TMP\" \"$FINAL\"\n")
	return b.String()
}

// DestroySession permanently removes the session. Idempotent: absent
// sessions succeed without error.
//
// If the session was previously evicted (upper layer persisted to the
// uppers PVC), a short-lived GC pod removes the tarball directory
// before the registry row is dropped. This prevents orphan growth on
// the uppers PVC.
func (b *K8sBackend) DestroySession(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.client == nil {
		// No client configured — treat as no-op rather than fail, so
		// the stub is usable in tests that want to confirm registry
		// drainage without wiring a fake client.
		return b.sessions.Remove(sessionID)
	}
	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("DestroySession: lookup: %w", err)
	}
	name := podNameForSession(sessionID)
	if rec != nil && rec.PodName != "" {
		name = rec.PodName
	}
	err = b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("DestroySession: delete pod: %w", err)
	}

	// Reclaim evicted upper-layer tarball from the uppers PVC.
	if rec != nil && (rec.State == store.SandboxSessionStateEvicted || (rec.State == store.SandboxSessionStateEvicting && rec.PodName == "")) {
		if gcErr := b.reclaimUpperTarball(ctx, sessionID); gcErr != nil {
			// Best-effort: log but don't fail the destroy. The deferred
			// GC reconciler (§5.12.2) will catch it later.
			fmt.Printf("[sandbox/k8s] DestroySession(%s): upper cleanup failed: %v\n", sessionID, gcErr)
		}
	}

	if err := b.sessions.Remove(sessionID); err != nil {
		return fmt.Errorf("DestroySession: remove from registry: %w", err)
	}
	return nil
}

// reclaimUpperTarball spawns a short-lived GC pod that removes the
// evicted upper directory from the uppers PVC:
//
//	/mnt/astonish-uppers/<sessionID>/
//
// Uses the same pattern as DeleteTemplate's GC pod. The pod is
// defer-deleted regardless of outcome.
func (b *K8sBackend) reclaimUpperTarball(ctx context.Context, sessionID string) error {
	dirName, err := upperDirNameForSession(sessionID)
	if err != nil {
		return err
	}
	podName := "astn-upper-gc-" + toDNSLabel(sessionID)
	if len(podName) > 63 {
		podName = podName[:63]
		for len(podName) > 0 && podName[len(podName)-1] == '-' {
			podName = podName[:len(podName)-1]
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: b.cfg.Namespace,
			Labels: map[string]string{
				labelType: "upper-gc",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:            "gc",
					Image:           "alpine:3.21",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/bin/sh", "-c", fmt.Sprintf("rm -rf %s/%s", mountUppers, dirName)},
					VolumeMounts: []corev1.VolumeMount{
						{Name: volumeUppers, MountPath: mountUppers},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: volumeUppers,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: b.cfg.UppersPVCName,
						},
					},
				},
			},
		},
	}

	// Always clean up the GC pod.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = b.client.CoreV1().Pods(b.cfg.Namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	// Delete any leftover pod from a previous failed attempt.
	_ = b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	time.Sleep(500 * time.Millisecond)

	if _, err := b.client.CoreV1().Pods(b.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create upper-gc pod: %w", err)
	}

	if err := b.waitForPodDone(ctx, podName, 60*time.Second); err != nil {
		return fmt.Errorf("wait upper-gc pod: %w", err)
	}

	return nil
}

// SessionState queries the pod and maps its phase to sandbox.SessionState.
// Returns SessionStateGone for absent pods or absent registry rows.
func (b *K8sBackend) SessionState(ctx context.Context, sessionID string) (sandbox.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if b.client == nil {
		return "", fmt.Errorf("SessionState: %w", ErrNotImplementedYet)
	}
	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("SessionState: lookup: %w", err)
	}
	if rec == nil {
		return sandbox.SessionStateGone, nil
	}
	// Evicted sessions are known-not-running without a pod.
	if rec.State == store.SandboxSessionStateEvicted && rec.PodName == "" {
		return sandbox.SessionStateStopped, nil
	}
	name := rec.PodName
	if name == "" {
		name = podNameForSession(sessionID)
	}
	pod, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return sandbox.SessionStateGone, nil
		}
		return "", fmt.Errorf("SessionState: get pod: %w", err)
	}
	return podPhaseToSessionState(pod), nil
}

// sessionReadyTimeout caps how long WaitForSessionReady polls. This
// covers image pull + PVC bind + overlay composition on first use.
const sessionReadyTimeout = 2 * time.Minute

// sessionReadyPollInterval is the poll cadence for WaitForSessionReady.
const sessionReadyPollInterval = 500 * time.Millisecond

// WaitForSessionReady polls the session's pod until it reaches Running phase.
// Returns an error if the pod enters a terminal phase (Failed/Succeeded),
// the timeout is exceeded, or the context is cancelled.
func (b *K8sBackend) WaitForSessionReady(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.client == nil {
		return fmt.Errorf("WaitForSessionReady: %w", ErrNotImplementedYet)
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("WaitForSessionReady(%s): lookup: %w", sessionID, err)
	}
	if rec == nil {
		return fmt.Errorf("WaitForSessionReady: session %q not found", sessionID)
	}

	podName := rec.PodName
	if podName == "" {
		podName = podNameForSession(sessionID)
	}

	deadline := time.Now().Add(sessionReadyTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("WaitForSessionReady(%s): %w", sessionID, err)
		}
		pod, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Pod not yet visible — may be a propagation delay
				if time.Now().After(deadline) {
					return fmt.Errorf("WaitForSessionReady(%s): pod %q not found within timeout", sessionID, podName)
				}
			} else {
				return fmt.Errorf("WaitForSessionReady(%s): get pod: %w", sessionID, err)
			}
		} else {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				return nil
			case corev1.PodFailed, corev1.PodSucceeded:
				return fmt.Errorf("WaitForSessionReady(%s): pod reached terminal phase %s", sessionID, pod.Status.Phase)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("WaitForSessionReady(%s): pod did not reach Running within %s", sessionID, sessionReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("WaitForSessionReady(%s): %w", sessionID, ctx.Err())
		case <-time.After(sessionReadyPollInterval):
		}
	}
}

// ListSessions enumerates pods in the configured namespace that carry
// the session type label, materialising each as a sandbox.Session.
// Filter fields are applied client-side where a server-side label
// selector cannot express them.
func (b *K8sBackend) ListSessions(ctx context.Context, filter sandbox.SessionFilter) ([]*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.client == nil {
		return nil, fmt.Errorf("ListSessions: %w", ErrNotImplementedYet)
	}

	selector := labelSelectorFor(filter)
	list, err := b.client.CoreV1().Pods(b.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("ListSessions: list pods: %w", err)
	}

	out := make([]*sandbox.Session, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		s := sessionFromPod(p)
		if filter.State != "" && s.State != filter.State {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// labelSelectorFor constructs a server-side label selector from the
// subset of SessionFilter fields that are expressible as equality
// matches against sandbox pod labels.
func labelSelectorFor(f sandbox.SessionFilter) string {
	parts := []string{labelType + "=" + typeSession}
	if f.Type == sandbox.SessionTypeFleet {
		parts[0] = labelType + "=" + typeFleet
	}
	if f.OrgSlug != "" {
		parts = append(parts, labelOrg+"="+toDNSLabel(f.OrgSlug))
	}
	if f.TeamSlug != "" {
		parts = append(parts, labelTeam+"="+toDNSLabel(f.TeamSlug))
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------
// State mapping helpers
// ---------------------------------------------------------------------------

// podPhaseToSessionState translates the observed pod phase to the
// backend-neutral SessionState values. Derived from §3.6 state machine.
func podPhaseToSessionState(pod *corev1.Pod) sandbox.SessionState {
	switch pod.Status.Phase {
	case corev1.PodPending:
		return sandbox.SessionStateCreating
	case corev1.PodRunning:
		if isPodReady(pod) {
			return sandbox.SessionStateRunning
		}
		return sandbox.SessionStateCreating
	case corev1.PodSucceeded, corev1.PodFailed:
		return sandbox.SessionStateStopped
	default:
		return sandbox.SessionStateCreating
	}
}

// isPodReady reports whether the pod's Ready condition is True.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// sessionFromStore constructs a sandbox.Session from a persisted
// store.SandboxSession. State is derived from the store's state column
// (the registry-level view — live cluster state is obtained through
// SessionState, which queries the API server).
func sessionFromStore(rec *store.SandboxSession, sessType sandbox.SessionType) *sandbox.Session {
	if sessType == "" {
		sessType = sandbox.SessionTypeChat
	}
	return &sandbox.Session{
		SessionID:  rec.SessionID,
		Type:       sessType,
		TemplateID: rec.TemplateID,
		State:      storeStateToSessionState(rec.State),
		BackendRef: rec.PodName,
		CreatedAt:  rec.CreatedAt,
		LastActive: rec.LastActiveAt,
	}
}

// sessionFromPod constructs a sandbox.Session from a corev1.Pod. Used
// by ListSessions where the source of truth is the API server rather
// than the registry.
func sessionFromPod(pod *corev1.Pod) *sandbox.Session {
	typ := sandbox.SessionTypeChat
	if pod.Labels[labelType] == typeFleet {
		typ = sandbox.SessionTypeFleet
	}
	return &sandbox.Session{
		SessionID:  pod.Labels[labelSessionID],
		Type:       typ,
		TemplateID: pod.Labels[labelTemplate],
		OrgSlug:    pod.Labels[labelOrg],
		TeamSlug:   pod.Labels[labelTeam],
		State:      podPhaseToSessionState(pod),
		BackendRef: pod.Name,
		Labels:     pod.Labels,
		CreatedAt:  pod.CreationTimestamp.Time,
	}
}

// storeStateToSessionState maps persisted state values to the interface's
// SessionState. The two enums overlap but are not identical (the store
// has "terminated", the interface has "gone"; the store has "evicted",
// the interface reports that as "stopped").
func storeStateToSessionState(s store.SandboxSessionState) sandbox.SessionState {
	switch s {
	case store.SandboxSessionStateCreating:
		return sandbox.SessionStateCreating
	case store.SandboxSessionStateRunning:
		return sandbox.SessionStateRunning
	case store.SandboxSessionStateEvicting:
		return sandbox.SessionStateEvicting
	case store.SandboxSessionStateEvicted:
		return sandbox.SessionStateStopped
	case store.SandboxSessionStateResuming:
		return sandbox.SessionStateResuming
	case store.SandboxSessionStateTerminated:
		return sandbox.SessionStateGone
	default:
		return sandbox.SessionStateCreating
	}
}
