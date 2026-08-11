package treesitter

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func hasCompiler() bool {
	for _, c := range []string{"cc", "clang", "gcc"} {
		if _, err := exec.LookPath(c); err == nil {
			return true
		}
	}
	return false
}

// TestDefaultLibrary_AutoBuildsWhenAbsent proves the local code-mode path: with
// no library installed and no override, the resolver compiles the library from
// embedded sources, caches it, and loads it.
func TestDefaultLibrary_AutoBuildsWhenAbsent(t *testing.T) {
	if !hasCompiler() {
		t.Skip("no C compiler available")
	}
	ResetDefaultLibraryForTest()
	t.Cleanup(ResetDefaultLibraryForTest)

	// Isolate the build cache and ensure no override is set.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.Unsetenv("ASTONISH_TREESITTER_LIB"); err != nil {
		t.Fatal(err)
	}

	lib, err := DefaultLibrary()
	if err != nil {
		t.Fatalf("DefaultLibrary auto-build: %v", err)
	}
	if lib == nil {
		t.Fatal("expected a loaded library")
	}
}

// TestDefaultLibrary_OverrideMissingErrors keeps the explicit-override contract:
// a bad ASTONISH_TREESITTER_LIB fails fast rather than silently auto-building.
func TestDefaultLibrary_OverrideMissingErrors(t *testing.T) {
	ResetDefaultLibraryForTest()
	t.Cleanup(ResetDefaultLibraryForTest)

	t.Setenv("ASTONISH_TREESITTER_LIB", "/no/such/libastonish-treesitter.so")

	_, err := DefaultLibrary()
	if err == nil {
		t.Fatal("expected error for missing override library")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}
