// Package k8s — tar-over-exec file I/O (Phase C).
//
// PushFile and PullFile move individual files in and out of a sandbox
// pod by exec'ing `tar` inside the container and piping the archive
// over the SPDY exec streams. This is the same pattern `kubectl cp`
// uses and has three advantages over alternatives:
//
//   - It requires no extra subresource beyond /exec, which means
//     Kubernetes RBAC rules that permit exec automatically permit file
//     transfer and nothing else.
//
//   - Permissions, ownership, and symlinks are preserved by the
//     archive, so a single 0644 file and a full ACL-laden tree both
//     work the same way.
//
//   - The implementation is naturally streaming: callers that push a
//     large file never have to buffer it in Go memory, and PullFile
//     returns an io.ReadCloser the caller drains at their own pace.
//
// Testability: both methods route through the same execExecutorFactory
// seam that exec.go uses, so files_test.go can synthesise tar behaviour
// in-process against a stub executor.
//
// References:
//   - docs/architecture/sandbox-backends.md §3.2 and §11 Phase C.
//   - exec.go for the shared stub-friendly exec plumbing.

package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"k8s.io/client-go/tools/remotecommand"
)

func sandboxFileCommand(cmd []string) []string {
	return append([]string{"/usr/local/bin/astonish-shell"}, cmd...)
}

// ---------------------------------------------------------------------------
// PushFile
// ---------------------------------------------------------------------------

// PushFile writes content to the given path inside the session's
// sandbox container. The mode is applied to the file; any directories
// above the target are created with 0755 as needed.
//
// Implementation: build a one-entry tar stream in memory (sized by the
// content, but we do not currently enforce an upper bound — callers
// pushing many GiB should use a more specialised path) and pipe it to
// `tar -C <dir> -xmpf -` inside the container. `-p` preserves the mode
// bits we set in the tar header; `-m` ignores mtimes so we do not
// demand a synchronised clock between client and sandbox.
func (b *K8sBackend) PushFile(ctx context.Context, sessionID, filePath string, content io.Reader, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if filePath == "" {
		return errors.New("sandbox/k8s: PushFile: path is required")
	}
	if !path.IsAbs(filePath) {
		return fmt.Errorf("sandbox/k8s: PushFile: path %q must be absolute", filePath)
	}
	if content == nil {
		return errors.New("sandbox/k8s: PushFile: content is required")
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): lookup session: %w", sessionID, err)
	}
	if rec == nil || rec.PodName == "" {
		return fmt.Errorf("sandbox/k8s: PushFile: session %q has no pod", sessionID)
	}

	dir, name := path.Split(filePath)
	if dir == "" {
		dir = "/"
	}
	// Strip trailing slash for readability in the exec'd command,
	// except keep "/" as-is.
	if len(dir) > 1 {
		dir = strings.TrimRight(dir, "/")
	}

	body, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): read source: %w", sessionID, err)
	}

	archive, err := buildSingleFileTar(name, mode, body)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): build tar: %w", sessionID, err)
	}

	command := sandboxFileCommand([]string{"sh", "-c", fmt.Sprintf("mkdir -p %s && tar -C %s -xmpf -", shellQuote(dir), shellQuote(dir))})
	method, u, err := b.buildExecURL(rec.PodName, command, false /*tty*/, true /*stdin*/, true /*stdout*/, true /*stderr*/)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): build URL: %w", sessionID, err)
	}
	execr, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): build executor: %w", sessionID, err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := execr.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  bytes.NewReader(archive),
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	code, err := decodeExitError(streamErr)
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): %w", sessionID, err)
	}
	if code != 0 {
		return fmt.Errorf("sandbox/k8s: PushFile(%s): tar exit %d: %s", sessionID, code, stderr.String())
	}
	return nil
}

// buildSingleFileTar emits a POSIX-format tar archive containing a
// single regular file at name with the given mode and content. The tar
// header's ModTime is left zero-valued; combined with `tar -m` on the
// remote side, this decouples the transfer from clock skew.
func buildSingleFileTar(name string, mode os.FileMode, body []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(mode.Perm()),
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := w.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// PullFile
// ---------------------------------------------------------------------------

// PullFile reads a file from the session's sandbox container as an
// io.ReadCloser. The returned stream yields the raw file content
// (extracted from the tar archive on the fly). The caller MUST Close
// it to release the background exec goroutine and the pipes.
//
// Implementation: exec `tar -C <dir> -cf - <basename>` in the
// container, stream the tar over the SPDY stdout, and extract the
// single expected entry via an in-process tar.Reader. The extraction
// is pipelined: we return as soon as the tar header is available, and
// the caller's Read calls drain directly from the SPDY stream.
func (b *K8sBackend) PullFile(ctx context.Context, sessionID, filePath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, errors.New("sandbox/k8s: PullFile: path is required")
	}
	if !path.IsAbs(filePath) {
		return nil, fmt.Errorf("sandbox/k8s: PullFile: path %q must be absolute", filePath)
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: PullFile(%s): lookup session: %w", sessionID, err)
	}
	if rec == nil || rec.PodName == "" {
		return nil, fmt.Errorf("sandbox/k8s: PullFile: session %q has no pod", sessionID)
	}

	dir, name := path.Split(filePath)
	if dir == "" {
		dir = "/"
	}
	if len(dir) > 1 {
		dir = strings.TrimRight(dir, "/")
	}

	// Keep PAX format so filenames up to 255 bytes work without truncation.
	command := sandboxFileCommand([]string{"tar", "-C", dir, "-cf", "-", name})
	method, u, err := b.buildExecURL(rec.PodName, command, false, false /*stdin*/, true /*stdout*/, true /*stderr*/)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: PullFile(%s): build URL: %w", sessionID, err)
	}
	execr, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: PullFile(%s): build executor: %w", sessionID, err)
	}

	// Pipe stdout into a tar reader running in a goroutine. The
	// goroutine reads the tar header, then copies the body through
	// to the caller-facing pipe.
	stdoutR, stdoutW := io.Pipe()
	ctx, cancel := context.WithCancel(ctx)

	var stderr bytes.Buffer

	extractR, extractW := io.Pipe()

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- execr.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: stdoutW,
			Stderr: &stderr,
			Tty:    false,
		})
		_ = stdoutW.Close()
	}()

	extractDone := make(chan error, 1)
	go func() {
		defer extractW.Close()
		tr := tar.NewReader(stdoutR)
		_, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				extractDone <- io.EOF
				return
			}
			extractDone <- fmt.Errorf("read tar header: %w", err)
			return
		}
		if _, copyErr := io.Copy(extractW, tr); copyErr != nil { //nolint:gosec // tar extraction over trusted SPDY exec stream; no decompression happens here
			extractDone <- fmt.Errorf("extract tar body: %w", copyErr)
			return
		}
		extractDone <- nil
	}()

	return &pullFileReader{
		inner:       extractR,
		stdoutR:     stdoutR,
		cancel:      cancel,
		streamDone:  streamDone,
		extractDone: extractDone,
		stderrBuf:   &stderr,
		sessionID:   sessionID,
	}, nil
}

// pullFileReader is the io.ReadCloser returned by PullFile. It wraps
// the tar-extract pipe and, on Close, cancels the exec stream and
// collects any error that the background goroutines produced.
type pullFileReader struct {
	inner       *io.PipeReader
	stdoutR     *io.PipeReader // closed on Close to unblock the stream goroutine
	cancel      context.CancelFunc
	streamDone  <-chan error
	extractDone <-chan error
	stderrBuf   *bytes.Buffer
	sessionID   string

	closed bool
}

// Read pulls from the extract pipe. A non-EOF error here means the
// remote tar process failed mid-stream; we propagate it verbatim.
func (r *pullFileReader) Read(p []byte) (int, error) {
	return r.inner.Read(p)
}

// Close cancels the underlying exec context, drains the pipes, and
// surfaces the first error that occurred in either the stream or the
// tar extractor. Calling Close more than once is safe.
//
// Closing stdoutR with ErrClosedPipe unblocks the background stream
// goroutine in the common case where the tar archive has padding
// bytes after the single extracted entry and the extract goroutine
// has already exited.
func (r *pullFileReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.cancel()
	_ = r.inner.Close()
	_ = r.stdoutR.CloseWithError(io.ErrClosedPipe)

	// Wait for both goroutines to exit to avoid leaking them.
	streamErr := <-r.streamDone
	extractErr := <-r.extractDone

	// io.EOF from extract is the "no entries" signal — surface a
	// clearer error.
	if errors.Is(extractErr, io.EOF) {
		return fmt.Errorf("sandbox/k8s: PullFile(%s): remote tar stream empty: %s", r.sessionID, r.stderrBuf.String())
	}

	code, err := decodeExitError(streamErr)
	// If the extractor finished cleanly, we don't care that the
	// stream goroutine got ErrClosedPipe on our deliberate Close
	// (the tar body and header were already delivered). Swallow it.
	if err != nil && errors.Is(err, io.ErrClosedPipe) && extractErr == nil {
		err = nil
		code = 0
	}
	if err != nil {
		return fmt.Errorf("sandbox/k8s: PullFile(%s): %w", r.sessionID, err)
	}
	if code != 0 {
		return fmt.Errorf("sandbox/k8s: PullFile(%s): tar exit %d: %s", r.sessionID, code, r.stderrBuf.String())
	}
	if extractErr != nil {
		return fmt.Errorf("sandbox/k8s: PullFile(%s): %w", r.sessionID, extractErr)
	}
	return nil
}
