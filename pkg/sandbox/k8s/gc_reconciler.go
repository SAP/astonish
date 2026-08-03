// Package k8s — sandbox storage GC reconciler.
//
// This file implements the deferred GC reconciler (§5.12.2 of
// sandbox-backends.md). It runs as a goroutine in the daemon and
// periodically:
//
//  1. Acquires a PG advisory lock so only one pod runs the reconciler.
//  2. Reclaims orphan layer directories from the layers PVC.
//  3. Reclaims orphan upper directories from the uppers PVC.
//  4. Reclaims stale evicted session uppers after the configured retention window.
//  5. Cleans up __staging-* directories from crashed template builders (skipping active ones).
//  6. Prunes orphan session/fleet pods whose session no longer exists in any team DB.
//  7. Cleans up stale (Succeeded/Failed) gc-ls/gc-rm pods older than 1h.
//
// The reconciler is leader-elected via pg_try_advisory_lock so it's safe
// to run in multi-replica deployments without double-deleting.

package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// GCReconcilerConfig configures the deferred GC reconciler.
type GCReconcilerConfig struct {
	// Interval between reconciler runs. Default: 1h.
	Interval time.Duration

	// GracePeriod for unreferenced layers. Layers with ref_count=0
	// older than this are candidates for deletion. Default: 24h.
	LayerGracePeriod time.Duration

	// UpperGracePeriod for orphan upper directories. Upper dirs not
	// tracked in any session and older than this are deleted. Default: 1h.
	UpperGracePeriod time.Duration

	// Namespace is the sandbox namespace where GC pods are spawned.
	Namespace string

	// LayersPVCName is the claim name for the layers PVC.
	LayersPVCName string

	// UppersPVCName is the claim name for the uppers PVC.
	UppersPVCName string

	// Client is the K8s clientset for spawning GC pods.
	Client kubernetes.Interface

	// PlatformPool is the PG connection pool for the platform database
	// (advisory lock + sandbox_layers queries).
	PlatformPool *pgxpool.Pool

	// Layers is the layer store for ref_count queries and row deletion.
	Layers store.LayerStore

	// SandboxSessionsQuerier returns all known sandbox session IDs (across all teams).
	// If non-nil, the reconciler periodically prunes orphan session/fleet pods
	// whose session no longer exists in any team schema.
	SandboxSessionsQuerier func(ctx context.Context) (map[string]bool, error)

	// SandboxSessionsMetadataQuerier returns all known sandbox sessions with
	// enough metadata to make retention decisions for stale evicted uppers.
	SandboxSessionsMetadataQuerier func(ctx context.Context) ([]store.SandboxSessionGCInfo, error)

	// SandboxSessionDeleter deletes one sandbox session row from its owning team
	// schema. It is called only after the corresponding persisted upper directory
	// has been removed successfully.
	SandboxSessionDeleter func(ctx context.Context, session store.SandboxSessionGCInfo) error

	// EvictedUpperRetention controls how long evicted, unpinned K8s sessions
	// remain resumable. A zero value disables stale evicted upper cleanup.
	EvictedUpperRetention time.Duration
}

type gcDirInfo struct {
	Name    string
	ModTime time.Time
}

// advisoryLockID is a stable hash for the reconciler's PG advisory lock.
// hashtext('astonish-sandbox-gc') evaluated once = 1267985313
const advisoryLockID int64 = 1267985313

// RunGCReconciler starts the deferred GC reconciler loop. It blocks until
// ctx is cancelled. Intended to be run as a goroutine.
func RunGCReconciler(ctx context.Context, cfg GCReconcilerConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.LayerGracePeriod <= 0 {
		cfg.LayerGracePeriod = 24 * time.Hour
	}
	if cfg.UpperGracePeriod <= 0 {
		cfg.UpperGracePeriod = time.Hour
	}
	if cfg.LayersPVCName == "" {
		cfg.LayersPVCName = "astonish-layers"
	}
	if cfg.UppersPVCName == "" {
		cfg.UppersPVCName = "astonish-uppers"
	}

	// Initial delay to avoid contention at startup.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		runGCCycle(ctx, cfg)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runGCCycle(ctx context.Context, cfg GCReconcilerConfig) {
	// Try to acquire advisory lock — non-blocking. Only one pod wins.
	conn, err := cfg.PlatformPool.Acquire(ctx)
	if err != nil {
		slog.Debug("gc-reconciler: failed to acquire PG connection", "error", err)
		return
	}
	defer conn.Release()

	var acquired bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, advisoryLockID).Scan(&acquired)
	if err != nil || !acquired {
		// Another pod holds the lock — skip this cycle.
		return
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockID)
	}()

	slog.Info("gc-reconciler: acquired advisory lock, starting cycle")
	start := time.Now()

	layersReclaimed := gcReclaimLayers(ctx, cfg)
	uppersReclaimed := gcReclaimUppers(ctx, cfg)
	staleEvictedUppers := gcReclaimStaleEvictedUppers(ctx, cfg)
	stagingReclaimed := gcReclaimStaging(ctx, cfg)
	orphanLayerDirs := gcReclaimOrphanLayerDirs(ctx, cfg)
	orphanPodsPruned := gcPruneOrphanPods(ctx, cfg)
	staleGCPods := gcCleanupStaleGCPods(ctx, cfg)

	slog.Info("gc-reconciler: cycle complete",
		"layers_reclaimed", layersReclaimed,
		"uppers_reclaimed", uppersReclaimed,
		"stale_evicted_uppers", staleEvictedUppers,
		"staging_reclaimed", stagingReclaimed,
		"orphan_layer_dirs", orphanLayerDirs,
		"orphan_pods_pruned", orphanPodsPruned,
		"stale_gc_pods", staleGCPods,
		"duration", time.Since(start).Round(time.Millisecond),
	)
}

// gcReclaimLayers finds layers with ref_count=0 past the grace period and
// removes their bytes from disk + PG row.
func gcReclaimLayers(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Layers == nil {
		return 0
	}
	candidates, err := cfg.Layers.ListUnreferenced(ctx, cfg.LayerGracePeriod)
	if err != nil {
		slog.Warn("gc-reconciler: ListUnreferenced failed", "error", err)
		return 0
	}

	reclaimed := 0
	for _, layer := range candidates {
		if ctx.Err() != nil {
			break
		}
		// Skip @base.
		if layer.LayerID == "a0000000-0000-4000-8000-000000000001" {
			continue
		}

		// Delete bytes from disk via a GC pod.
		if err := gcDeleteDir(ctx, cfg.Client, cfg.Namespace, cfg.LayersPVCName, "/mnt/layers", layer.LayerID); err != nil {
			slog.Warn("gc-reconciler: failed to delete layer dir",
				"layer", layer.LayerID, "error", err)
			continue
		}

		// Delete PG row.
		if err := cfg.Layers.DeleteLayer(ctx, layer.LayerID); err != nil {
			slog.Warn("gc-reconciler: failed to delete layer PG row",
				"layer", layer.LayerID, "error", err)
			continue
		}

		reclaimed++
	}
	return reclaimed
}

// gcReclaimUppers lists directories on the uppers PVC via an audit pod,
// diffs against known sessions, and removes orphans older than the grace period.
func gcReclaimUppers(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.SandboxSessionsQuerier == nil || cfg.Client == nil {
		return 0
	}

	dirs, err := gcListDirInfos(ctx, cfg.Client, cfg.Namespace, cfg.UppersPVCName, "/mnt/uppers")
	if err != nil {
		slog.Warn("gc-reconciler: failed to list uppers dirs", "error", err)
		return 0
	}

	knownSessions, err := cfg.SandboxSessionsQuerier(ctx)
	if err != nil {
		slog.Warn("[gc-reconciler] failed to query sandbox sessions", "err", err)
		return 0
	}

	orphans := orphanUpperCandidates(dirs, knownSessions, time.Now(), cfg.UpperGracePeriod)
	if len(orphans) == 0 {
		return 0
	}

	if err := gcDeleteDirs(ctx, cfg.Client, cfg.Namespace, cfg.UppersPVCName, "/mnt/uppers", orphans); err != nil {
		slog.Warn("gc-reconciler: failed to delete orphan upper dirs", "error", err)
		return 0
	}

	return len(orphans)
}

func orphanUpperCandidates(dirs []gcDirInfo, knownSessions map[string]bool, now time.Time, grace time.Duration) []string {
	var orphans []string
	for _, d := range dirs {
		if d.Name == "" || knownSessions[d.Name] {
			continue
		}
		if _, err := upperDirNameForSession(d.Name); err != nil {
			slog.Warn("gc-reconciler: skipping unsafe orphan upper dir", "dir", d.Name, "error", err)
			continue
		}
		if grace > 0 && !d.ModTime.IsZero() && now.Sub(d.ModTime) < grace {
			slog.Debug("gc-reconciler: retaining young orphan upper dir", "dir", d.Name, "age", now.Sub(d.ModTime).Round(time.Second), "grace", grace)
			continue
		}
		orphans = append(orphans, d.Name)
	}
	return orphans
}

func gcReclaimStaleEvictedUppers(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Client == nil || cfg.SandboxSessionsMetadataQuerier == nil || cfg.SandboxSessionDeleter == nil || cfg.EvictedUpperRetention <= 0 {
		return 0
	}

	sessions, err := cfg.SandboxSessionsMetadataQuerier(ctx)
	if err != nil {
		slog.Warn("gc-reconciler: failed to query sandbox session metadata", "error", err)
		return 0
	}
	if len(sessions) == 0 {
		return 0
	}

	liveSessions := gcLiveSandboxSessions(ctx, cfg)
	candidates := staleEvictedUpperCandidates(sessions, liveSessions, time.Now(), cfg.EvictedUpperRetention)
	if len(candidates) == 0 {
		return 0
	}

	reclaimed := 0
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		if err := gcDeleteDir(ctx, cfg.Client, cfg.Namespace, cfg.UppersPVCName, "/mnt/uppers", candidate.SessionID); err != nil {
			slog.Warn("gc-reconciler: failed to delete stale evicted upper dir", "session", candidate.SessionID, "team_schema", candidate.TeamSchema, "error", err)
			continue
		}
		if err := cfg.SandboxSessionDeleter(ctx, candidate); err != nil {
			slog.Warn("gc-reconciler: deleted stale evicted upper but failed to delete session row", "session", candidate.SessionID, "team_schema", candidate.TeamSchema, "error", err)
			continue
		}
		reclaimed++
		slog.Info("gc-reconciler: reclaimed stale evicted upper", "session", candidate.SessionID, "team_schema", candidate.TeamSchema, "age", time.Since(sessionRetentionTime(candidate)).Round(time.Hour))
	}
	return reclaimed
}

func staleEvictedUpperCandidates(sessions []store.SandboxSessionGCInfo, liveSessions map[string]bool, now time.Time, retention time.Duration) []store.SandboxSessionGCInfo {
	if retention <= 0 {
		return nil
	}
	var candidates []store.SandboxSessionGCInfo
	for _, sess := range sessions {
		if sess.Backend != string(sandbox.BackendKindK8s) {
			continue
		}
		if sess.State != store.SandboxSessionStateEvicted && sess.State != store.SandboxSessionStateTerminated {
			continue
		}
		if sess.Pinned || sess.PodName != "" || liveSessions[sess.SessionID] {
			continue
		}
		if _, err := upperDirNameForSession(sess.SessionID); err != nil {
			slog.Warn("gc-reconciler: skipping stale upper candidate with unsafe session ID", "session", sess.SessionID, "error", err)
			continue
		}
		basis := sessionRetentionTime(sess)
		if basis.IsZero() || now.Sub(basis) < retention {
			continue
		}
		candidates = append(candidates, sess)
	}
	return candidates
}

func sessionRetentionTime(sess store.SandboxSessionGCInfo) time.Time {
	if !sess.LastActiveAt.IsZero() {
		return sess.LastActiveAt
	}
	if !sess.UpdatedAt.IsZero() {
		return sess.UpdatedAt
	}
	return sess.CreatedAt
}

func gcLiveSandboxSessions(ctx context.Context, cfg GCReconcilerConfig) map[string]bool {
	live := make(map[string]bool)
	if cfg.Client == nil {
		return live
	}
	selector := labelType + " in (" + typeSession + "," + typeFleet + ")"
	list, err := cfg.Client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		slog.Warn("gc-reconciler: failed to list live sandbox pods for stale upper cleanup", "error", err)
		return live
	}
	for i := range list.Items {
		p := &list.Items[i]
		sid := p.Labels[labelSessionID]
		if sid == "" {
			continue
		}
		switch p.Status.Phase {
		case corev1.PodRunning, corev1.PodPending, corev1.PodUnknown:
			live[sid] = true
		}
	}
	return live
}

// gcReclaimStaging lists __staging-* directories on the layers PVC
// (left behind by crashed template-builder pods) and removes them.
// It skips staging directories that belong to still-running builder pods.
func gcReclaimStaging(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Client == nil {
		return 0
	}

	dirs, err := gcListDirs(ctx, cfg.Client, cfg.Namespace, cfg.LayersPVCName, "/mnt/layers")
	if err != nil {
		slog.Warn("gc-reconciler: failed to list layers dirs for staging cleanup", "error", err)
		return 0
	}

	// Collect names of currently running (or pending) template builder pods.
	runningBuilders := map[string]bool{}
	if pods, listErr := cfg.Client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelType + "=" + typeTemplateBuilder,
	}); listErr == nil {
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
				runningBuilders[p.Name] = true
			}
		}
	}

	var staging []string
	for _, d := range dirs {
		if !strings.HasPrefix(d, "__staging-") {
			continue
		}
		// d = "__staging-<podName>-<nano>"
		rest := strings.TrimPrefix(d, "__staging-")
		// Skip if any running builder pod name is a prefix of the rest (i.e. rest starts with "<podName>-")
		skip := false
		for name := range runningBuilders {
			if strings.HasPrefix(rest, name+"-") {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		staging = append(staging, d)
	}

	if len(staging) == 0 {
		return 0
	}

	if err := gcDeleteDirs(ctx, cfg.Client, cfg.Namespace, cfg.LayersPVCName, "/mnt/layers", staging); err != nil {
		slog.Warn("gc-reconciler: failed to delete staging dirs", "error", err)
		return 0
	}

	return len(staging)
}

// gcReclaimOrphanLayerDirs detects layer directories that exist on disk but have
// no corresponding row in the sandbox_layers table. This catches drift caused by
// failed PutLayer, manual row deletion, or bugs.
func gcReclaimOrphanLayerDirs(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Layers == nil || cfg.Client == nil {
		return 0
	}

	dirs, err := gcListDirs(ctx, cfg.Client, cfg.Namespace, cfg.LayersPVCName, "/mnt/layers")
	if err != nil {
		slog.Warn("gc-reconciler: failed to list layer dirs for drift scan", "error", err)
		return 0
	}

	// Filter to plausible layer UUIDs, exclude staging and the sacred @base.
	var candidates []string
	for _, d := range dirs {
		if strings.HasPrefix(d, "__staging-") {
			continue
		}
		if d == "a0000000-0000-4000-8000-000000000001" {
			continue
		}
		// Rough UUID check
		if len(d) == 36 && strings.Count(d, "-") == 4 {
			candidates = append(candidates, d)
		}
	}

	if len(candidates) == 0 {
		return 0
	}

	known := make(map[string]bool)
	rows, err := cfg.Layers.ListAll(ctx)
	if err != nil {
		slog.Warn("gc-reconciler: ListAll failed during drift scan", "error", err)
		return 0
	}
	for _, r := range rows {
		known[r.LayerID] = true
	}

	var orphans []string
	for _, d := range candidates {
		if !known[d] {
			orphans = append(orphans, d)
		}
	}

	if len(orphans) == 0 {
		return 0
	}

	if err := gcDeleteDirs(ctx, cfg.Client, cfg.Namespace, cfg.LayersPVCName, "/mnt/layers", orphans); err != nil {
		slog.Warn("gc-reconciler: failed to delete orphan layer dirs", "error", err)
		return 0
	}

	return len(orphans)
}

// gcPruneOrphanPods lists session/fleet pods and deletes those whose session ID
// is no longer present in any team schema (and older than 1h).
func gcPruneOrphanPods(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Client == nil || cfg.SandboxSessionsQuerier == nil {
		return 0
	}
	if ctx.Err() != nil {
		return 0
	}

	known, err := cfg.SandboxSessionsQuerier(ctx)
	if err != nil {
		slog.Warn("gc-reconciler: SandboxSessionsQuerier failed", "error", err)
		return 0
	}

	selector := labelType + " in (" + typeSession + "," + typeFleet + ")"
	list, err := cfg.Client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		slog.Warn("gc-reconciler: failed to list session/fleet pods", "error", err)
		return 0
	}

	pruned := 0
	for i := range list.Items {
		p := &list.Items[i]
		sid := p.Labels[labelSessionID]
		if sid == "" {
			continue
		}
		if known[sid] {
			continue
		}
		created := p.CreationTimestamp.Time
		if time.Since(created) < time.Hour {
			continue
		}

		slog.Info("gc-reconciler: pruning orphan sandbox pod",
			"pod", p.Name, "session", sid, "age", time.Since(created).Round(time.Minute))

		delErr := cfg.Client.CoreV1().Pods(cfg.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{})
		if delErr != nil && !apierrors.IsNotFound(delErr) {
			slog.Warn("gc-reconciler: failed to delete orphan pod", "pod", p.Name, "error", delErr)
			continue
		}
		pruned++
	}
	return pruned
}

// gcCleanupStaleGCPods deletes old completed/failed gc-ls / gc-rm pods (>1h).
func gcCleanupStaleGCPods(ctx context.Context, cfg GCReconcilerConfig) int {
	if cfg.Client == nil {
		return 0
	}

	selector := labelType + " in (gc-list,gc-rm)"
	list, err := cfg.Client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0
	}

	cleaned := 0
	for i := range list.Items {
		p := &list.Items[i]
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			continue
		}
		created := p.CreationTimestamp.Time
		if time.Since(created) < time.Hour {
			continue
		}
		_ = cfg.Client.CoreV1().Pods(cfg.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{})
		cleaned++
	}
	return cleaned
}

// ---------------------------------------------------------------------------
// Pod helpers for the GC reconciler
// ---------------------------------------------------------------------------

// gcListDirInfos spawns a read-only pod that lists top-level directories and
// their modification times on a PVC.
func gcListDirInfos(ctx context.Context, cs kubernetes.Interface, namespace, pvcName, mountPath string) ([]gcDirInfo, error) {
	podName := fmt.Sprintf("astn-gc-ls-%d", time.Now().UnixNano()%1000000)
	cmd := fmt.Sprintf(`for d in %s/*; do
  [ -d "$d" ] || continue
  name=${d##*/}
  mtime=$(stat -c %%Y "$d" 2>/dev/null || stat -f %%m "$d" 2>/dev/null || echo 0)
  printf '%%s\t%%s\n' "$name" "$mtime"
done`, mountPath)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{labelType: "gc-list"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "ls",
				Image:           "alpine:3.21",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sh", "-c", cmd},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "data", MountPath: mountPath, ReadOnly: true},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  true,
					},
				},
			}},
		},
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cs.CoreV1().Pods(namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	_ = cs.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	time.Sleep(500 * time.Millisecond)

	if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create list pod: %w", err)
	}

	if err := gcWaitForPodDone(ctx, cs, namespace, podName, 60*time.Second); err != nil {
		return nil, err
	}

	logReq := cs.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	logStream, err := logReq.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("read list pod logs: %w", err)
	}
	defer logStream.Close()

	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, readErr := logStream.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if readErr != nil {
			break
		}
	}

	var dirs []gcDirInfo
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		info := gcDirInfo{Name: name}
		if len(parts) == 2 {
			if sec, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); parseErr == nil && sec > 0 {
				info.ModTime = time.Unix(sec, 0)
			}
		}
		dirs = append(dirs, info)
	}
	return dirs, nil
}

// gcListDirs spawns a read-only pod that lists top-level directories on a PVC.
func gcListDirs(ctx context.Context, cs kubernetes.Interface, namespace, pvcName, mountPath string) ([]string, error) {
	podName := fmt.Sprintf("astn-gc-ls-%d", time.Now().UnixNano()%1000000)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{labelType: "gc-list"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "ls",
				Image:           "alpine:3.21",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sh", "-c", fmt.Sprintf("ls -1 %s 2>/dev/null || true", mountPath)},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "data", MountPath: mountPath, ReadOnly: true},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  true,
					},
				},
			}},
		},
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cs.CoreV1().Pods(namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	_ = cs.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	time.Sleep(500 * time.Millisecond)

	if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create list pod: %w", err)
	}

	if err := gcWaitForPodDone(ctx, cs, namespace, podName, 60*time.Second); err != nil {
		return nil, err
	}

	// Read logs.
	logReq := cs.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	logStream, err := logReq.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("read list pod logs: %w", err)
	}
	defer logStream.Close()

	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, readErr := logStream.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if readErr != nil {
			break
		}
	}

	var dirs []string
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs, nil
}

// gcDeleteDir spawns a pod that deletes a single directory from a PVC.
func gcDeleteDir(ctx context.Context, cs kubernetes.Interface, namespace, pvcName, mountPath, dir string) error {
	return gcDeleteDirs(ctx, cs, namespace, pvcName, mountPath, []string{dir})
}

func safeGCDirName(dir string) bool {
	return dir != "" && dir != "." && dir != ".." && !strings.Contains(dir, "/") && !strings.Contains(dir, "..")
}

// gcDeleteDirs spawns a pod that deletes multiple directories from a PVC.
func gcDeleteDirs(ctx context.Context, cs kubernetes.Interface, namespace, pvcName, mountPath string, dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nset -e\n")
	for _, d := range dirs {
		if !safeGCDirName(d) {
			continue
		}
		sb.WriteString(fmt.Sprintf("rm -rf %s/%s\n", shellQuote(mountPath), shellQuote(d)))
	}

	podName := fmt.Sprintf("astn-gc-rm-%d", time.Now().UnixNano()%1000000)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{labelType: "gc-rm"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:            "rm",
				Image:           "alpine:3.21",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"/bin/sh", "-c", sb.String()},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "data", MountPath: mountPath},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			}},
		},
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cs.CoreV1().Pods(namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	}()

	_ = cs.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	time.Sleep(500 * time.Millisecond)

	if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create gc-rm pod: %w", err)
	}

	return gcWaitForPodDone(ctx, cs, namespace, podName, 120*time.Second)
}

// gcWaitForPodDone polls until a pod reaches Succeeded or Failed.
func gcWaitForPodDone(ctx context.Context, cs kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("gc pod %s did not complete within %s", podName, timeout)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("gc pod %s disappeared", podName)
			}
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			return fmt.Errorf("gc pod %s failed", podName)
		}
		time.Sleep(2 * time.Second)
	}
}
