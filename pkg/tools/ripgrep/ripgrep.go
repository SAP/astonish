// Package ripgrep guarantees a usable ripgrep (rg) binary for the code-search
// tools. ripgrep is far superior to the pure-Go fallback (gitignore-aware,
// faster, supports type filters, multiline, and context lines), so code mode —
// which runs on the user's own machine rather than a sandbox that apt-installs
// rg — provisions it: it prefers an rg already on PATH, and otherwise downloads
// the pinned official release for the host OS/arch, verifies its SHA256, and
// caches it under the user's config directory.
package ripgrep

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version is the pinned ripgrep release provisioned when rg is not already
// installed. Bump alongside the checksums in targets below.
const Version = "14.1.1"

// downloadTimeout bounds the one-time fetch+extract so provisioning cannot hang.
const downloadTimeout = 90 * time.Second

// ErrUnsupportedPlatform indicates no pinned ripgrep asset exists for the host.
var ErrUnsupportedPlatform = errors.New("no pinned ripgrep build for this platform")

// target describes a downloadable ripgrep release asset for one OS/arch.
type target struct {
	// asset is the release tarball name (without the .tar.gz suffix), which is
	// also the directory the archive extracts into.
	asset  string
	sha256 string
}

// targets maps GOOS/GOARCH to the pinned official ripgrep asset + checksum.
// Checksums are the official *.sha256 files from the v14.1.1 release.
var targets = map[string]target{
	"darwin/arm64": {
		asset:  "ripgrep-14.1.1-aarch64-apple-darwin",
		sha256: "24ad76777745fbff131c8fbc466742b011f925bfa4fffa2ded6def23b5b937be",
	},
	"darwin/amd64": {
		asset:  "ripgrep-14.1.1-x86_64-apple-darwin",
		sha256: "fc87e78f7cb3fea12d69072e7ef3b21509754717b746368fd40d88963630e2b3",
	},
	"linux/amd64": {
		asset:  "ripgrep-14.1.1-x86_64-unknown-linux-musl",
		sha256: "4cf9f2741e6c465ffdb7c26f38056a59e2a2544b51f7cc128ef28337eeae4d8e",
	},
	"linux/arm64": {
		asset:  "ripgrep-14.1.1-aarch64-unknown-linux-gnu",
		sha256: "c827481c4ff4ea10c9dc7a4022c8de5db34a5737cb74484d62eb94a95841ab2f",
	},
}

// downloadURL is overridable in tests to point at a local fixture server.
var downloadURL = func(asset string) string {
	return fmt.Sprintf("https://github.com/BurntSushi/ripgrep/releases/download/%s/%s.tar.gz", Version, asset)
}

var (
	resolveOnce sync.Once
	resolvePath string
)

// ResolvePath returns the path to a usable rg binary, provisioning it if needed.
// Resolution order: an rg already on PATH (respect the user's install), then the
// managed copy under the config dir, then a fresh download. The result is
// memoized for the process. On failure it returns an error; callers may fall
// back to a pure-Go search but should treat that as degraded.
func ResolvePath() (string, error) {
	var err error
	resolveOnce.Do(func() {
		resolvePath, err = resolve()
	})
	if resolvePath == "" && err == nil {
		// A prior successful call is cached; re-resolve if the once ran with an
		// error (resolvePath empty) so transient failures can recover.
		return resolve()
	}
	return resolvePath, err
}

func resolve() (string, error) {
	if p, err := exec.LookPath("rg"); err == nil {
		return p, nil
	}
	return EnsureManaged()
}

// binaryName is the on-disk name of the managed rg binary.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "rg.exe"
	}
	return "rg"
}

// CacheDir returns the directory holding the managed rg binary. It honors
// XDG_CONFIG_HOME (like pkg/config.GetConfigDir) for portability and test
// isolation, without importing pkg/config to avoid a dependency cycle.
func CacheDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = cfg
	}
	return filepath.Join(base, "astonish", "bin"), nil
}

// ManagedPath returns the absolute path the managed rg binary would occupy.
func ManagedPath() (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, binaryName()), nil
}

// EnsureManaged returns the path to the managed rg binary, downloading and
// verifying it if it is not already cached.
func EnsureManaged() (string, error) {
	managed, err := ManagedPath()
	if err != nil {
		return "", err
	}
	if fi, statErr := os.Stat(managed); statErr == nil && fi.Size() > 0 && fi.Mode()&0o111 != 0 {
		return managed, nil
	}
	if err := download(managed); err != nil {
		return "", err
	}
	return managed, nil
}

// download fetches the pinned release for the host, verifies its checksum,
// extracts the rg binary, and installs it atomically at dst.
func download(dst string) error {
	tgt, ok := targets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL(tgt.asset), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download ripgrep: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download ripgrep: unexpected status %s", resp.Status)
	}

	// Read the whole archive so we can checksum it before trusting any bytes.
	// Cap the read to guard against an unexpectedly huge response.
	const maxArchive = 32 << 20 // 32 MiB; rg tarballs are a few MB.
	archive, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return fmt.Errorf("read ripgrep archive: %w", err)
	}
	if len(archive) > maxArchive {
		return fmt.Errorf("ripgrep archive larger than %d bytes", maxArchive)
	}

	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != tgt.sha256 {
		return fmt.Errorf("ripgrep checksum mismatch for %s: got %s, want %s", tgt.asset, got, tgt.sha256)
	}

	rgBytes, err := extractRg(archive, tgt.asset)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create ripgrep cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".rg-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(rgBytes); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("install ripgrep: %w", err)
	}
	return nil
}

// extractRg pulls the rg binary out of the gzip-compressed release tarball.
// Official archives place it at "<asset>/rg".
func extractRg(archive []byte, asset string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("open ripgrep archive: %w", err)
	}
	defer gz.Close()

	want := asset + "/rg"
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read ripgrep archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == want || filepath.Base(name) == "rg" {
			const maxBinary = 64 << 20 // 64 MiB ceiling for the rg binary.
			data, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
			if err != nil {
				return nil, fmt.Errorf("extract rg: %w", err)
			}
			if len(data) > maxBinary {
				return nil, fmt.Errorf("rg binary larger than %d bytes", maxBinary)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("rg binary not found in %s.tar.gz", asset)
}
