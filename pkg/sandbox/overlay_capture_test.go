package sandbox

import (
	"strings"
	"testing"
)

func TestOverlayCaptureScript_StreamsWithoutTmpTar(t *testing.T) {
	s := OverlayCaptureScript(OverlayCaptureOpts{
		LayersDir:     "/mnt/astonish-layers",
		UpperDir:      "/var/astonish/overlay/upper",
		BuilderID:     "bid-123",
		ExtractXattrs: false,
	})
	mustContain := []string{
		"set -euo pipefail",
		"trap cleanup EXIT",
		"/mnt/astonish-layers/__staging-bid-123",
		"/var/astonish/overlay/upper",
		"sha256sum",
		"tar --numeric-owner --xattrs --acls --sort=name --mtime=@0",
		`mv "$STAGING" "$LAYERS_DIR/$SHA"`,
		`echo "SHA=$SHA"`,
		`echo "SIZE=0"`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(s, needle) {
			t.Errorf("capture script missing %q:\n%s", needle, s)
		}
	}
	if strings.Contains(s, "/tmp/astn-layer.tar") {
		t.Error("must not stage a full tar on /tmp (ENOSPC after base-layer install)")
	}
	if strings.Contains(s, "mkfifo") {
		t.Error("must not use mkfifo (virtiofs EPERM and tee deadlock)")
	}
	if strings.Contains(s, "du -sm") || strings.Contains(s, "du -sb") {
		t.Error("must not walk the overlay with du (slow/OOM on fuse + virtiofs)")
	}
	if strings.Contains(s, ">(sha256sum") {
		t.Error("must not use bash process substitution")
	}
	// virtiofs extract must not request xattrs
	if strings.Count(s, "tar --numeric-owner --xattrs --acls -C") != 0 {
		t.Error("docker/virtiofs extract must not pass --xattrs --acls")
	}
}

func TestOverlayCaptureScript_K8sKeepsXattrsOnExtract(t *testing.T) {
	s := OverlayCaptureScript(OverlayCaptureOpts{
		LayersDir:     "/mnt/astonish-layers",
		UpperDir:      "/var/astonish/overlay/upper",
		BuilderID:     "bid-k8s",
		ExtractXattrs: true,
	})
	if !strings.Contains(s, "tar --numeric-owner --xattrs --acls -C") {
		t.Fatal("k8s extract should preserve xattrs/acls on CephFS")
	}
}
