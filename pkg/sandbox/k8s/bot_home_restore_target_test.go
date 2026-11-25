package k8s

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	kt "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
)

var _ interface {
	sandbox.BotHomeFilesBackend
	sandbox.BotHomeStreamArchiveBackend
	CreateBotHomeRestoreTarget(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error)
} = (*K8sBackend)(nil)

func TestK8sRestoreGenerationArchiveAdmission(t *testing.T) {
	b, source := archiveFixture(t)
	h, err := b.CreateBotHomeRestoreTarget(t.Context(), source.Scope)
	if err != nil {
		t.Fatal(err)
	}
	m := &sandbox.BotHomeArchiveManifest{Version: 1, Binding: source.Scope.Key()}
	called := 0
	b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
		called++
		command := u.Query()["command"]
		if !strings.Contains(command[2], "EXPECTED_OWNER="+shellQuote(botHomeStorageOwner(h))) || !strings.Contains(command[2], "test ! -e "+shellQuote(mountBotHome+"/.owner")) {
			t.Fatal("missing exact target initialization", command)
		}
		return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
			var q struct {
				Op, Binding string
				Manifest    *sandbox.BotHomeArchiveManifest
			}
			reader := bufio.NewReader(o.Stdin)
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return err
			}
			if err := json.Unmarshal(line, &q); err != nil {
				return err
			}
			if q.Op != "restore" || q.Binding != source.Scope.Key() || q.Manifest.Binding != source.Scope.Key() {
				t.Error("logical archive identities changed")
			}
			if _, err := io.WriteString(o.Stdout, "{\"ready\":true}\n{\"commit\":true}\n"); err != nil {
				return err
			}
			buf := make([]byte, len("commit\n"))
			if _, err := io.ReadFull(reader, buf); err != nil {
				return err
			}
			if string(buf) != "commit\n" {
				t.Error("missing commit barrier")
			}
			_, err = io.WriteString(o.Stdout, "{\"restored\":true}\n")
			return err
		}), nil
	}
	if err := b.RestoreBotHomeArchive(t.Context(), source, m, bytes.NewReader(nil), archiveAllow); err == nil || called != 0 {
		t.Fatal("accepted source as target")
	}
	if err := b.RestoreBotHomeArchive(t.Context(), h, m, bytes.NewReader(nil), archiveAllow); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatal("archive operation replayed", called)
	}
	for _, a := range b.client.(*fake.Clientset).Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			pod := a.(kt.CreateAction).GetObject().(*corev1.Pod)
			if pod.Spec.Containers[0].VolumeMounts[0].SubPath != botHomeStorageSubPath(h) || !strings.HasPrefix(pod.Name, "astn-home-archive-") {
				t.Fatal("archive attached source or started session")
			}
		}
	}
	purge := b.botHomePurgePod(h, botHomePurge{Name: "cleanup", Image: b.cfg.SandboxImage})
	if !strings.Contains(purge.Spec.Containers[0].Command[2], `find -P "$root/workspace-private" -xdev -depth -delete`) {
		t.Fatal("generation cleanup leaves private objects")
	}
}

func TestK8sRestoreGenerationIdentity(t *testing.T) {
	b, source := homeBackend(t)
	client := b.client.(*fake.Clientset)
	first, err := b.CreateBotHomeRestoreTarget(t.Context(), source.Scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.CreateBotHomeRestoreTarget(t.Context(), source.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scope != source.Scope || second.Scope != source.Scope {
		t.Fatal("changed logical ownership")
	}
	for _, h := range []*sandbox.BotHomeBinding{source, first, second} {
		if err := b.validateBotHome(h); err != nil {
			t.Fatal(err)
		}
	}
	if first.Equal(second) || first.Equal(source) || botHomeStorageSubPath(first) == botHomeStorageSubPath(second) || strings.Contains(botHomeStorageSubPath(first), "/") {
		t.Fatal("not an isolated sibling")
	}
	if botHomeStorageSubPath(source) != source.Scope.Key() || botHomeStorageOwner(source) != source.Ref {
		t.Fatal("legacy layout changed")
	}
	for _, h := range []*sandbox.BotHomeBinding{first, second} {
		owner := botHomeStorageOwner(h)
		if len(strings.TrimPrefix(owner, botHomeRefPrefix)) != 64 {
			t.Fatal("incompatible compiled owner")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(owner, botHomeRefPrefix)); err != nil {
			t.Fatal(err)
		}
		cm, err := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(h), metav1.GetOptions{})
		var stored sandbox.BotHomeBinding
		if err != nil || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil || !h.Equal(&stored) || cm.Data["state"] != string(botHomeActive) || cm.Data["reservations"] != "{}" {
			t.Fatalf("fence mismatch: %v", err)
		}
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "pods" {
			t.Fatal("allocation started compute")
		}
	}
	again, err := b.EnsureBotHome(t.Context(), source.Scope)
	if err != nil || !again.Equal(source) {
		t.Fatal("source selection changed", err)
	}
	for _, suffix := range []string{"", "../x", "00000000-0000-0000-0000-000000000000", "12345678-1234-1234-1234-123456789ABc", "12345678123412341234123456789abc"} {
		bad := source.Clone()
		bad.Ref += botHomeRestoreSeparator + suffix
		if b.validateBotHome(bad) == nil {
			t.Fatalf("accepted alias %q", suffix)
		}
	}
	bad := first.Clone()
	bad.Scope.UserID += "other"
	if b.validateBotHome(bad) == nil {
		t.Fatal("cross owner accepted")
	}
	oldNS := b.cfg.Namespace
	b.cfg.Namespace += "other"
	if b.validateBotHome(first) == nil {
		t.Fatal("cross namespace accepted")
	}
	b.cfg.Namespace = oldNS
	b.cfg.HomesPVCName += "other"
	if b.validateBotHome(first) == nil {
		t.Fatal("cross claim accepted")
	}
}

func TestK8sRestoreGenerationJournalFailures(t *testing.T) {
	for _, mode := range []string{"deny", "cancel", "ambiguous", "exists", "mutate"} {
		t.Run(mode, func(t *testing.T) {
			b, source := homeBackend(t)
			client := b.client.(*fake.Clientset)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var recorded *sandbox.BotHomeBinding
			creates := 0
			client.PrependReactor("create", "configmaps", func(a kt.Action) (bool, runtime.Object, error) {
				creates++
				cm := a.(kt.CreateAction).GetObject().(*corev1.ConfigMap)
				if recorded == nil || cm.Name != botHomeFenceName(recorded) {
					t.Fatal("create before exact journal")
				}
				if mode == "ambiguous" {
					if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("configmaps"), cm, b.cfg.Namespace); err != nil {
						t.Fatal(err)
					}
					return true, nil, errors.New("lost create response")
				}
				if mode == "exists" {
					return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, cm.Name)
				}
				return false, nil, nil
			})
			h, err := b.CreateBotHomeRestoreTargetJournaled(ctx, source.Scope, func(h *sandbox.BotHomeBinding) error {
				recorded = h.Clone()
				switch mode {
				case "deny":
					return errors.New("journal denied")
				case "cancel":
					cancel()
				case "mutate":
					h.Ref = source.Ref
				}
				return nil
			})
			if mode == "mutate" {
				if err != nil || !h.Equal(recorded) {
					t.Fatal("callback changed reserved target", err)
				}
			} else if err == nil || h != nil {
				t.Fatal("failed reservation succeeded")
			}
			if mode == "deny" || mode == "cancel" {
				if creates != 0 {
					t.Fatal("allocated without journal")
				}
			} else if creates != 1 {
				t.Fatal("create replayed", creates)
			}
			if mode == "ambiguous" {
				cm, e := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(recorded), metav1.GetOptions{})
				if e != nil || cm.Data["state"] != string(botHomeActive) {
					t.Fatal("lost exact cleanup target", e)
				}
			}
		})
	}
}

func TestK8sRestoreGenerationMountsAndFences(t *testing.T) {
	b, source := homeBackend(t)
	h, err := b.CreateBotHomeRestoreTarget(t.Context(), source.Scope)
	if err != nil {
		t.Fatal(err)
	}
	pod, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "restore-generation", BotHome: h})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.validatePodBotHome(pod, h); err != nil {
		t.Fatal(err)
	}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, mount := range c.VolumeMounts {
			if mount.Name == volumeBotHome && mount.SubPath != botHomeStorageSubPath(h) && mount.SubPath != botHomeStorageSubPath(h)+"/data" {
				t.Fatal("wrong physical generation", mount)
			}
		}
	}
	if pod.Spec.InitContainers[0].Env[0].Value != botHomeStorageOwner(h) {
		t.Fatal("initializer owner mismatch")
	}
	wrong := pod.DeepCopy()
	for i := range wrong.Spec.Containers[0].VolumeMounts {
		if wrong.Spec.Containers[0].VolumeMounts[i].Name == volumeBotHome {
			wrong.Spec.Containers[0].VolumeMounts[i].SubPath = source.Scope.Key()
		}
	}
	if b.validatePodBotHome(wrong, h) == nil {
		t.Fatal("source mount accepted")
	}
	helper := b.botWorkspacePod(h, "restore-helper")
	purge := b.botHomePurgePod(h, botHomePurge{Name: "restore-purge", Image: b.cfg.SandboxImage})
	if helper.Spec.Containers[0].VolumeMounts[0].SubPath != botHomeStorageSubPath(h) || purge.Spec.Containers[0].VolumeMounts[0].SubPath != botHomeStorageSubPath(h) || purge.Spec.Containers[0].Command[4] != botHomeStorageOwner(h) {
		t.Fatal("offline or cleanup targets source")
	}
	if !strings.Contains(botHomeFilesCommand(h, mountBotHomeScope, true, false)[2], shellQuote(botHomeStorageOwner(h))) {
		t.Fatal("file owner mismatch")
	}
	if err := b.archiveFence(t.Context(), h, "restore-op", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.reserveBotHome(t.Context(), h, "target-writer"); err == nil {
		t.Fatal("target admitted concurrent writer")
	}
	if _, err := b.reserveBotHome(t.Context(), source, "source-writer"); err != nil {
		t.Fatal("generation fence changed source", err)
	}
	cm, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(h), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data["state"] != string(botHomeArchiving) {
		t.Fatal("lost exclusive generation fence")
	}
	// Immutable session recovery must retain the physical generation, not Ensure's source.
	stored, err := b.sessionBotHome(t.Context(), "restore-binding", h, false)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := b.sessionBotHome(t.Context(), "restore-binding", nil, true)
	if err != nil || !reflect.DeepEqual(stored, recovered) || !h.Equal(recovered) {
		t.Fatal("generation recovery changed", err)
	}
}
