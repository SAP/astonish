package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	ManifestPath  = "manifest.json"
	ChecksumsPath = "checksums.json"
	// MaxMetadataBytes bounds each reserved JSON metadata entry, not payload files.
	MaxMetadataBytes = 16 << 20
)

type Compression string

const (
	CompressionGzip Compression = "gzip"
	CompressionNone Compression = "none"
)

type WriterOptions struct {
	Compression Compression
}

type Checksum struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Summary struct {
	Manifest  Manifest   `json:"manifest"`
	Checksums []Checksum `json:"checksums"`
	Encrypted bool       `json:"encrypted"`
}

type Writer struct {
	file        *os.File
	tar         *tar.Writer
	compressor  io.Closer
	compression Compression
	entries     []Checksum
	closed      bool
	streamErr   error
}

func Create(path string, opts ...WriterOptions) (*Writer, error) {
	options := WriterOptions{Compression: CompressionGzip}
	if len(opts) > 0 {
		options = opts[0]
	}
	compression, err := normalizeCompression(options.Compression)
	if err != nil {
		return nil, err
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	var out io.Writer = f
	var compressor io.Closer
	if compression == CompressionGzip {
		gz := gzip.NewWriter(f)
		out = gz
		compressor = gz
	}
	return &Writer{file: f, tar: tar.NewWriter(out), compressor: compressor, compression: compression}, nil
}

func ParseCompression(value string) (Compression, error) {
	return normalizeCompression(Compression(value))
}

// AddFile spools an unknown-size payload to a private temporary file. Tar needs
// the size before its header; payload memory stays bounded independent of size.
// Call AddFileSized when a fenced source already provides an exact size.
func (w *Writer) AddFile(path string, r io.Reader) (Checksum, error) {
	return w.AddFileContext(context.Background(), path, r)
}

func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.closeArchive()
}

func (w *Writer) CloseWithManifest(manifest Manifest) error {
	if w.closed {
		return errors.New("backup archive writer is closed")
	}
	if w.streamErr != nil {
		return errors.Join(w.streamErr, w.Close())
	}
	w.closed = true
	defer w.file.Close()

	manifest.Compression = string(w.compression)
	if err := manifest.Validate(); err != nil {
		_ = w.tar.Close()
		if w.compressor != nil {
			_ = w.compressor.Close()
		}
		return err
	}
	checksums := append([]Checksum(nil), w.entries...)
	sort.Slice(checksums, func(i, j int) bool { return checksums[i].Path < checksums[j].Path })

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = w.tar.Close()
		if w.compressor != nil {
			_ = w.compressor.Close()
		}
		return err
	}
	if err := w.writeTarFile(ManifestPath, append(manifestData, '\n')); err != nil {
		_ = w.tar.Close()
		if w.compressor != nil {
			_ = w.compressor.Close()
		}
		return err
	}

	checksumData, err := json.MarshalIndent(checksums, "", "  ")
	if err != nil {
		_ = w.tar.Close()
		if w.compressor != nil {
			_ = w.compressor.Close()
		}
		return err
	}
	if err := w.writeTarFile(ChecksumsPath, append(checksumData, '\n')); err != nil {
		_ = w.tar.Close()
		if w.compressor != nil {
			_ = w.compressor.Close()
		}
		return err
	}

	if err := w.tar.Close(); err != nil {
		return err
	}
	if w.compressor != nil {
		if err := w.compressor.Close(); err != nil {
			return err
		}
	}
	return w.file.Close()
}

func Inspect(path string, opts ...ReaderOptions) (Summary, error) {
	reader, err := OpenReader(path, opts...)
	if err != nil {
		return Summary{}, err
	}
	defer reader.Close()
	return reader.Summary()
}

func Verify(path string, opts ...ReaderOptions) (Summary, error) {
	reader, err := OpenReader(path, opts...)
	if err != nil {
		return Summary{}, err
	}
	defer reader.Close()

	summary, err := reader.Summary()
	if err != nil {
		return Summary{}, err
	}
	checksums := make(map[string]Checksum, len(summary.Checksums))
	for _, checksum := range summary.Checksums {
		if err := checksum.Validate(); err != nil {
			return Summary{}, err
		}
		checksums[checksum.Path] = checksum
	}
	seen := make(map[string]struct{}, len(summary.Checksums))
	if err := reader.ForEachFile(func(path string, r io.Reader) error {
		if path == ManifestPath || path == ChecksumsPath {
			return nil
		}
		checksum, ok := checksums[path]
		if !ok {
			return fmt.Errorf("backup archive contains unchecked file %q", path)
		}
		h := sha256.New()
		size, err := io.Copy(h, r)
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != checksum.SHA256 || size != checksum.Size {
			return fmt.Errorf("checksum mismatch for %q", path)
		}
		seen[path] = struct{}{}
		return nil
	}); err != nil {
		return Summary{}, err
	}
	for _, checksum := range summary.Checksums {
		if _, ok := seen[checksum.Path]; !ok {
			return Summary{}, fmt.Errorf("backup archive missing checksummed file %q", checksum.Path)
		}
	}
	return summary, nil
}

func (c Checksum) Validate() error {
	if err := validateArchivePath(c.Path); err != nil {
		return err
	}
	if c.Path == ManifestPath || c.Path == ChecksumsPath {
		return fmt.Errorf("checksum path %q is reserved", c.Path)
	}
	if c.Size < 0 {
		return fmt.Errorf("checksum size for %q cannot be negative", c.Path)
	}
	if len(c.SHA256) != sha256.Size*2 {
		return fmt.Errorf("checksum for %q must be a SHA-256 hex digest", c.Path)
	}
	if _, err := hex.DecodeString(c.SHA256); err != nil {
		return fmt.Errorf("checksum for %q must be hex: %w", c.Path, err)
	}
	return nil
}

func (w *Writer) writeTarFile(path string, data []byte) error {
	if len(data) > MaxMetadataBytes {
		return fmt.Errorf("backup metadata size limit exceeded for %q", path)
	}
	hdr := &tar.Header{
		Name:    path,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now().UTC(),
	}
	if err := w.tar.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := w.tar.Write(data)
	return err
}

func (w *Writer) closeArchive() error {
	if err := w.tar.Close(); err != nil {
		_ = w.closeCompressorAndFile()
		return err
	}
	return w.closeCompressorAndFile()
}

func (w *Writer) closeCompressorAndFile() error {
	if w.compressor != nil {
		if err := w.compressor.Close(); err != nil {
			_ = w.file.Close()
			return err
		}
	}
	return w.file.Close()
}

type Reader struct {
	path string
	opts ReaderOptions
}

func OpenReader(path string, opts ...ReaderOptions) (*Reader, error) {
	options := ReaderOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	if encrypted, err := IsEncryptedArchive(path); err != nil {
		return nil, err
	} else if encrypted && options.Passphrase == "" {
		return nil, errors.New("backup archive is encrypted; passphrase is required")
	}
	return &Reader{path: path, opts: options}, nil
}

func (r *Reader) Close() error {
	return nil
}

func (r *Reader) Summary() (Summary, error) {
	encrypted, err := IsEncryptedArchive(r.path)
	if err != nil {
		return Summary{}, err
	}
	files := make(map[string][]byte)
	if err := r.ForEachFile(func(path string, file io.Reader) error {
		if path != ManifestPath && path != ChecksumsPath {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(file, MaxMetadataBytes+1))
		if err != nil {
			return err
		}
		if len(data) > MaxMetadataBytes {
			return fmt.Errorf("backup metadata size limit exceeded for %q", path)
		}
		files[path] = data
		return nil
	}); err != nil {
		return Summary{}, err
	}
	manifestData, ok := files[ManifestPath]
	if !ok {
		return Summary{}, errors.New("backup archive missing manifest.json")
	}
	checksumData, ok := files[ChecksumsPath]
	if !ok {
		return Summary{}, errors.New("backup archive missing checksums.json")
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Summary{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Summary{}, err
	}

	var checksums []Checksum
	if err := json.Unmarshal(checksumData, &checksums); err != nil {
		return Summary{}, fmt.Errorf("decode checksums: %w", err)
	}
	return Summary{Manifest: manifest, Checksums: checksums, Encrypted: encrypted}, nil
}

func (r *Reader) ForEachFile(fn func(path string, r io.Reader) error) error {
	payload, closePayload, err := r.openPayload()
	if err != nil {
		return err
	}
	defer closePayload()

	seen := make(map[string]struct{})
	tr := tar.NewReader(payload)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("backup archive contains non-file entry %q", hdr.Name)
		}
		if err := validateArchivePath(hdr.Name); err != nil {
			return err
		}
		if _, exists := seen[hdr.Name]; exists {
			return fmt.Errorf("backup archive contains duplicate file %q", hdr.Name)
		}
		seen[hdr.Name] = struct{}{}
		if err := fn(hdr.Name, tr); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) openPayload() (io.Reader, func(), error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, nil, err
	}
	payload, err := archiveFilePayloadReader(f, r.opts.Passphrase)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	archiveReader, closeArchive, err := archivePayloadReader(payload)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return archiveReader, func() {
		if closeArchive != nil {
			closeArchive()
		}
		_ = f.Close()
	}, nil
}

func ReadArchiveFiles(path string, opts ...ReaderOptions) (map[string][]byte, error) {
	reader, err := OpenReader(path, opts...)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	files := make(map[string][]byte)
	if err := reader.ForEachFile(func(path string, r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

func archiveFilePayloadReader(r io.Reader, passphrase string) (io.Reader, error) {
	br := bufio.NewReader(r)
	prefix, err := br.Peek(len(encryptedArchiveMagic))
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	if string(prefix) != encryptedArchiveMagic {
		return br, nil
	}
	data, err := io.ReadAll(br)
	if err != nil {
		return nil, err
	}
	plaintext, err := decryptArchivePayload(data, passphrase)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(plaintext), nil
}

func archivePayloadReader(r io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReader(r)
	peek, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, nil, err
	}
	if len(peek) == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip-compressed backup archive: %w", err)
		}
		return gz, func() { _ = gz.Close() }, nil
	}
	return br, nil, nil
}

func normalizeCompression(compression Compression) (Compression, error) {
	if compression == "" {
		return CompressionGzip, nil
	}
	switch compression {
	case CompressionGzip, CompressionNone:
		return compression, nil
	default:
		return "", fmt.Errorf("unsupported backup compression %q", compression)
	}
}

func validateArchivePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("archive path is required")
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "\\") {
		return fmt.Errorf("archive path %q must be relative and must not contain parent traversal", path)
	}
	return nil
}
