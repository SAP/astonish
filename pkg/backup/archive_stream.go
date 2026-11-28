package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const archiveCopyBufferSize = 32 << 10

// AddFileContext is AddFile with cancellation between reads. Callers own and
// must close their source (including closing blocked network reads on cancel).
// Temporary payloads use mode 0600 beside the output and are always removed.
func (w *Writer) AddFileContext(ctx context.Context, path string, r io.Reader) (Checksum, error) {
	if err := w.validatePayload(ctx, path); err != nil {
		return Checksum{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(w.file.Name()), ".astonish-backup-payload-*")
	if err != nil {
		return Checksum{}, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	n, err := io.CopyBuffer(f, archiveContextReader{ctx, r}, make([]byte, archiveCopyBufferSize))
	if err != nil {
		return Checksum{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Checksum{}, err
	}
	return w.AddFileSized(ctx, path, n, f)
}

// AddFileSized streams a source of exactly size bytes into a regular tar entry,
// hashing as it writes. It checks EOF after size bytes; callers must supply a
// file-bounded reader, not a multiplexed stream. Any failure after the header
// poisons the writer: CloseWithManifest cannot produce an apparently complete
// backup. This does not supply a filesystem or SQL consistency fence.
func (w *Writer) AddFileSized(ctx context.Context, path string, size int64, r io.Reader) (Checksum, error) {
	if err := w.validatePayload(ctx, path); err != nil {
		return Checksum{}, err
	}
	if size < 0 {
		return Checksum{}, errors.New("backup payload size cannot be negative")
	}
	fail := func(err error) (Checksum, error) {
		w.streamErr = fmt.Errorf("write backup payload %q: %w", path, err)
		return Checksum{}, w.streamErr
	}
	if err := w.tar.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: size, ModTime: time.Now().UTC()}); err != nil {
		return fail(err)
	}
	h := sha256.New()
	source := archiveContextReader{ctx, r}
	n, err := io.CopyBuffer(io.MultiWriter(w.tar, h), io.LimitReader(source, size), make([]byte, archiveCopyBufferSize))
	if err != nil {
		return fail(err)
	}
	if n != size {
		return fail(io.ErrUnexpectedEOF)
	}
	var extra [1]byte
	next, err := io.ReadFull(source, extra[:])
	if next != 0 {
		return fail(errors.New("payload exceeds declared size"))
	}
	if !errors.Is(err, io.EOF) {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	checksum := Checksum{Path: path, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	w.entries = append(w.entries, checksum)
	return checksum, nil
}

func (w *Writer) validatePayload(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return errors.New("backup archive writer is closed")
	}
	if w.streamErr != nil {
		return w.streamErr
	}
	if err := validateArchivePath(path); err != nil {
		return err
	}
	if path == ManifestPath || path == ChecksumsPath {
		return fmt.Errorf("%s is reserved", path)
	}
	for _, entry := range w.entries {
		if entry.Path == path {
			return fmt.Errorf("duplicate backup payload %q", path)
		}
	}
	return nil
}

type archiveContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
