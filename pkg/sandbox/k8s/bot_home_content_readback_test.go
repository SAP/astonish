package k8s

import (
	"bytes"
	"context"
	"errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotHomeContentReadbackAdapter(t *testing.T) {
	for _, mode := range []string{"valid", "deny", "double", "cancel", "corrupt-stage", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			b, h, root := localHomeFilesBackend(t)
			if mode == "terminal" {
				factory := b.execExecutorFactory
				b.execExecutorFactory = func(c *rest.Config, method string, u *url.URL) (remoteExecutor, error) {
					executor, err := factory(c, method, u)
					if err != nil {
						return nil, err
					}
					return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
						return errors.Join(executor.StreamWithContext(ctx, o), errors.New("readback terminal failure"))
					}), nil
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := bytes.Repeat([]byte("stored\x00bytes\n"), 10000)
			barrier := func(f func() error) error {
				switch mode {
				case "deny":
					return store.ErrBotForbidden
				case "cancel":
					cancel()
				case "corrupt-stage":
					files, err := filepath.Glob(filepath.Join(root, "workspace-private/content-v1/.stage-*"))
					if err != nil || len(files) != 1 {
						t.Fatal(files, err)
					}
					if err = os.WriteFile(files[0], bytes.Repeat([]byte("!"), len(body)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := f(); err != nil {
					return err
				}
				if mode == "double" {
					return f()
				}
				return nil
			}
			ref, raw, err := b.PutBotContentReadback(ctx, h, store.BotContentEvidence, int64(len(body)), bytes.NewReader(body), 1, barrier)
			if mode != "valid" {
				if err == nil || ref != nil || raw != nil {
					t.Fatal("unsafe readback accepted", err)
				}
				return
			}
			if err != nil || !bytes.Equal(raw, body) {
				t.Fatal("readback", err)
			}
			disk, err := os.ReadFile(filepath.Join(root, "workspace-private", ref.Key))
			if err != nil || !bytes.Equal(disk, raw) {
				t.Fatal("not stored bytes", err)
			}
			if len(homeReservations(t, b, h)) != 0 {
				t.Fatal("helper attachment leaked")
			}
		})
	}
}
