package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox"
	"net/url"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func TestCompiledWorkspaceUnicodeReceipt(t *testing.T) {
	_, h, _ := warmHomeFilesFixture(t)
	entry := sandbox.BotHomeWorkspaceEntry{Path: "line\u2028break\u2029.txt", Digest: fmt.Sprintf("%x", sha256.Sum256(nil))}
	// Python ensure_ascii=False emits the separator characters literally.
	canonical := fmt.Sprintf(`[{"digest":"%s","executable":false,"path":"%s","size_bytes":0}]`, entry.Digest, entry.Path)
	r := &sandbox.BotHomeWorkspaceReceipt{ReceiptRef: "pub", PublicationID: "pub", TaskPath: "tasks/task", BindingKey: h.Scope.Key(), Entries: []sandbox.BotHomeWorkspaceEntry{entry}, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))}
	if err := validateWorkspaceReceipt(h, r); err != nil {
		t.Fatal(err)
	}
}

func TestCompiledWorkspaceJoinsRequestWriter(t *testing.T) {
	b, _, _ := warmHomeFilesFixture(t)
	b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
		return homeReadExecutor(func(_ context.Context, o remotecommand.StreamOptions) error {
			// A broken remote endpoint cannot claim success without consuming input.
			_, err := o.Stdout.Write([]byte("null\n"))
			return err
		}), nil
	}
	if err := b.executeWorkspaceCommand(t.Context(), "fixture", []string{"fixture"}, []byte(`{"op":"write"}`), nil); err == nil {
		t.Fatal("unconsumed request accepted")
	}
}

func TestCompiledWorkspaceWarmNoHelper(t *testing.T) {
	b, h, client := warmHomeFilesFixture(t)
	b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
		args := strings.Join(u.Query()["command"], " ")
		if !strings.Contains(u.Path, podNameForSession("canonical")) || !strings.Contains(args, "--internal-scoped-home-workspace") || !strings.Contains(args, "physical-session") || strings.Contains(args, "python") {
			t.Fatal(u)
		}
		return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
			var q map[string]any
			if err := json.NewDecoder(o.Stdin).Decode(&q); err != nil {
				return err
			}
			return json.NewEncoder(o.Stdout).Encode([]byte("value"))
		}), nil
	}
	data, err := b.ReadBotHomeWorkspace(t.Context(), h, "tasks/task/state/x", 100)
	if err != nil || string(data) != "value" {
		t.Fatalf("%q %v", data, err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("warm operation changed API: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}
