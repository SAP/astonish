package native

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	nativeembed "github.com/SAP/astonish/pkg/codeintel/native/embed"
)

func hasCompiler() bool {
	for _, c := range []string{"cc", "clang", "gcc"} {
		if _, err := exec.LookPath(c); err == nil {
			return true
		}
	}
	return false
}

func TestCacheDir_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/astonish-xdg-test")
	dir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/astonish-xdg-test", "astonish", "lib", nativeembed.Version)
	if dir != want {
		t.Fatalf("CacheDir() = %q, want %q", dir, want)
	}
}

func TestCachedLibraryPath_UsesPlatformExtension(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := CachedLibraryPath()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(p)
	if base != "libastonish-treesitter.so" && base != "libastonish-treesitter.dylib" {
		t.Fatalf("unexpected library file name %q", base)
	}
}

func TestEnsureLibrary_BuildsAndCaches(t *testing.T) {
	if !hasCompiler() {
		t.Skip("no C compiler available")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := EnsureLibrary()
	if err != nil {
		t.Fatalf("EnsureLibrary build: %v", err)
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("built library not found: %v", statErr)
	}
	if fi.Size() == 0 {
		t.Fatal("built library is empty")
	}

	// A second call must reuse the cached artifact (same path, still present)
	// without rebuilding.
	before := fi.ModTime()
	path2, err := EnsureLibrary()
	if err != nil {
		t.Fatalf("EnsureLibrary cache: %v", err)
	}
	if path2 != path {
		t.Fatalf("cached path changed: %q -> %q", path, path2)
	}
	fi2, _ := os.Stat(path2)
	if !fi2.ModTime().Equal(before) {
		t.Fatal("expected cache reuse (unchanged mtime), library was rebuilt")
	}
}

func TestBuildLibrary_NoCompiler(t *testing.T) {
	// Force compiler discovery to fail regardless of the host toolchain.
	t.Setenv("CC", "astonish-nonexistent-compiler")
	t.Setenv("PATH", t.TempDir())

	_, err := buildLibrary(filepath.Join(t.TempDir(), "out.so"))
	if !errors.Is(err, ErrNoCompiler) {
		t.Fatalf("expected ErrNoCompiler, got %v", err)
	}
}

func TestSafeJoin_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	for _, bad := range []string{"../escape", "../../etc/passwd", "/abs/path"} {
		if _, err := safeJoin(base, bad); err == nil {
			t.Fatalf("safeJoin allowed unsafe path %q", bad)
		}
	}
	good, err := safeJoin(base, "tree-sitter/lib/src/lib.c")
	if err != nil {
		t.Fatalf("safeJoin rejected valid path: %v", err)
	}
	if !strings.HasPrefix(good, base) {
		t.Fatalf("safeJoin escaped base: %q", good)
	}
}

func TestExtractTarGz_UnpacksEmbeddedSources(t *testing.T) {
	dst := t.TempDir()
	if err := extractTarGz(nativeembed.SourceTarball, dst); err != nil {
		t.Fatalf("extract embedded sources: %v", err)
	}
	// Spot-check that the files the compile manifest needs are present.
	for _, rel := range nativeembed.Manifest().Sources {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected source %q after extract: %v", rel, err)
		}
	}
}
