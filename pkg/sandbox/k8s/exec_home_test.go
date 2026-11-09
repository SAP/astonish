package k8s

import (
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestHomeExecDispatch(t *testing.T) {
	b, home := homeBackend(t)
	if _, err := b.sessionBotHome(t.Context(), "retained", home, false); err != nil {
		t.Fatal(err)
	}
	seedSession(t, b, "retained", "pod")
	seedSession(t, b, "ordinary", "other-pod")
	for _, id := range []string{"ordinary", "retained"} {
		for _, dir := range []string{"", "/tmp"} {
			for _, mode := range []string{"buffered", "interactive", "streaming"} {
				t.Run(id+dir+mode, func(t *testing.T) {
					se := stubFactory(t, b, nil)
					command := []string{"/usr/local/bin/astonish-shell", "sh", "-c", "pwd"}
					var err error
					if mode == "buffered" {
						_, err = b.Exec(t.Context(), id, sandbox.ExecSpec{Command: command, WorkDir: dir})
					} else {
						var stream sandbox.ExecStream
						if mode == "interactive" {
							stream, err = b.ExecInteractive(t.Context(), id, sandbox.PTYSpec{Command: command, WorkDir: dir})
						} else {
							stream, err = b.ExecStreaming(t.Context(), id, sandbox.ExecStreamSpec{Command: command, WorkDir: dir})
						}
						if err == nil {
							_, err = stream.Wait()
							stream.Close()
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					want := wrapCommand(command, dir, nil)
					if id == "retained" {
						want = sandbox.RetainedBotHomeCommand(command, dir)
					}
					if got := se.url.Query()["command"]; !reflect.DeepEqual(got, want) {
						t.Fatalf("dispatch = %q want %q", got, want)
					}
				})
			}
		}
	}
}

func TestHomeExecCommand(t *testing.T) {
	b, home := homeBackend(t)
	if _, err := b.sessionBotHome(t.Context(), "retained", home, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ordinary", "retained"} {
		for _, dir := range []string{"", "/tmp"} {
			for _, cmd := range [][]string{{"pwd"}, {"/usr/local/bin/astonish-shell", "sh", "-c", "pwd"}, nil} {
				got, outer, err := b.homeExecCommand(t.Context(), id, cmd, dir)
				if err != nil {
					t.Fatal(err)
				}
				want := cmd
				wantOuter := dir
				if id == "retained" {
					want = sandbox.RetainedBotHomeCommand(cmd, dir)
					wantOuter = ""
				}
				if !reflect.DeepEqual(got, want) || outer != wantOuter {
					t.Fatalf("%s %q: %q %q", id, dir, got, outer)
				}
			}
		}
	}
}
