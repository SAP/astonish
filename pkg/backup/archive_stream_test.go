package backup

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"testing"
)

// Bounded reader requests make accidental whole-file buffering observable without
// relying on nondeterministic heap statistics or a huge resident test fixture.
type boundedArchiveReader struct {
	remaining int64
	maxRead   int
}

func (r *boundedArchiveReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		return 0, fmt.Errorf("unbounded read request: %d > %d", len(p), r.maxRead)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	for i := range p[:n] {
		p[i] = byte(i % 251)
	}
	r.remaining -= n
	return int(n), nil
}

func TestArchiveAddFileBoundedStreaming(t *testing.T) {
	for _, compression := range []Compression{CompressionNone, CompressionGzip} {
		t.Run(string(compression), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stream.backup")
			w, err := Create(path, WriterOptions{Compression: compression})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			const size = 17<<20 + 123
			c, err := w.AddFile("files/large.bin", &boundedArchiveReader{remaining: size, maxRead: 32 << 10})
			if err != nil {
				t.Fatal(err)
			}
			if c.Size != size {
				t.Fatalf("size = %d", c.Size)
			}
			m := NewManifest("sqlite", "logical", []Scope{{Kind: "platform"}})
			m.Entries = []Entry{{Path: c.Path, Kind: "file", Scope: m.Scopes[0]}}
			if err := w.CloseWithManifest(m); err != nil {
				t.Fatal(err)
			}
			summary, err := Verify(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(summary.Checksums) != 1 || summary.Checksums[0] != c {
				t.Fatalf("checksums: %+v", summary.Checksums)
			}
		})
	}
}

func TestArchiveWriterRejectsDuplicatePayload(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "duplicate.backup"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.AddFile("files/a", bytes.NewReader([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddFile("files/a", bytes.NewReader([]byte("b"))); err == nil {
		t.Fatal("duplicate payload accepted")
	}
}
