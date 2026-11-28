package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveAddFileSizedExactAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int64
		body   string
		cancel bool
	}{
		{"short", 3, "ab", false}, {"long", 1, "ab", false}, {"cancel", 2, "ab", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := Create(filepath.Join(t.TempDir(), "bad.backup"))
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if _, err := w.AddFileSized(ctx, "files/a", tc.size, strings.NewReader(tc.body)); err == nil {
				t.Fatal("invalid stream accepted")
			}
			if !tc.cancel {
				if err := w.CloseWithManifest(NewManifest("sqlite", "logical", nil)); err == nil {
					t.Fatal("failed stream finalized")
				}
			}
		})
	}
	w, err := Create(filepath.Join(t.TempDir(), "good.backup"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	c, err := w.AddFileSized(context.Background(), "files/empty", 0, strings.NewReader(""))
	if err != nil || c.Size != 0 {
		t.Fatalf("empty file: %+v, %v", c, err)
	}
	const body = "exact known-size binary\x00\xff"
	c, err = w.AddFileSized(context.Background(), "files/exact", int64(len(body)), strings.NewReader(body))
	digest := sha256.Sum256([]byte(body))
	if err != nil || c.Size != int64(len(body)) || c.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("exact file checksum: %+v, %v", c, err)
	}
	if _, err := w.AddFileSized(context.Background(), "files/negative", -1, strings.NewReader("")); err == nil {
		t.Fatal("negative size accepted")
	}
}

type failingArchiveReader struct{}

func (failingArchiveReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestArchiveAddFileReadFailureLeavesWriterUsable(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.AddFile("files/a", failingArchiveReader{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read error: %v", err)
	}
	if _, err := w.AddFile("files/a", strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	m := NewManifest("sqlite", "logical", nil)
	if err := w.CloseWithManifest(m); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, ".astonish-backup-payload-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary payloads leaked: %v, %v", entries, err)
	}
}
