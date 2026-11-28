package docker

import (
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestHomeExecCommand(t *testing.T) {
	db := &DockerBackend{cfg: Config{UppersDir: t.TempDir()}}
	if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: "retained", TemplateID: "base", LayerChain: []string{"base"}, Image: "image", BotHome: testBotHome()})); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ordinary", "retained"} {
		for _, dir := range []string{"", "/tmp"} {
			for _, cmd := range [][]string{{"pwd"}, {astonishShell, "sh", "-c", "pwd"}, nil} {
				got, outer, err := db.homeExecCommand(id, cmd, dir)
				if err != nil {
					t.Fatal(err)
				}
				want := wrapShell(cmd)
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
