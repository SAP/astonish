package main

import (
	"bytes"
	"strings"
	"testing"
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
