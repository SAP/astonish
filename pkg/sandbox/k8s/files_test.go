// Package k8s — files_test.go drives PushFile and PullFile against a
// stub remoteExecutor so we can assert:
//
//   - ctx-cancellation short-circuit.
//   - session/pod lookup (error when session missing or PodName empty).
//   - Path validation (empty / non-absolute rejected).
//   - The tar archive we emit on stdin (PushFile) contains exactly
//     one regular file with the expected name, mode, and content.
//   - The exec'd command is `tar -C <dir> -x...` or `tar -C <dir>
//     -cf - <basename>` with the right flags and working directory.
//   - PullFile streams the tar body back to the caller: the returned
//     io.ReadCloser yields the exact file bytes, EOF ends the read,
//     and Close reconciles the stream / extract goroutines.
//   - Non-zero remote exit and transport failures surface through the
//     returned error (PushFile) or through Close (PullFile).
//
// All tests use the stubFactory helper from exec_test.go.

package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/SAP/astonish/pkg/sandbox"
)

// ---------------------------------------------------------------------------
// PushFile
// ---------------------------------------------------------------------------

func TestPushFile_ContextCancelledShortCircuits(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	stubFactory(t, b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := b.PushFile(ctx, "s", "/tmp/x", strings.NewReader("ignored"), 0o644)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("PushFile cancelled: got %v, want context.Canceled", err)
	}
}

func TestPushFile_ValidatesArgs(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	stubFactory(t, b, nil)
	seedSession(t, b, "s1", "astn-sess-s1")

	cases := []struct {
		name    string
		path    string
		content io.Reader
		want    string
	}{
		{"empty path", "", strings.NewReader("x"), "path is required"},
		{"relative path", "foo/bar", strings.NewReader("x"), "must be absolute"},
		{"nil content", "/tmp/x", nil, "content is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := b.PushFile(context.Background(), "s1", c.path, c.content, 0o644)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("PushFile %s: got %v, want %q", c.name, err, c.want)
			}
		})
	}
}

func TestPushFile_MissingSessionRejected(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	stubFactory(t, b, nil)
	err := b.PushFile(context.Background(), "nope", "/tmp/x", strings.NewReader("x"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "no pod") {
		t.Errorf("PushFile missing session: got %v, want no-pod error", err)
	}
}

func TestPushFile_EmitsSingleFileTarStream(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")

	var received []byte
	se := stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, opts.Stdin); err != nil {
			return err
		}
		received = buf.Bytes()
		return nil
	})

	payload := []byte("file contents\n")
	if err := b.PushFile(context.Background(), "s1", "/etc/astonish/hello.txt", bytes.NewReader(payload), 0o640); err != nil {
		t.Fatalf("PushFile: %v", err)
	}

	// Validate tar archive shape.
	tr := tar.NewReader(bytes.NewReader(received))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "hello.txt" {
		t.Errorf("tar header Name = %q, want hello.txt", hdr.Name)
	}
	if hdr.Mode != 0o640 {
		t.Errorf("tar header Mode = %o, want 0640", hdr.Mode)
	}
	if hdr.Typeflag != tar.TypeReg {
		t.Errorf("tar header Typeflag = %v, want regular", hdr.Typeflag)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read tar body: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("tar body = %q, want %q", body, payload)
	}
	// No second entry.
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("tar should have exactly one entry, got next err %v", err)
	}

	// Validate exec'd command: astonish-shell tar -C /etc/astonish -xmpf -
	q := se.url.Query()
	cmds := q["command"]
	want := []string{"/usr/local/bin/astonish-shell", "tar", "-C", "/etc/astonish", "-xmpf", "-"}
	if len(cmds) != len(want) {
		t.Fatalf("command = %v, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("command[%d] = %q, want %q", i, cmds[i], want[i])
		}
	}
}

func TestPushFile_RootDirectoryPath(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")
	se := stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		return nil
	})

	if err := b.PushFile(context.Background(), "s1", "/root-level", strings.NewReader("x"), 0o644); err != nil {
		t.Fatalf("PushFile: %v", err)
	}
	q := se.url.Query()
	cmds := q["command"]
	if len(cmds) < 4 || cmds[3] != "/" {
		t.Errorf("root dir: command[3] = %v, want /", cmds)
	}
}

func TestPushFile_NonZeroExitSurfaced(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		_, _ = opts.Stderr.Write([]byte("tar: permission denied"))
		return k8sexec.CodeExitError{Err: fmt.Errorf("exit 2"), Code: 2}
	})

	err := b.PushFile(context.Background(), "s1", "/root/blocked.txt", strings.NewReader("x"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "tar exit 2") || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("PushFile non-zero exit: got %v, want tar-exit + stderr", err)
	}
}

func TestPushFile_TransportErrorWrapped(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")
	boom := errors.New("connection closed")
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		return boom
	})

	err := b.PushFile(context.Background(), "s1", "/tmp/x", strings.NewReader("x"), 0o644)
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("PushFile transport err: got %v, want wraps %v", err, boom)
	}
}

// ---------------------------------------------------------------------------
// PullFile
// ---------------------------------------------------------------------------

func TestPullFile_ContextCancelledShortCircuits(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	stubFactory(t, b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.PullFile(ctx, "s", "/tmp/x")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("PullFile cancelled: got %v, want context.Canceled", err)
	}
}

func TestPullFile_ValidatesArgs(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	stubFactory(t, b, nil)
	seedSession(t, b, "s1", "astn-sess-s1")

	if _, err := b.PullFile(context.Background(), "s1", ""); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("PullFile empty path: got %v", err)
	}
	if _, err := b.PullFile(context.Background(), "s1", "rel/path"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("PullFile relative: got %v", err)
	}
	if _, err := b.PullFile(context.Background(), "nope", "/tmp/x"); err == nil || !strings.Contains(err.Error(), "no pod") {
		t.Errorf("PullFile missing session: got %v", err)
	}
}

func TestPullFile_StreamsFileContent(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")

	payload := []byte("pulled from the sandbox\n")
	se := stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		// Emit a one-entry tar archive on stdout.
		archive, err := buildSingleFileTar("hello.txt", 0o644, payload)
		if err != nil {
			return err
		}
		_, _ = opts.Stdout.Write(archive)
		return nil
	})

	rc, err := b.PullFile(context.Background(), "s1", "/etc/astonish/hello.txt")
	if err != nil {
		t.Fatalf("PullFile: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("pulled content = %q, want %q", got, payload)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Verify command shape.
	q := se.url.Query()
	cmds := q["command"]
	want := []string{"/usr/local/bin/astonish-shell", "tar", "-C", "/etc/astonish", "-cf", "-", "hello.txt"}
	if len(cmds) != len(want) {
		t.Fatalf("command = %v, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("command[%d] = %q, want %q", i, cmds[i], want[i])
		}
	}
}

func TestPullFile_NonZeroExitViaClose(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")

	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		// Write a valid tar so Read doesn't fail, then report non-zero exit.
		arch, _ := buildSingleFileTar("x", 0o644, []byte("content"))
		_, _ = opts.Stdout.Write(arch)
		_, _ = opts.Stderr.Write([]byte("tar: x: No such file"))
		return k8sexec.CodeExitError{Err: fmt.Errorf("exit 2"), Code: 2}
	})

	rc, err := b.PullFile(context.Background(), "s1", "/tmp/x")
	if err != nil {
		t.Fatalf("PullFile: %v", err)
	}
	_, _ = io.ReadAll(rc) // drain
	closeErr := rc.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "tar exit 2") || !strings.Contains(closeErr.Error(), "No such file") {
		t.Errorf("Close after non-zero exit: got %v, want tar-exit + stderr", closeErr)
	}
}

func TestPullFile_EmptyStreamSurfacesError(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")

	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		// No stdout, no error — simulates tar producing nothing.
		_, _ = opts.Stderr.Write([]byte("nothing here"))
		return nil
	})

	rc, err := b.PullFile(context.Background(), "s1", "/tmp/x")
	if err != nil {
		t.Fatalf("PullFile: %v", err)
	}
	_, _ = io.ReadAll(rc)
	closeErr := rc.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "empty") {
		t.Errorf("Close on empty stream: got %v, want empty-stream error", closeErr)
	}
}

func TestPullFile_DoubleCloseIsSafe(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		arch, _ := buildSingleFileTar("x", 0o644, []byte("y"))
		_, _ = opts.Stdout.Write(arch)
		return nil
	})
	rc, err := b.PullFile(context.Background(), "s1", "/tmp/x")
	if err != nil {
		t.Fatalf("PullFile: %v", err)
	}
	_, _ = io.ReadAll(rc)
	if err := rc.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestBuildSingleFileTar(t *testing.T) {
	data, err := buildSingleFileTar("foo.txt", 0o755, []byte("hello"))
	if err != nil {
		t.Fatalf("buildSingleFileTar: %v", err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "foo.txt" || hdr.Mode != 0o755 || hdr.Size != 5 {
		t.Errorf("header = %+v", hdr)
	}
	body, _ := io.ReadAll(tr)
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
}

// Validate round-trip via PushFile → PullFile using the same stub to
// show the two sides agree on the tar wire format.
func TestPushPullFile_RoundTrip(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	seedSession(t, b, "s1", "astn-sess-s1")

	var archive []byte
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		if opts.Stdin != nil {
			// Push side: capture the archive.
			buf, err := io.ReadAll(opts.Stdin)
			if err != nil {
				return err
			}
			archive = buf
			return nil
		}
		// Pull side: replay the archive.
		_, _ = opts.Stdout.Write(archive)
		return nil
	})

	payload := []byte("round trip payload\n")
	if err := b.PushFile(context.Background(), "s1", "/tmp/rt.txt", bytes.NewReader(payload), 0o600); err != nil {
		t.Fatalf("PushFile: %v", err)
	}
	rc, err := b.PullFile(context.Background(), "s1", "/tmp/rt.txt")
	if err != nil {
		t.Fatalf("PullFile: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-trip = %q, want %q", got, payload)
	}
}

// Sanity compile-time: pullFileReader satisfies io.ReadCloser.
var _ io.ReadCloser = (*pullFileReader)(nil)

// Use sandbox package to silence if imports shift during refactors.
var _ sandbox.BackendKind = sandbox.BackendKindK8s
