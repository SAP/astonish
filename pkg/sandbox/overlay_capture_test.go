package sandbox

import (
	"strings"
	"testing"
)

func TestOverlayCaptureScript_StreamsWithoutTmpTar(t *testing.T) {
	s := OverlayCaptureScript("/mnt/astonish-layers", "/var/astonish/overlay/upper", "bid-123")
	mustContain := []string{
		"set -e",
		"trap cleanup EXIT",
		"/mnt/astonish-layers/__staging-bid-123",
		"/var/astonish/overlay/upper",
		"PIPE_DIR=/dev/shm",
		"mkfifo \"$FIFO\"",
		"sha256sum",
		"tar --numeric-owner --xattrs --acls --sort=name --mtime=@0",
		`mv "$STAGING" "$LAYERS_DIR/$SHA"`,
		`echo "SHA=$SHA"`,
		`echo "SIZE=$SIZE"`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(s, needle) {
			t.Errorf("capture script missing %q:\n%s", needle, s)
		}
	}
	if strings.Contains(s, "/tmp/astn-layer.tar") {
		t.Error("must not stage a full tar on /tmp (ENOSPC after base-layer install)")
	}
	if strings.Contains(s, "$STAGING/hash.fifo") {
		t.Error("fifo must not live on the layers bind mount (virtiofs rejects mkfifo)")
	}
	if strings.Contains(s, ">(sha256sum") {
		t.Error("must not use bash process substitution (dash /bin/sh)")
	}
}
