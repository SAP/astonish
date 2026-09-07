package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
)

func TestAliasLayer_CreatesRelativeSymlink(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}

	layerID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	layerDir := filepath.Join(b.LayersDir(), layerID)
	if err := os.MkdirAll(filepath.Join(layerDir, "rootfs"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := b.AliasLayer("web", layerID); err != nil {
		t.Fatalf("AliasLayer: %v", err)
	}
	alias := filepath.Join(b.LayersDir(), "web")
	got, err := os.Readlink(alias)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != layerID {
		t.Errorf("symlink target = %q, want %q", got, layerID)
	}
	if _, err := os.Stat(filepath.Join(alias, "rootfs")); err != nil {
		t.Errorf("aliased rootfs: %v", err)
	}
}

func TestAliasLayer_RejectsBase(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "base", sandbox.BaseTemplateID} {
		if err := b.AliasLayer(name, "abc"); err == nil {
			t.Errorf("AliasLayer(%q) succeeded, want error", name)
		}
	}
}

func TestDeleteTemplate_RefusesBase(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteTemplate(t.Context(), sandbox.BaseTemplateID, true); err == nil {
		t.Fatal("DeleteTemplate(@base) succeeded, want error")
	}
}
