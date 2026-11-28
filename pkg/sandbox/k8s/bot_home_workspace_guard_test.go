package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func TestBotHomeWorkspaceGuardedPublicationProtocol(t *testing.T) {
	b, h := homeBackend(t)
	workspaceFake(t, b)
	called := false
	b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
		return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
			var request map[string]any
			if err := json.NewDecoder(o.Stdin).Decode(&request); err != nil {
				return err
			}
			called = true
			if _, err := o.Stdout.Write([]byte("WORKSPACE_READY\n")); err != nil {
				return err
			}
			line, err := bufio.NewReader(o.Stdin).ReadString('\n')
			if err != nil {
				return err
			}
			if line != "commit\n" {
				return errors.New("publication denied")
			}
			_, err = o.Stdout.Write([]byte("null\n"))
			return err
		}), nil
	}
	ctx := sandbox.WithBotHomeWorkspaceCommitGuard(t.Context(), func(context.Context) error { return errors.New("lease revoked") })
	if err := b.WriteBotHomeWorkspace(ctx, h, sandbox.BotHomeWorkspaceFile{Path: "tasks/task/state/value", Data: []byte("late")}); err == nil || !called {
		t.Fatal("late commit accepted", err, called)
	}
	if rs := homeReservations(t, b, h); len(rs) != 0 {
		t.Fatal("helper leaked", rs)
	}
}
