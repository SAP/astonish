package k8s

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

const treeSitterLibPath = "/usr/lib/astonish/libastonish-treesitter.so"

func TestTreeSitterLibraryPathContract(t *testing.T) {
	if sandbox.TreeSitterLibraryDestPath != treeSitterLibPath {
		t.Errorf("sandbox.TreeSitterLibraryDestPath = %q, want %q", sandbox.TreeSitterLibraryDestPath, treeSitterLibPath)
	}

	var opts EntrypointScriptOptions
	opts.applyDefaults()
	if opts.HostTreeSitterLibPath != treeSitterLibPath {
		t.Errorf("HostTreeSitterLibPath default = %q, want %q", opts.HostTreeSitterLibPath, treeSitterLibPath)
	}

	dockerfile := readSandboxBaseDockerfile(t)
	wantCopy := "COPY --from=treesitter-builder /tmp/libastonish-treesitter.so " + treeSitterLibPath
	if !strings.Contains(dockerfile, wantCopy) {
		t.Fatalf("sandbox-base Dockerfile must COPY library to %s; missing:\n%s", treeSitterLibPath, wantCopy)
	}
	// treesitter-builder needs libc headers; gcc alone on Debian slim is insufficient.
	if !strings.Contains(dockerfile, "libc6-dev") {
		t.Fatal("sandbox-base Dockerfile treesitter-builder must apt-install libc6-dev")
	}
	// Guard against accidental apt install of ripgrep in the pod image
	// (invisible after overlay chroot; belongs in @base / OpenShell).
	if strings.Contains(dockerfile, "\tripgrep") || strings.Contains(dockerfile, " ripgrep \\\n") || strings.Contains(dockerfile, " ripgrep\n") {
		t.Fatal("sandbox-base Dockerfile must not apt-install ripgrep; install via CoreToolInstallCommands into @base")
	}
}

func TestSandboxBaseDockerfileIncludesMuslLoaderCompatLink(t *testing.T) {
	dockerfile := readSandboxBaseDockerfile(t)
	want := "ln -sf x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib/ld-musl-x86_64.so.1"
	if !strings.Contains(dockerfile, want) {
		t.Fatalf("sandbox-base Dockerfile must create musl loader compatibility link; missing:\n%s", want)
	}
}

func readSandboxBaseDockerfile(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// pkg/sandbox/k8s → repo root
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../../.."))
	path := filepath.Join(repoRoot, "docker/sandbox-base/Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
