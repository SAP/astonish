package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox/k8s"
)

// TestRun_EmitsCanonicalScript locks in the shape that the host-side
// `make sandbox-entrypoint` step depends on: the emitted bytes must
// begin with the POSIX shebang and contain the well-known entrypoint
// sentinels so a broken defaults change is caught before it ships to
// the base image.
func TestRun_EmitsCanonicalScript(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "#!/bin/sh") {
		t.Fatalf("script missing shebang, got: %q", out[:min(40, len(out))])
	}
	for _, want := range []string{
		"ASTONISH_SESSION_ID",
		"ASTONISH_LAYER_CHAIN",
		"mount -t overlay overlay",
		"exec chroot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

func TestRun_GeneratorEqualityAndPackaging(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatal(err)
	}
	canonical := k8s.EntrypointScript(k8s.EntrypointScriptOptions{})
	if out.String() != canonical {
		t.Fatal("generator diverges from canonical script")
	}
	// The build artifact is gitignored and absent in clean checkouts. Compare
	// generator output directly with its canonical source above; packaging below
	// verifies that image builds install the generated script.
	dockerfile, err := os.ReadFile("../../docker/sandbox-base/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"util-linux", "command -v flock", "COPY astonish-sandbox-entrypoint /usr/local/bin/astonish-sandbox-entrypoint"} {
		if !strings.Contains(string(dockerfile), want) {
			t.Errorf("packaging missing %q", want)
		}
	}
	native, err := os.ReadFile("../../docker/sandbox-openshell/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(native), "does not consume the overlay initialization marker") {
		t.Fatal("native supervisor contract undocumented")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
