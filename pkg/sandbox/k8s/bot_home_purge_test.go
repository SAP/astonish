package k8s

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func TestBotHomePurgeCapability(t *testing.T) {
	b, _ := homeBackend(t)
	if _, ok := any(b).(sandbox.BotHomeDeleteBackend); !ok {
		t.Fatal("missing retained-data purge capability")
	}
}

func purgeReady(t *testing.T, b *K8sBackend, h *sandbox.BotHomeBinding, failed bool) *int {
	t.Helper()
	if err := b.mutateBotHomeFence(context.Background(), h, func(botHomeFenceState, map[string]botHomeReservation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	n := new(int)
	b.client.(*fake.Clientset).PrependReactor("create", "pods", func(a kt.Action) (bool, runtime.Object, error) {
		*n++
		p := a.(kt.CreateAction).GetObject().(*corev1.Pod)
		obj, err := b.client.(*fake.Clientset).Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), b.cfg.Namespace, botHomeFenceName(h))
		if err != nil {
			t.Fatal(err)
		}
		if obj.(*corev1.ConfigMap).Data["state"] != string(botHomeDeleting) {
			t.Fatal("purge before fence")
		}
		if p.Spec.Containers[0].VolumeMounts[0].SubPath != h.Scope.Key() || len(p.Spec.Volumes) != 1 || len(p.Spec.InitContainers) != 0 {
			t.Fatal("unsafe purge mount")
		}
		p.UID = "purge-uid"
		p.Status.Phase = corev1.PodSucceeded
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
		if failed {
			p.Status.Phase = corev1.PodFailed
			p.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
		}
		return false, nil, nil
	})
	return n
}

func TestBotHomePurgeRetryAndTerminal(t *testing.T) {
	for _, mode := range []string{"success", "cleanup-error", "finalizer", "owner-mismatch", "ambiguous-create", "ambiguous-created"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			ctx := context.Background()
			client := b.client.(*fake.Clientset)
			n := purgeReady(t, b, h, mode == "owner-mismatch")
			blocked := true
			if mode == "cleanup-error" || mode == "finalizer" {
				client.PrependReactor("delete", "pods", func(kt.Action) (bool, runtime.Object, error) {
					if blocked {
						if mode == "finalizer" {
							return true, nil, nil
						}
						return true, nil, errors.New("denied")
					}
					return false, nil, nil
				})
			}
			if mode == "ambiguous-create" {
				client.PrependReactor("create", "pods", func(kt.Action) (bool, runtime.Object, error) {
					*n++
					return true, nil, errors.New("unknown create outcome")
				})
			}
			if mode == "ambiguous-created" {
				client.PrependReactor("create", "pods", func(a kt.Action) (bool, runtime.Object, error) {
					*n++
					p := a.(kt.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
					p.UID = "ambiguous-uid"
					p.Status.Phase = corev1.PodSucceeded
					p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
					if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), p, b.cfg.Namespace); err != nil {
						t.Fatal(err)
					}
					return true, nil, errors.New("response lost")
				})
			}
			err := b.DeleteBotHome(ctx, h)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("false success")
			}
			blocked = false
			other := sharedHomeBackend(t, b)
			err = other.DeleteBotHome(ctx, h)
			if mode == "owner-mismatch" || mode == "ambiguous-create" {
				if err == nil {
					t.Fatal("unproven purge succeeded")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if *n != 1 {
				t.Fatalf("submitted %d purge helpers", *n)
			}
			if _, err = other.EnsureBotHome(ctx, h.Scope); err == nil {
				t.Fatal("terminal home resurrected")
			}
			if _, err = other.reserveBotHome(ctx, h, "new"); err == nil {
				t.Fatal("terminal admission")
			}
		})
	}
}

func TestBotHomePurgeMissingFenceAndReservations(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	if _, err := b.reserveBotHome(ctx, h, "pending"); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteBotHome(ctx, h); err == nil {
		t.Fatal("purged reserved home")
	}
	if err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Delete(ctx, botHomeFenceName(h), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteBotHome(ctx, h); err == nil {
		t.Fatal("synthesized missing fence")
	}
}

func TestBotHomePurgeAdmissionRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		b, h := homeBackend(t)
		enforceHomeCAS(b)
		purgeReady(t, b, h, false)
		other := sharedHomeBackend(t, b)
		var admitted, purged error
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			_, admitted = other.reserveBotHome(context.Background(), h, "racing-pod")
		}()
		go func() { defer wg.Done(); <-start; purged = b.DeleteBotHome(context.Background(), h) }()
		close(start)
		wg.Wait()
		if admitted == nil && purged == nil {
			t.Fatal("admission and purge both won")
		}
	}
}

// Opt-in Linux execution of the production script inside an isolated Docker
// container, not evidence of a Kubernetes mount or API lifecycle.
func TestBotHomePurgeFilesystem(t *testing.T) {
	image := os.Getenv("ASTONISH_BOT_HOME_PURGE_TEST_IMAGE")
	if image == "" {
		t.Skip("set ASTONISH_BOT_HOME_PURGE_TEST_IMAGE for Linux filesystem exercise")
	}
	for _, mode := range []string{"success", "owner-mismatch", "missing-owner", "data-link", "resume", "nested-mount"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "nested-mount" && os.Getenv("ASTONISH_BOT_HOME_PURGE_MOUNT_TEST") != "1" {
				t.Skip("requires explicit privileged mount-test opt-in")
			}
			setup := `set -eu
mkdir -p /mnt/astonish-bot-home/data/nested /outside
printf 'keep' >/outside/file
printf 'owner\n' >/mnt/astonish-bot-home/.owner
printf 'delete' >/mnt/astonish-bot-home/data/nested/file
ln -s /outside /mnt/astonish-bot-home/data/link
`
			switch mode {
			case "owner-mismatch":
				setup += "printf 'foreign\\n' >/mnt/astonish-bot-home/.owner\n"
			case "missing-owner":
				setup += "rm /mnt/astonish-bot-home/.owner\n"
			case "data-link":
				setup += "rm -r /mnt/astonish-bot-home/data; ln -s /outside /mnt/astonish-bot-home/data\n"
			case "resume":
				setup += "rm -r /mnt/astonish-bot-home/data; printf 'operation\\n' >/mnt/astonish-bot-home/.purging\n"
			case "nested-mount":
				setup += "mount --bind /outside /mnt/astonish-bot-home/data/nested\n"
			}
			setup += "printf '%s' '" + strings.ReplaceAll(botHomePurgeScript, "'", "'\\''") + "' >/purge.sh\n"
			if mode == "success" || mode == "resume" {
				setup += "sh /purge.sh owner operation\nsh /purge.sh owner operation\n[ ! -e /mnt/astonish-bot-home/data ]\n[ -f /mnt/astonish-bot-home/.owner ]\n[ -f /mnt/astonish-bot-home/.purged ]\n"
			} else {
				setup += "if sh /purge.sh owner operation; then exit 91; fi\n"
			}
			setup += "[ \"$(cat /outside/file)\" = keep ]\n"
			args := []string{"container", "run", "--rm", "--network=none"}
			if mode == "nested-mount" {
				args = append(args, "--cap-add=SYS_ADMIN")
			}
			args = append(args, "--entrypoint=/bin/sh", image, "-c", setup)
			out, err := exec.Command("docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
}
