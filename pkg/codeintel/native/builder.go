// Package native builds the astonish tree-sitter shared library on demand.
//
// In container images the library is compiled at image-build time and installed
// under /usr/lib/astonish. Local code mode runs in the user's own environment
// where that path does not exist, so this package compiles the library from the
// embedded C sources on first use and caches the result under the user's config
// directory. The compile is a plain `cc -shared`, matching
// pkg/codeintel/native/Makefile.
package native

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	nativeembed "github.com/SAP/astonish/pkg/codeintel/native/embed"
)

// ErrNoCompiler indicates no usable C compiler was found on PATH, so the library
// cannot be built locally. Callers should fall back to grep_search/find_files.
var ErrNoCompiler = errors.New("no C compiler found (need cc, clang, or gcc)")

// buildTimeout bounds the one-time compile so a hung toolchain cannot wedge a
// coding turn. The compile normally takes a few seconds.
const buildTimeout = 3 * time.Minute

// libraryFileName is the shared-object name written into the cache directory.
func libraryFileName() string {
	if runtime.GOOS == "darwin" {
		return "libastonish-treesitter.dylib"
	}
	return "libastonish-treesitter.so"
}

// CacheDir returns the directory where the locally built library is cached. It
// honors XDG_CONFIG_HOME (like pkg/config.GetConfigDir) for portability and test
// isolation, without importing pkg/config to avoid an import cycle.
func CacheDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = cfg
	}
	// Namespace by embedded-source version so a binary upgrade with newer
	// grammars rebuilds instead of loading a stale library.
	return filepath.Join(base, "astonish", "lib", nativeembed.Version), nil
}

// CachedLibraryPath returns the absolute path the built library would occupy.
func CachedLibraryPath() (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, libraryFileName()), nil
}

// findCompiler resolves a C compiler, honoring the CC environment variable and
// then trying common names.
func findCompiler() (string, error) {
	candidates := []string{os.Getenv("CC"), "cc", "clang", "gcc"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	return "", ErrNoCompiler
}

// EnsureLibrary returns the path to a usable tree-sitter shared library,
// building it from the embedded sources and caching it if necessary. A cached
// build is reused across runs. It returns ErrNoCompiler when the library is
// absent and no compiler is available.
func EnsureLibrary() (string, error) {
	cached, err := CachedLibraryPath()
	if err != nil {
		return "", err
	}
	if fi, statErr := os.Stat(cached); statErr == nil && fi.Size() > 0 {
		return cached, nil
	}
	return buildLibrary(cached)
}

// buildLibrary extracts the embedded sources to a temporary directory, compiles
// the shared library, and installs it atomically at dst.
func buildLibrary(dst string) (string, error) {
	compiler, err := findCompiler()
	if err != nil {
		return "", err
	}

	srcDir, err := os.MkdirTemp("", "astonish-treesitter-src-")
	if err != nil {
		return "", fmt.Errorf("create temp source dir: %w", err)
	}
	defer os.RemoveAll(srcDir)

	if err := extractTarGz(nativeembed.SourceTarball, srcDir); err != nil {
		return "", fmt.Errorf("extract embedded tree-sitter sources: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create library cache dir: %w", err)
	}

	manifest := nativeembed.Manifest()
	args := []string{"-O2", "-fPIC", "-shared"}
	for _, inc := range manifest.IncludeDirs {
		args = append(args, "-I"+filepath.Join(srcDir, filepath.FromSlash(inc)))
	}
	for _, src := range manifest.Sources {
		args = append(args, filepath.Join(srcDir, filepath.FromSlash(src)))
	}
	// Compile to a temp file in the destination dir, then rename atomically so a
	// concurrent reader never sees a half-written library.
	tmpOut, err := os.CreateTemp(filepath.Dir(dst), ".libastonish-treesitter-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp library file: %w", err)
	}
	tmpOutPath := tmpOut.Name()
	_ = tmpOut.Close()
	defer os.Remove(tmpOutPath)
	args = append(args, "-o", tmpOutPath)

	if err := runCompiler(compiler, args); err != nil {
		return "", err
	}

	if err := os.Rename(tmpOutPath, dst); err != nil {
		return "", fmt.Errorf("install built library: %w", err)
	}
	return dst, nil
}

func runCompiler(compiler string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, compiler, args...) // #nosec G204 -- compiler resolved from PATH; args are embedded-asset paths, not user input.
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("compile tree-sitter library: timed out after %s", buildTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("compile tree-sitter library: %s: %s", err, msg)
	}
	return nil
}

// extractTarGz unpacks a gzip-compressed tar archive into dst, guarding against
// path traversal.
func extractTarGz(data []byte, dst string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dst, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFileFromTar(tr, target); err != nil {
				return err
			}
		default:
			// Skip symlinks and other special entries; the grammar sources are
			// plain files and directories.
		}
	}
	return nil
}

func writeFileFromTar(tr *tar.Reader, target string) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		// Copy in bounded chunks to keep memory flat and satisfy gosec's
		// decompression-bomb check; the archive is a trusted embedded asset.
		if _, err := io.CopyN(f, tr, 1<<20); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// safeJoin joins name onto base, rejecting entries that escape base.
func safeJoin(base, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(base, clean)
	if !strings.HasPrefix(target, filepath.Clean(base)+string(filepath.Separator)) && target != filepath.Clean(base) {
		return "", fmt.Errorf("archive path %q escapes destination", name)
	}
	return target, nil
}
