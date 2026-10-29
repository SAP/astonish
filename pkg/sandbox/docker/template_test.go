package docker_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLayerReady(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if b.LayerReady(sandbox.BaseTemplateID) {
		t.Fatal("empty layers dir should not be ready")
	}
	rootfs := filepath.Join(b.LayersDir(), sandbox.BaseTemplateID, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	if b.LayerReady(sandbox.BaseTemplateID) {
		t.Fatal("empty rootfs should not be ready")
	}
	if err := os.WriteFile(filepath.Join(rootfs, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !b.LayerReady(sandbox.BaseTemplateID) {
		t.Fatal("non-empty rootfs should be ready")
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

func TestReseedBaseLayerFromImage_UsesLocalRuntimeImageWithoutPull(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	runtimePath := filepath.Join(binDir, "docker")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$ASTONISH_TEST_DOCKER_LOG"
case "$1" in
  export)
    work=$(mktemp -d)
    printf 'fresh-runtime' > "$work/runtime-marker"
    tar -C "$work" -cf - .
    rm -rf "$work"
    ;;
esac
`
	if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTONISH_TEST_DOCKER_LOG", logPath)

	r := newTestRegistry(t)
	layersDir := t.TempDir()
	b, err := docker.New(docker.Config{
		Sessions:             r,
		LayersDir:            layersDir,
		UppersDir:            t.TempDir(),
		SandboxImage:         "ghcr.io/sap/astonish-sandbox-base:latest",
		ContainerRuntimePath: runtimePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleRoot := filepath.Join(layersDir, sandbox.BaseTemplateID, "rootfs")
	if err := os.MkdirAll(staleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleRoot, "stale-marker"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := b.ReseedBaseLayerFromImage(t.Context()); err != nil {
		t.Fatalf("ReseedBaseLayerFromImage: %v", err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logBytes), "pull ") {
		t.Fatalf("refresh must not pull over the locally built runtime image; docker calls:\n%s", logBytes)
	}
	if got, err := os.ReadFile(filepath.Join(staleRoot, "runtime-marker")); err != nil || string(got) != "fresh-runtime" {
		t.Fatalf("reseeded base marker = %q, %v", got, err)
	}
}
