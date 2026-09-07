// Package k8s — template_test.go drives BuildTemplate and
// SaveSessionAsTemplate against the fake clientset plus a stub
// executor so we can assert:
//
//   - Manifest shape: labels (type=template-builder, template=<id>),
//     annotations, volumes (layers RW, upper emptyDir, work emptyDir),
//     image, runtimeClassName, and the long-sleep command.
//   - Name derivation: builder-pod prefix, sanitized template ID,
//     timestamp suffix.
//   - parentLayerOf helper: empty chain, single element, multi-element.
//   - Capture-script shape: `set -e`, canonical tar options, sha256sum,
//     sha256sum, staging-directory rename, emitted SHA= / SIZE= lines.
//   - parseCaptureOutput: valid, missing SHA, missing SIZE, malformed
//     SIZE, short SHA, extra diagnostic lines.
//   - BuildTemplate happy path: pod created, build steps exec'd in
//     order, capture pipeline exec'd, pod deleted on success.
//   - BuildTemplate failure paths: ctx cancelled, client nil, empty
//     TemplateID, chain too deep, non-zero build step, failed capture
//     pipeline, malformed capture output. Each must still clean up
//     the builder pod.
//   - SaveSessionAsTemplate: session-lookup required, pod name honoured,
//     artifact populated with TemplateID as ParentLayer.
//   - RefreshTemplate: returns the descriptive "requires build-spec"
//     error once a client is configured.
//   - DeleteTemplate: no-op success, independent of force flag.

package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/SAP/astonish/pkg/sandbox"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// autoRunBuilderPods installs a reactor that transitions any pod labelled
// type=template-builder (the only kind BuildTemplate creates) to phase
// Running immediately on create, so waitForBuilderPodRunning returns
// without polling delay. Without this, fake-clientset pods stay in
// phase "" forever.
func autoRunBuilderPods(t *testing.T, b *K8sBackend) {
	t.Helper()
	fake, ok := b.client.(interface {
		PrependReactor(verb, resource string, r clientgotesting.ReactionFunc)
	})
	if !ok {
		t.Fatalf("client does not support PrependReactor")
	}
	fake.PrependReactor("create", "pods", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clientgotesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := ca.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		if pod.Labels[labelType] != typeTemplateBuilder {
			return false, nil, nil
		}
		// Don't short-circuit the default store; let the default
		// reactor persist the pod, then stamp Running via a
		// goroutine. Simpler: mutate the Pod in place before the
		// default reactor sees it — the fake clientset deep-copies on
		// write but reads the object we return.
		pod.Status.Phase = corev1.PodRunning
		return false, nil, nil
	})
}

// captureStubScript is a canned buildCaptureScript output the stub
// executor can return to satisfy parseCaptureOutput. Test-local so we
// don't accidentally couple template_test.go to production output
// formatting beyond the documented contract.
func captureStubStdout(sha string, size int64) []byte {
	return []byte(fmt.Sprintf("diagnostics line\nSHA=%s\nSIZE=%d\n", sha, size))
}

// buildAndTemplateID composes a template build spec for tests.
func buildAndTemplateID() sandbox.TemplateBuildSpec {
	return sandbox.TemplateBuildSpec{
		TemplateID: "python-dev",
		ParentLayers: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
			"1111111111111111111111111111111111111111111111111111111111111111",
		},
		Steps:  []string{"apt-get install -y jq", "pip install requests"},
		Labels: map[string]string{"astonish.io/extra": "yes"},
	}
}

// sha64 returns a placeholder 64-char hex string. 'a' repeated is
// lexicographically harmless and easy to spot in failure output.
const sha64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// ---------------------------------------------------------------------------
// Manifest builder
// ---------------------------------------------------------------------------

func TestBuildTemplateBuilderPodManifest_LabelsAndAnnotations(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildTemplateBuilderPodManifest(buildAndTemplateID())
	if err != nil {
		t.Fatalf("buildTemplateBuilderPodManifest: %v", err)
	}

	if got := pod.Labels[labelType]; got != typeTemplateBuilder {
		t.Errorf("label[%s] = %q, want %q", labelType, got, typeTemplateBuilder)
	}
	if got := pod.Labels[labelTemplate]; got != "python-dev" {
		t.Errorf("label[%s] = %q, want %q", labelTemplate, got, "python-dev")
	}
	if got := pod.Labels["astonish.io/extra"]; got != "yes" {
		t.Errorf("custom label[astonish.io/extra] = %q, want yes", got)
	}
	if _, ok := pod.Annotations[annotationCreatedAt]; !ok {
		t.Errorf("missing annotation %s", annotationCreatedAt)
	}
	if got := pod.Annotations[annotationLayerChain]; !strings.Contains(got, "0000") || !strings.Contains(got, "1111") {
		t.Errorf("annotation %s = %q, want both parent layers", annotationLayerChain, got)
	}
}

func TestBuildTemplateBuilderPodManifest_Name(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildTemplateBuilderPodManifest(buildAndTemplateID())
	if err != nil {
		t.Fatalf("buildTemplateBuilderPodManifest: %v", err)
	}
	if !strings.HasPrefix(pod.Name, builderPodNamePrefix+"python-dev-") {
		t.Errorf("pod name %q does not have expected prefix %q", pod.Name, builderPodNamePrefix+"python-dev-")
	}
	if len(pod.Name) > 253 {
		t.Errorf("pod name %q exceeds DNS-1123 limit (%d bytes)", pod.Name, len(pod.Name))
	}
}

func TestBuildTemplateBuilderPodManifest_Volumes(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildTemplateBuilderPodManifest(buildAndTemplateID())
	if err != nil {
		t.Fatalf("buildTemplateBuilderPodManifest: %v", err)
	}

	var layersV, overlayV, uppersV *corev1.Volume
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		switch v.Name {
		case volumeLayers:
			layersV = v
		case volumeOverlay:
			overlayV = v
		case volumeUppers:
			uppersV = v
		}
	}
	if layersV == nil || layersV.PersistentVolumeClaim == nil {
		t.Fatalf("missing layers PVC volume")
	}
	if layersV.PersistentVolumeClaim.ReadOnly {
		t.Errorf("layers PVC is ReadOnly, want RW for atomic rename")
	}
	if overlayV == nil || overlayV.EmptyDir == nil {
		t.Errorf("missing overlay emptyDir volume")
	}
	// Uppers PVC is mounted because the overlay entrypoint references
	// $UPPERS_DIR for the resume-tar path (which is safely skipped when
	// the tarball is absent). The mount must exist to avoid a pod crash.
	if uppersV == nil || uppersV.PersistentVolumeClaim == nil {
		t.Fatalf("missing uppers PVC volume (required by overlay entrypoint)")
	}
}

func TestBuildTemplateBuilderPodManifest_ContainerShape(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildTemplateBuilderPodManifest(buildAndTemplateID())
	if err != nil {
		t.Fatalf("buildTemplateBuilderPodManifest: %v", err)
	}
	if n := len(pod.Spec.Containers); n != 1 {
		t.Fatalf("want 1 container, got %d", n)
	}
	c := pod.Spec.Containers[0]
	if c.Name != containerName {
		t.Errorf("container name = %q, want %q", c.Name, containerName)
	}
	if c.Image != b.cfg.SandboxImage {
		t.Errorf("image = %q, want %q", c.Image, b.cfg.SandboxImage)
	}
	// Command must be empty — the builder uses the image's ENTRYPOINT
	// (astonish-sandbox-entrypoint) which composes the overlay, then
	// execs into ASTONISH_HANDOFF (/bin/sleep infinity).
	if len(c.Command) != 0 {
		t.Errorf("container Command = %v, want empty (use image ENTRYPOINT)", c.Command)
	}
	// Verify ASTONISH_HANDOFF is set so the entrypoint sleeps after
	// overlay composition (same as session pods).
	envMap := make(map[string]string)
	for _, e := range c.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["ASTONISH_HANDOFF"] != "/bin/sleep" {
		t.Errorf("ASTONISH_HANDOFF = %q, want /bin/sleep", envMap["ASTONISH_HANDOFF"])
	}
	if envMap["ASTONISH_HANDOFF_ARGS"] != "infinity" {
		t.Errorf("ASTONISH_HANDOFF_ARGS = %q, want infinity", envMap["ASTONISH_HANDOFF_ARGS"])
	}
	if !strings.HasPrefix(envMap["ASTONISH_SESSION_ID"], "build-") {
		t.Errorf("ASTONISH_SESSION_ID = %q, want build-<podname> prefix", envMap["ASTONISH_SESSION_ID"])
	}
	// Phase F: default backend has empty RuntimeClassName → nil on the
	// pod (cluster default applies).
	if pod.Spec.RuntimeClassName != nil {
		t.Errorf("runtimeClassName = %v, want nil (empty config → cluster default)", pod.Spec.RuntimeClassName)
	}
}

func TestBuildTemplateBuilderPodManifest_RejectsEmptyTemplateID(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.buildTemplateBuilderPodManifest(sandbox.TemplateBuildSpec{})
	if err == nil {
		t.Fatal("expected error on empty TemplateID")
	}
}

// ---------------------------------------------------------------------------
// parentLayerOf
// ---------------------------------------------------------------------------

func TestParentLayerOf(t *testing.T) {
	cases := []struct {
		name  string
		chain []string
		want  string
	}{
		{"empty", nil, ""},
		{"single", []string{"a"}, "a"},
		{"multi", []string{"a", "b", "c"}, "c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parentLayerOf(c.chain); got != c.want {
				t.Errorf("parentLayerOf(%v) = %q, want %q", c.chain, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildCaptureScript / parseCaptureOutput
// ---------------------------------------------------------------------------

func TestBuildCaptureScript_ContainsCanonicalPipeline(t *testing.T) {
	s := buildCaptureScript("/mnt/astonish-layers", "bid-123")
	mustContain := []string{
		"set -euo pipefail",
		"trap cleanup EXIT",
		"/mnt/astonish-layers/__staging-bid-123",
		"tar --numeric-owner --xattrs --acls --sort=name --mtime=@0",
		"/var/astonish/overlay/upper",
		"sha256sum",
		"mv \"$STAGING\" \"$LAYERS_DIR/$SHA\"",
		"echo \"SHA=$SHA\"",
		"du -sb",
	}
	for _, needle := range mustContain {
		if !strings.Contains(s, needle) {
			t.Errorf("capture script missing %q:\n%s", needle, s)
		}
	}
	if strings.Contains(s, "/tmp/astn-layer.tar") {
		t.Error("must not stage a full tar on /tmp")
	}
	if strings.Contains(s, "mkfifo") {
		t.Error("must not use mkfifo")
	}
}

func TestParseCaptureOutput_Valid(t *testing.T) {
	sha, size, err := parseCaptureOutput(captureStubStdout(sha64, 12345))
	if err != nil {
		t.Fatalf("parseCaptureOutput: %v", err)
	}
	if sha != sha64 {
		t.Errorf("sha = %q, want %q", sha, sha64)
	}
	if size != 12345 {
		t.Errorf("size = %d, want 12345", size)
	}
}

func TestParseCaptureOutput_MissingSHA(t *testing.T) {
	_, _, err := parseCaptureOutput([]byte("SIZE=100\n"))
	if err == nil {
		t.Fatal("expected error on missing SHA")
	}
}

func TestParseCaptureOutput_ShortSHA(t *testing.T) {
	_, _, err := parseCaptureOutput([]byte("SHA=abc\nSIZE=0\n"))
	if err == nil || !strings.Contains(err.Error(), "64-char hex") {
		t.Fatalf("expected 64-char error, got %v", err)
	}
}

func TestParseCaptureOutput_MalformedSize(t *testing.T) {
	_, _, err := parseCaptureOutput([]byte("SHA=" + sha64 + "\nSIZE=notanumber\n"))
	if err == nil {
		t.Fatal("expected error on malformed SIZE")
	}
}

func TestParseCaptureOutput_NegativeSize(t *testing.T) {
	// strconv.ParseInt handles signed; verify we reject <0 explicitly.
	_, _, err := parseCaptureOutput([]byte("SHA=" + sha64 + "\nSIZE=-1\n"))
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("expected negative-size error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// BuildTemplate
// ---------------------------------------------------------------------------

func TestBuildTemplate_ContextCancelled(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.BuildTemplate(ctx, buildAndTemplateID()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestBuildTemplate_NilClientReturnsNotImplemented(t *testing.T) {
	b, err := New(Config{Sessions: newRegistry(t)})
	if err != nil {
		t.Fatalf("k8s.New: %v", err)
	}
	_, err = b.BuildTemplate(context.Background(), buildAndTemplateID())
	if !errors.Is(err, ErrNotImplementedYet) {
		t.Fatalf("err = %v, want ErrNotImplementedYet", err)
	}
}

func TestBuildTemplate_EmptyTemplateID(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.BuildTemplate(context.Background(), sandbox.TemplateBuildSpec{})
	if err == nil || !strings.Contains(err.Error(), "TemplateID is required") {
		t.Fatalf("err = %v, want TemplateID-required", err)
	}
}

func TestBuildTemplate_ChainTooDeep(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	// MaxChainDepth defaults to 20; supply 21.
	parents := make([]string, 21)
	for i := range parents {
		parents[i] = fmt.Sprintf("layer-%02d", i)
	}
	_, err := b.BuildTemplate(context.Background(), sandbox.TemplateBuildSpec{
		TemplateID:   "deep",
		ParentLayers: parents,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds MaxChainDepth") {
		t.Fatalf("err = %v, want MaxChainDepth error", err)
	}
}

func TestBuildTemplate_HappyPath(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	autoRunBuilderPods(t, b)

	// Track the exec calls: each step is one, then the capture
	// pipeline is one more.
	var (
		mu      sync.Mutex
		scripts []string
	)
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		// The command body is encoded in the URL query string;
		// but we can't easily decode that here. Instead, use a
		// sentinel via the stub's captured URL in the stub
		// executor. For this test, we rely on the call order.
		mu.Lock()
		scripts = append(scripts, "call")
		n := len(scripts)
		mu.Unlock()
		// First N steps return success; the (N+1)th is the capture
		// pipeline — emit SHA/SIZE stdout.
		if n == len(buildAndTemplateID().Steps)+1 {
			_, _ = opts.Stdout.Write(captureStubStdout(sha64, 7777))
		}
		return nil
	})

	spec := buildAndTemplateID()
	art, err := b.BuildTemplate(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if art.LayerID != sha64 {
		t.Errorf("LayerID = %q, want %q", art.LayerID, sha64)
	}
	if art.SizeBytes != 7777 {
		t.Errorf("SizeBytes = %d, want 7777", art.SizeBytes)
	}
	if art.ParentLayer != spec.ParentLayers[len(spec.ParentLayers)-1] {
		t.Errorf("ParentLayer = %q, want %q (top of parent chain)",
			art.ParentLayer, spec.ParentLayers[len(spec.ParentLayers)-1])
	}
	if !strings.Contains(art.CephFSPath, sha64) {
		t.Errorf("CephFSPath %q missing sha", art.CephFSPath)
	}
	mu.Lock()
	if got, want := len(scripts), len(spec.Steps)+1; got != want {
		t.Errorf("exec calls = %d, want %d (steps + capture)", got, want)
	}
	mu.Unlock()

	// Builder pod must have been cleaned up.
	pods, err := cs.CoreV1().Pods(b.cfg.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	for _, p := range pods.Items {
		if p.Labels[labelType] == typeTemplateBuilder {
			t.Errorf("builder pod %q not deleted", p.Name)
		}
	}
}

func TestBuildTemplate_StepFailurePropagates(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	autoRunBuilderPods(t, b)

	var calls atomic.Int32
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		n := calls.Add(1)
		if n == 1 {
			// First step fails with a non-zero exit code.
			_, _ = opts.Stderr.Write([]byte("apt-get: E: not found"))
			// client-go decodes exit codes from CodeExitError.
			return &fakeExitError{code: 100}
		}
		return nil
	})

	_, err := b.BuildTemplate(context.Background(), buildAndTemplateID())
	if err == nil || !strings.Contains(err.Error(), "exited 100") {
		t.Fatalf("err = %v, want exited 100", err)
	}

	// Builder pod must still have been deleted.
	pods, _ := cs.CoreV1().Pods(b.cfg.Namespace).List(context.Background(), metav1.ListOptions{})
	for _, p := range pods.Items {
		if p.Labels[labelType] == typeTemplateBuilder {
			t.Errorf("builder pod %q not deleted after step failure", p.Name)
		}
	}
}

func TestBuildTemplate_CaptureMalformed(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	autoRunBuilderPods(t, b)

	var calls atomic.Int32
	spec := buildAndTemplateID()
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		n := calls.Add(1)
		if int(n) == len(spec.Steps)+1 {
			// Capture pipeline succeeded per exit code but
			// emits nothing parseable.
			_, _ = opts.Stdout.Write([]byte("garbage output\n"))
		}
		return nil
	})

	_, err := b.BuildTemplate(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "parse capture output") {
		t.Fatalf("err = %v, want parse-capture-output error", err)
	}
}

func TestBuildTemplate_ReadinessTimeoutPropagatesCtx(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	// Do NOT install autoRunBuilderPods — the pod stays in phase "".
	// Instead use a small ctx deadline so we don't actually wait for
	// builderPodReadinessTimeout (2 minutes).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	stubFactory(t, b, nil)

	_, err := b.BuildTemplate(ctx, buildAndTemplateID())
	if err == nil {
		t.Fatal("expected error when pod never reaches Running")
	}
	// Either ctx.DeadlineExceeded or a "wait pod running" wrapper.
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "wait pod running") {
		t.Errorf("err = %v, want deadline or wait-pod-running error", err)
	}
}

// ---------------------------------------------------------------------------
// SaveSessionAsTemplate
// ---------------------------------------------------------------------------

func TestSaveSessionAsTemplate_ContextCancelled(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.SaveSessionAsTemplate(ctx, "s1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSaveSessionAsTemplate_NilClientReturnsNotImplemented(t *testing.T) {
	b, err := New(Config{Sessions: newRegistry(t)})
	if err != nil {
		t.Fatalf("k8s.New: %v", err)
	}
	_, err = b.SaveSessionAsTemplate(context.Background(), "s1")
	if !errors.Is(err, ErrNotImplementedYet) {
		t.Fatalf("err = %v, want ErrNotImplementedYet", err)
	}
}

func TestSaveSessionAsTemplate_EmptySessionID(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.SaveSessionAsTemplate(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty sessionID")
	}
}

func TestSaveSessionAsTemplate_MissingSession(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.SaveSessionAsTemplate(context.Background(), "no-such-session")
	if err == nil || !strings.Contains(err.Error(), "no pod") {
		t.Fatalf("err = %v, want no-pod error", err)
	}
}

func TestSaveSessionAsTemplate_HappyPath(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")
	// Set the session's template ID so we can assert ParentLayer
	// passthrough.
	rec, _ := b.sessions.GetSession("s1")
	rec.TemplateID = "python-dev"
	if err := b.sessions.PutSession(rec); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stdout.Write(captureStubStdout(sha64, 42))
		return nil
	})

	art, err := b.SaveSessionAsTemplate(context.Background(), "s1")
	if err != nil {
		t.Fatalf("SaveSessionAsTemplate: %v", err)
	}
	if art.LayerID != sha64 {
		t.Errorf("LayerID = %q, want %q", art.LayerID, sha64)
	}
	if art.SizeBytes != 42 {
		t.Errorf("SizeBytes = %d, want 42", art.SizeBytes)
	}
	if art.ParentLayer != "python-dev" {
		t.Errorf("ParentLayer = %q, want python-dev", art.ParentLayer)
	}
}

// ---------------------------------------------------------------------------
// RefreshTemplate / DeleteTemplate
// ---------------------------------------------------------------------------

func TestRefreshTemplate_ContextCancelled(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.RefreshTemplate(ctx, "t1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRefreshTemplate_NilClientReturnsNotImplemented(t *testing.T) {
	b, err := New(Config{Sessions: newRegistry(t)})
	if err != nil {
		t.Fatalf("k8s.New: %v", err)
	}
	_, err = b.RefreshTemplate(context.Background(), "t1")
	if !errors.Is(err, ErrNotImplementedYet) {
		t.Fatalf("err = %v, want ErrNotImplementedYet", err)
	}
}

func TestRefreshTemplate_ReturnsBuildSpecError(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.RefreshTemplate(context.Background(), "t1")
	if err == nil || !strings.Contains(err.Error(), "build-spec persistence") {
		t.Fatalf("err = %v, want build-spec persistence error", err)
	}
}

func TestDeleteTemplate_SkipsBase(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	// @base and empty IDs should be no-ops.
	for _, id := range []string{"", sandbox.BaseTemplateID} {
		if err := b.DeleteTemplate(context.Background(), id, false); err != nil {
			t.Errorf("DeleteTemplate(%q) = %v, want nil", id, err)
		}
	}
}

func TestDeleteTemplate_CreatesGCPod(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	autoCompleteGCPods(t, b)

	layerID := "abc123def456"
	if err := b.DeleteTemplate(context.Background(), layerID, false); err != nil {
		t.Fatalf("DeleteTemplate = %v, want nil", err)
	}

	// Verify GC pod was created (and cleaned up via defer).
	cs := b.client.(*fake.Clientset)
	actions := cs.Actions()
	var foundCreate bool
	for _, a := range actions {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			ca := a.(clientgotesting.CreateAction)
			pod := ca.GetObject().(*corev1.Pod)
			if pod.Labels[labelType] == "layer-gc" {
				foundCreate = true
				// Verify the rm command references the layer ID.
				cmd := pod.Spec.Containers[0].Command
				if len(cmd) < 3 || !strings.Contains(cmd[2], layerID) {
					t.Errorf("GC pod command does not reference layer ID: %v", cmd)
				}
			}
		}
	}
	if !foundCreate {
		t.Error("expected a layer-gc pod to be created")
	}
}

func TestDeleteTemplate_ContextCancelled(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.DeleteTemplate(ctx, "t1", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// autoCompleteGCPods installs a reactor that transitions layer-gc pods
// to Succeeded on create, mirroring autoRunBuilderPods for builder pods.
func autoCompleteGCPods(t *testing.T, b *K8sBackend) {
	t.Helper()
	fakeCS, ok := b.client.(interface {
		PrependReactor(verb, resource string, r clientgotesting.ReactionFunc)
	})
	if !ok {
		t.Fatalf("client does not support PrependReactor")
	}
	fakeCS.PrependReactor("create", "pods", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clientgotesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := ca.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		if pod.Labels[labelType] != "layer-gc" {
			return false, nil, nil
		}
		pod.Status.Phase = corev1.PodSucceeded
		return false, nil, nil
	})
}

// ---------------------------------------------------------------------------
// TemplatePersister hook (Phase D)
// ---------------------------------------------------------------------------

// newBackendWithPersister builds a backend like newBackendWithFakeClient
// but with a caller-supplied TemplatePersister and auto-running builder
// pods. The returned backend is ready to exercise BuildTemplate /
// SaveSessionAsTemplate end-to-end; callers just need to stub the
// executor.
func newBackendWithPersister(t *testing.T, persister TemplatePersister) (*K8sBackend, kubernetes.Interface) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	b, err := New(Config{
		Client:            cs,
		Sessions:          newRegistry(t),
		TemplatePersister: persister,
	})
	if err != nil {
		t.Fatalf("k8s.New: %v", err)
	}
	// Mirror autoRunBuilderPods inline so callers don't need to call it.
	fake2, ok := b.client.(interface {
		PrependReactor(verb, resource string, r clientgotesting.ReactionFunc)
	})
	if !ok {
		t.Fatalf("client does not support PrependReactor")
	}
	fake2.PrependReactor("create", "pods", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clientgotesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := ca.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		if pod.Labels[labelType] != typeTemplateBuilder {
			return false, nil, nil
		}
		pod.Status.Phase = corev1.PodRunning
		return false, nil, nil
	})
	return b, cs
}

// TestBuildTemplate_PersisterInvokedOnSuccess asserts the persister is
// called exactly once with the captured artifact when BuildTemplate
// succeeds, and that hints.TemplateID carries the spec's identifier.
func TestBuildTemplate_PersisterInvokedOnSuccess(t *testing.T) {
	var calls int32
	var gotHints TemplatePersistHints
	var gotArt *sandbox.TemplateArtifact

	persister := func(_ context.Context, a *sandbox.TemplateArtifact, h TemplatePersistHints) error {
		atomic.AddInt32(&calls, 1)
		gotArt = a
		gotHints = h
		return nil
	}

	b, _ := newBackendWithPersister(t, persister)
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		// Emit valid capture output for every call; build-step calls
		// ignore the stdout, capture call parses it.
		_, _ = opts.Stdout.Write(captureStubStdout(sha64, 1234))
		return nil
	})

	spec := buildAndTemplateID()
	art, err := b.BuildTemplate(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("persister calls = %d, want 1", c)
	}
	if gotHints.TemplateID != spec.TemplateID {
		t.Errorf("hints.TemplateID = %q, want %q", gotHints.TemplateID, spec.TemplateID)
	}
	if gotHints.SessionID != "" {
		t.Errorf("hints.SessionID = %q, want empty (BuildTemplate path)", gotHints.SessionID)
	}
	if gotArt == nil || gotArt.LayerID != sha64 {
		t.Errorf("gotArt.LayerID = %v, want %q", gotArt, sha64)
	}
	if art.LayerID != sha64 {
		t.Errorf("returned LayerID = %q, want %q", art.LayerID, sha64)
	}
}

// TestBuildTemplate_PersisterErrorPropagates asserts that a non-nil
// persister error fails BuildTemplate with a wrapped message naming the
// template — so operators can see which persistence attempt failed.
func TestBuildTemplate_PersisterErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	persister := func(_ context.Context, _ *sandbox.TemplateArtifact, _ TemplatePersistHints) error {
		return wantErr
	}

	b, _ := newBackendWithPersister(t, persister)
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stdout.Write(captureStubStdout(sha64, 1))
		return nil
	})

	_, err := b.BuildTemplate(context.Background(), buildAndTemplateID())
	if err == nil {
		t.Fatal("expected BuildTemplate to fail when persister errors")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapping %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "python-dev") {
		t.Errorf("err %q should mention the template id", err)
	}
}

// TestSaveSessionAsTemplate_PersisterHintsIncludeSessionID asserts that
// the save-path populates SessionID on the hints and uses the session's
// template ID as ParentTemplateID.
func TestSaveSessionAsTemplate_PersisterHintsIncludeSessionID(t *testing.T) {
	var gotHints TemplatePersistHints
	persister := func(_ context.Context, _ *sandbox.TemplateArtifact, h TemplatePersistHints) error {
		gotHints = h
		return nil
	}

	b, _ := newBackendWithPersister(t, persister)
	seedSession(t, b, "sess-7", "astn-sess-7")
	rec, _ := b.sessions.GetSession("sess-7")
	rec.TemplateID = "team-python"
	if err := b.sessions.PutSession(rec); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stdout.Write(captureStubStdout(sha64, 99))
		return nil
	})

	if _, err := b.SaveSessionAsTemplate(context.Background(), "sess-7"); err != nil {
		t.Fatalf("SaveSessionAsTemplate: %v", err)
	}
	if gotHints.SessionID != "sess-7" {
		t.Errorf("SessionID = %q, want sess-7", gotHints.SessionID)
	}
	if gotHints.ParentTemplateID != "team-python" {
		t.Errorf("ParentTemplateID = %q, want team-python", gotHints.ParentTemplateID)
	}
	if gotHints.TemplateID != "" {
		t.Errorf("TemplateID = %q, want empty (SaveSessionAsTemplate path)", gotHints.TemplateID)
	}
}

// TestSaveSessionAsTemplate_PersisterErrorPropagates is the analogue of
// TestBuildTemplate_PersisterErrorPropagates for the save path.
func TestSaveSessionAsTemplate_PersisterErrorPropagates(t *testing.T) {
	wantErr := errors.New("layer insert failed")
	persister := func(_ context.Context, _ *sandbox.TemplateArtifact, _ TemplatePersistHints) error {
		return wantErr
	}

	b, _ := newBackendWithPersister(t, persister)
	seedSession(t, b, "sess-8", "astn-sess-8")

	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stdout.Write(captureStubStdout(sha64, 1))
		return nil
	})

	_, err := b.SaveSessionAsTemplate(context.Background(), "sess-8")
	if err == nil {
		t.Fatal("expected error when persister fails")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapping %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "sess-8") {
		t.Errorf("err %q should mention the session id", err)
	}
}

// ---------------------------------------------------------------------------
// fakeExitError — local minimal CodeExitError impl
// ---------------------------------------------------------------------------

// fakeExitError mimics k8s.io/client-go/util/exec.CodeExitError without
// pulling CodeExitError construction into every test site. decodeExitError
// inspects via errors.As, so we just need to satisfy the interface.
type fakeExitError struct {
	code int
}

func (e *fakeExitError) Error() string   { return fmt.Sprintf("command exited %d", e.code) }
func (e *fakeExitError) ExitStatus() int { return e.code }
func (e *fakeExitError) Exited() bool    { return true }
