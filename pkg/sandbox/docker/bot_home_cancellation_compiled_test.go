package docker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// Pausing the consumer leaves the remote node blocked on its stdout. Cancellation
// must reap that node, not merely disconnect the local docker CLI.
func verifyCompiledDownloadCancellation(t *testing.T, db *DockerBackend, h *sandbox.BotHomeBinding, warm bool) {
	t.Helper()
	ctx := t.Context()
	body := bytes.Repeat([]byte("paused-download\x00"), 1<<20)
	ref, err := db.PutBotContent(ctx, h, store.BotContentArtifact, int64(len(body)), bytes.NewReader(body), 1, func(f func() error) error { return f() })
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	operation, end := sandbox.WithBotHomeFileOperation(cancelled)
	defer end()
	other, endOther := sandbox.WithBotHomeFileOperation(ctx)
	defer endOther()
	first, err := db.OpenBotContent(operation, h, *ref, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.OpenBotContent(other, h, *ref, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	p := first.ReadCloser.(*homeFileProcess)
	q := second.ReadCloser.(*homeFileProcess)
	if !warm && p.name == q.name {
		t.Fatal("independent operations shared an attachment")
	}
	// Neither body is drained: both remote writers must still exist.
	select {
	case <-p.done:
		t.Fatal("download unexpectedly finished before cancellation")
	default:
	}
	cancel()
	select {
	case <-p.done:
	case <-time.After(12 * time.Second):
		t.Fatal("cancelled download did not join")
	}
	if err = first.Close(); err == nil {
		t.Fatal("cancelled download lost terminal error")
	}
	if err = end(); err != nil {
		t.Fatal(err)
	}
	if warm {
		// The cancelled operation's unique argv token must disappear remotely, while
		// the other operation remains alive in the same physical session.
		token := p.cmd.Args[len(p.cmd.Args)-1]
		probe := `for f in /proc/[0-9]*/cmdline; do test -r "$f" || continue; tr '\000' '\n' < "$f" 2>/dev/null | grep -Fxq '` + token + `' && exit 1; done; exit 0`
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "exec", p.name, "/bin/sh", "-ec", probe)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("cancelled remote process still present: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	} else if _, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", p.name); err == nil {
		t.Fatal("cancelled operation attachment remains")
	}
	live, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.State.Running}}", q.name)
	if err != nil || strings.TrimSpace(string(live)) != "true" {
		t.Fatalf("cancellation destroyed unrelated operation/session: %s %v", live, err)
	}
	got, err := io.ReadAll(second)
	closeErr := second.Close()
	if err != nil || closeErr != nil || !bytes.Equal(got, body) {
		t.Fatalf("unrelated download corrupted: bytes=%d err=%v close=%v", len(got), err, closeErr)
	}
	if err = endOther(); err != nil {
		t.Fatal(err)
	}
	t.Logf("paused download cancellation joined; warm=%t; independent operation delivered %d exact bytes", warm, len(got))
}
