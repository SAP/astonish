package ripgrep

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// makeArchive builds a gzip tarball containing "<asset>/rg" with the given
// payload, returning the archive bytes and their hex SHA256.
func makeArchive(t *testing.T, asset string, payload []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     asset + "/rg",
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// currentTarget returns the pinned target for the host, or skips if unsupported.
func currentTarget(t *testing.T) target {
	t.Helper()
	tgt, ok := targets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Skipf("no pinned ripgrep target for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return tgt
}

func TestEnsureManaged_DownloadsVerifiesAndCaches(t *testing.T) {
	tgt := currentTarget(t)
	payload := []byte("#!/bin/sh\necho ripgrep 14.1.1\n")
	archive, sum := makeArchive(t, tgt.asset, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	// Point downloads at the fixture and pin its checksum for the host target.
	restoreURL := downloadURL
	downloadURL = func(string) string { return srv.URL }
	t.Cleanup(func() { downloadURL = restoreURL })
	restore := targets[runtime.GOOS+"/"+runtime.GOARCH]
	targets[runtime.GOOS+"/"+runtime.GOARCH] = target{asset: tgt.asset, sha256: sum}
	t.Cleanup(func() { targets[runtime.GOOS+"/"+runtime.GOARCH] = restore })

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := EnsureManaged()
	if err != nil {
		t.Fatalf("EnsureManaged: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("extracted rg content mismatch")
	}
	fi, _ := os.Stat(path)
	if fi.Mode()&0o111 == 0 {
		t.Fatal("managed rg is not executable")
	}

	// A second call reuses the cache without hitting the (now-closed) server.
	srv.Close()
	if _, err := EnsureManaged(); err != nil {
		t.Fatalf("cached EnsureManaged: %v", err)
	}
}

func TestEnsureManaged_RejectsChecksumMismatch(t *testing.T) {
	tgt := currentTarget(t)
	archive, _ := makeArchive(t, tgt.asset, []byte("tampered"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	restoreURL := downloadURL
	downloadURL = func(string) string { return srv.URL }
	t.Cleanup(func() { downloadURL = restoreURL })
	restore := targets[runtime.GOOS+"/"+runtime.GOARCH]
	targets[runtime.GOOS+"/"+runtime.GOARCH] = target{asset: tgt.asset, sha256: "0000000000000000000000000000000000000000000000000000000000000000"}
	t.Cleanup(func() { targets[runtime.GOOS+"/"+runtime.GOARCH] = restore })

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := EnsureManaged()
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("checksum mismatch")) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	// Nothing should have been installed.
	managed, _ := ManagedPath()
	if _, statErr := os.Stat(managed); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a binary was installed despite checksum failure")
	}
}

func TestResolvePath_PrefersSystemRg(t *testing.T) {
	// Put a fake rg on PATH and confirm ResolvePath returns it without
	// downloading. Reset the memoization guard first.
	resolveOnce = sync.Once{}
	resolvePath = ""
	dir := t.TempDir()
	fake := filepath.Join(dir, "rg")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != fake {
		t.Fatalf("ResolvePath = %q, want system rg %q", got, fake)
	}
}

func TestCacheDir_HonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/astonish-rg-xdg")
	dir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/astonish-rg-xdg", "astonish", "bin")
	if dir != want {
		t.Fatalf("CacheDir() = %q, want %q", dir, want)
	}
}

func TestExtractRg_NotFound(t *testing.T) {
	// Archive with an unrelated file → extraction fails clearly.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "ripgrep-x/README", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi\n"))
	_ = tw.Close()
	_ = gz.Close()

	if _, err := extractRg(buf.Bytes(), "ripgrep-x"); err == nil {
		t.Fatal("expected error when rg is absent from the archive")
	}
}
