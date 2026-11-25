package backup

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveSummaryRejectsOversizedMetadata(t *testing.T) {
	for _, name := range []string{ManifestPath, ChecksumsPath} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oversized.backup")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			tw := tar.NewWriter(f)
			const size = 16<<20 + 1
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(tw, &boundedArchiveReader{remaining: size, maxRead: 32 << 10}); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(path)
			if err == nil || !strings.Contains(err.Error(), "metadata size limit") {
				t.Fatalf("expected bounded metadata rejection, got %v", err)
			}
		})
	}
}
