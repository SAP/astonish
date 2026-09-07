package docker

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestExtractOverlayTar_WritesRegularFilesAndSymlinks(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mustTarDir(t, tw, "a")
	mustTarFile(t, tw, "a/hello.txt", []byte("hi"))
	mustTarSymlink(t, tw, "a/link", "hello.txt")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractOverlayTar(bytes.NewReader(buf.Bytes()), dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "a/hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("content = %q", got)
	}
	link, err := os.Readlink(filepath.Join(dest, "a/link"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "hello.txt" {
		t.Fatalf("symlink = %q", link)
	}
}

func TestExtractOverlayTar_SkipsDevicesAndRejectsEscape(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0666}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extractOverlayTar(bytes.NewReader(buf.Bytes()), dest); err != nil {
		t.Fatal(err)
	}

	var escape bytes.Buffer
	tw = tar.NewWriter(&escape)
	mustTarFile(t, tw, "../etc/passwd", []byte("nope"))
	_ = tw.Close()
	if err := extractOverlayTar(bytes.NewReader(escape.Bytes()), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc/passwd")); err != nil {
		t.Fatal("cleaned path should land inside dest")
	}
}

func TestTarSafePath(t *testing.T) {
	root := t.TempDir()
	ok, err := tarSafePath(root, "usr/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "usr", "bin", "bash")
	if ok != want {
		t.Fatalf("path = %q want %q", ok, want)
	}
	cleaned, err := tarSafePath(root, "../outside")
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != filepath.Join(root, "outside") {
		t.Fatalf("cleaned = %q", cleaned)
	}
}

func TestSkipCaseCollidingTarName_DropsLWPHead(t *testing.T) {
	if !skipCaseCollidingTarName("/tmp/usr/bin/HEAD") {
		t.Fatal("HEAD must be skipped so it cannot clobber coreutils head")
	}
	if skipCaseCollidingTarName("/tmp/usr/bin/bash") {
		t.Fatal("bash should not be skipped")
	}
}

func TestCountingReader(t *testing.T) {
	var n atomic.Int64
	r := &countingReader{r: bytes.NewReader([]byte("abcd")), n: &n}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcd" || n.Load() != 4 {
		t.Fatalf("got %q n=%d", got, n.Load())
	}
}

func mustTarDir(t *testing.T, tw *tar.Writer, name string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
}

func mustTarFile(t *testing.T, tw *tar.Writer, name string, body []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
}

func mustTarSymlink(t *testing.T, tw *tar.Writer, name, target string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0777}); err != nil {
		t.Fatal(err)
	}
}
