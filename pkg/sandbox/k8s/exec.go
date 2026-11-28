// Package k8s — SPDY exec implementation (Phase C).
//
// This file implements Exec and ExecInteractive against a pod's /exec
// subresource via client-go's remotecommand SPDY transport. The design
// honours four invariants:
//
//  1. Context cancellation propagates through the stream (via
//     StreamWithContext) so a cancelled ctx tears down the remote
//     process and frees local goroutines.
//
//  2. Exit codes surface as *sandbox.ExecResult.ExitCode (for Exec) or
//     the int returned by ExecStream.Wait() (for ExecInteractive). The
//     remotecommand executor wraps non-zero exits in
//     k8s.io/client-go/util/exec.CodeExitError; we decode that here so
//     callers see a clean typed result instead of the wrapper error.
//
//  3. The heavy SPDY transport is constructed once per call (not once
//     per backend) so each exec gets its own dedicated connection and
//     resize queue. This matches kubectl's model.
//
//  4. Testability is a first-class concern. The real SPDY path is gated
//     behind execExecutorFactory so tests can inject a stub that runs
//     the StreamOptions in-memory and emulates exit codes without
//     standing up an API server. Production callers never touch this
//     hook.
//
// References:
//   - docs/architecture/sandbox-backends.md §3.2 and §11 Phase C.
//   - k8s.io/client-go/tools/remotecommand.NewSPDYExecutor
//   - k8s.io/client-go/util/exec.CodeExitError

package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/SAP/astonish/pkg/sandbox"
)

// ---------------------------------------------------------------------------
// Executor factory (test seam)
// ---------------------------------------------------------------------------

// remoteExecutor is the minimal subset of remotecommand.Executor we use.
// Declaring it locally means tests can supply a stub that satisfies this
// two-method interface without pulling in the full SPDY stack.
type remoteExecutor interface {
	StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error
}

// execExecutorFactory builds a remoteExecutor for a concrete pod/
// container/command invocation. The factory signature mirrors what we
// would pass to NewSPDYExecutor but returns our local interface to keep
// callers testable.
type execExecutorFactory func(
	restCfg *rest.Config,
	method string,
	u *url.URL,
) (remoteExecutor, error)

// defaultExecExecutorFactory is the production factory. It constructs a
// SPDY-backed remotecommand executor.
func defaultExecExecutorFactory(restCfg *rest.Config, method string, u *url.URL) (remoteExecutor, error) {
	return remotecommand.NewSPDYExecutor(restCfg, method, u)
}

// ---------------------------------------------------------------------------
// URL builder
// ---------------------------------------------------------------------------

// buildExecURL constructs the /pods/<name>/exec subresource URL with
// the PodExecOptions encoded as query parameters. Returns the method
// ("POST") and the fully-qualified URL.
//
// The URL is built directly from b.restConfig.Host rather than via
// client-go's RESTClient because the fake clientset used in tests does
// not wire up a RESTClient. Building the URL ourselves keeps the
// test-seam clean while still producing the canonical
// /api/v1/namespaces/<ns>/pods/<pod>/exec?<params> form the API server
// expects.
func (b *K8sBackend) buildExecURL(
	podName string,
	command []string,
	tty bool,
	stdin, stdout, stderr bool,
) (string, *url.URL, error) {
	if b.client == nil {
		return "", nil, errors.New("sandbox/k8s: no Kubernetes client configured")
	}
	if b.restConfig == nil || b.restConfig.Host == "" {
		return "", nil, errors.New("sandbox/k8s: RESTConfig is required for exec; construct K8sBackend via k8s.New with Config.RESTConfig")
	}

	base, err := url.Parse(b.restConfig.Host)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox/k8s: parse RESTConfig.Host: %w", err)
	}
	base.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec",
		b.cfg.Namespace, podName)

	opts := &corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     stdin,
		Stdout:    stdout,
		Stderr:    stderr,
		TTY:       tty,
	}
	params, err := encodePodExecOptions(opts)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox/k8s: encode PodExecOptions: %w", err)
	}
	base.RawQuery = params.Encode()
	return "POST", base, nil
}

// encodePodExecOptions turns PodExecOptions into url.Values using the
// API-server's canonical parameter codec. This is how kubectl builds the
// query string and what we want to exercise in tests.
func encodePodExecOptions(opts *corev1.PodExecOptions) (url.Values, error) {
	v := url.Values{}
	if opts.Container != "" {
		v.Set("container", opts.Container)
	}
	if opts.Stdin {
		v.Set("stdin", "true")
	}
	if opts.Stdout {
		v.Set("stdout", "true")
	}
	if opts.Stderr {
		v.Set("stderr", "true")
	}
	if opts.TTY {
		v.Set("tty", "true")
	}
	for _, c := range opts.Command {
		v.Add("command", c)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Exec (non-interactive)
// ---------------------------------------------------------------------------

// Exec runs a command non-interactively inside the session's sandbox
// container. Stdout and stderr are captured separately; the exit code
// surfaces as ExecResult.ExitCode. A non-zero exit code is NOT a Go
// error — callers inspect ExecResult.ExitCode.
//
// Transport errors (connection failures, context cancellation, protocol
// violations) DO surface as errors. Honours ctx cancellation throughout.
func (b *K8sBackend) Exec(ctx context.Context, sessionID string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(opts.Command) == 0 {
		return nil, errors.New("sandbox/k8s: Exec: Command is required")
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: Exec(%s): lookup session: %w", sessionID, err)
	}
	if rec == nil || rec.PodName == "" {
		return nil, fmt.Errorf("sandbox/k8s: Exec: session %q has no pod", sessionID)
	}

	opts.Command, opts.WorkDir, err = b.homeExecCommand(ctx, sessionID, opts.Command, opts.WorkDir)
	if err != nil {
		return nil, err
	}
	res, err := b.execInPod(ctx, rec.PodName, opts)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: Exec(%s): %w", sessionID, err)
	}
	return res, nil
}

// execInPod runs a non-interactive exec against a specific pod name,
// bypassing the session registry. Used internally by template.go where
// the builder pod is not a registered session, and by Exec after it has
// resolved the session → pod name mapping.
//
// Like Exec, execInPod honours ctx cancellation, wraps the command with
// a shell when WorkDir or Env is non-empty, and surfaces non-zero exits
// via ExecResult.ExitCode (errors are reserved for transport failures).
func (b *K8sBackend) execInPod(ctx context.Context, podName string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if podName == "" {
		return nil, errors.New("sandbox/k8s: execInPod: podName is required")
	}
	if len(opts.Command) == 0 {
		return nil, errors.New("sandbox/k8s: execInPod: Command is required")
	}

	// Wrap the command with a shell that honours WorkDir and Env, since
	// PodExecOptions has no such fields. We prepend `cd <workdir>;
	// export K=V; ...` then exec the user command. The exec(1) tail
	// keeps PID 1 semantics intact for signal propagation.
	command := wrapCommand(opts.Command, opts.WorkDir, opts.Env)

	method, u, err := b.buildExecURL(podName, command, false /*tty*/, opts.Stdin != nil, true, true)
	if err != nil {
		return nil, fmt.Errorf("build URL: %w", err)
	}

	execr, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return nil, fmt.Errorf("build executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := execr.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	code, err := decodeExitError(streamErr)
	if err != nil {
		return nil, err
	}
	return &sandbox.ExecResult{
		ExitCode: code,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}, nil
}

// ---------------------------------------------------------------------------
// ExecInteractive (PTY)
// ---------------------------------------------------------------------------

// ExecInteractive starts a PTY-attached process inside the session's
// sandbox container. Returns a sandbox.ExecStream the caller wires to
// their local terminal: reads yield combined stdout+stderr (or separate
// stderr if PTYSpec.SeparateStderr is set), writes forward to stdin,
// Resize pushes TerminalSize events onto the SPDY resize channel.
//
// The stream goroutine runs StreamWithContext in the background; Close
// cancels the context; Wait blocks until the process exits and returns
// the decoded exit code.
func (b *K8sBackend) ExecInteractive(ctx context.Context, sessionID string, opts sandbox.PTYSpec) (sandbox.ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(opts.Command) == 0 {
		return nil, errors.New("sandbox/k8s: ExecInteractive: Command is required")
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecInteractive(%s): lookup session: %w", sessionID, err)
	}
	if rec == nil || rec.PodName == "" {
		return nil, fmt.Errorf("sandbox/k8s: ExecInteractive: session %q has no pod", sessionID)
	}

	opts.Command, opts.WorkDir, err = b.homeExecCommand(ctx, sessionID, opts.Command, opts.WorkDir)
	if err != nil {
		return nil, err
	}
	command := wrapCommand(opts.Command, opts.WorkDir, opts.Env)

	wantStderr := opts.SeparateStderr != nil
	method, u, err := b.buildExecURL(rec.PodName, command, true /*tty*/, true, true, wantStderr)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecInteractive(%s): build URL: %w", sessionID, err)
	}

	execr, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecInteractive(%s): build executor: %w", sessionID, err)
	}

	// Pipes bridge the caller's Read/Write against the SPDY stream.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	streamCtx, cancel := context.WithCancel(ctx)
	stream := &k8sExecStream{
		stdinW:   stdinW,
		stdoutR:  stdoutR,
		cancel:   cancel,
		done:     make(chan struct{}),
		resizeCh: make(chan remotecommand.TerminalSize, 4),
	}
	// Seed an initial resize if the caller supplied dims.
	if opts.Rows > 0 && opts.Cols > 0 {
		select {
		case stream.resizeCh <- remotecommand.TerminalSize{Width: uint16(opts.Cols), Height: uint16(opts.Rows)}:
		default:
		}
	}

	var stderrW io.Writer
	if wantStderr {
		stderrW = opts.SeparateStderr
	}

	go func() {
		defer close(stream.done)
		defer stdoutW.Close()
		defer stdinR.Close()
		err := execr.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:             stdinR,
			Stdout:            stdoutW,
			Stderr:            stderrW,
			Tty:               true,
			TerminalSizeQueue: stream,
		})
		code, decodeErr := decodeExitError(err)
		stream.mu.Lock()
		stream.exitCode = code
		stream.exitErr = decodeErr
		stream.mu.Unlock()
	}()

	return stream, nil
}

// ExecStreaming starts a non-interactive streaming process (no PTY) inside the
// session's pod. Used for machine-to-machine protocols (MCP JSON-RPC, NDJSON)
// that need a long-running bidirectional stdin/stdout stream without terminal
// processing.
func (b *K8sBackend) ExecStreaming(ctx context.Context, sessionID string, opts sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(opts.Command) == 0 {
		return nil, errors.New("sandbox/k8s: ExecStreaming: Command is required")
	}

	rec, err := b.sessions.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecStreaming(%s): lookup session: %w", sessionID, err)
	}
	if rec == nil || rec.PodName == "" {
		return nil, fmt.Errorf("sandbox/k8s: ExecStreaming: session %q has no pod", sessionID)
	}

	opts.Command, opts.WorkDir, err = b.homeExecCommand(ctx, sessionID, opts.Command, opts.WorkDir)
	if err != nil {
		return nil, err
	}
	command := wrapCommand(opts.Command, opts.WorkDir, opts.Env)

	wantStderr := opts.SeparateStderr != nil
	method, u, err := b.buildExecURL(rec.PodName, command, false /*tty*/, true, true, wantStderr)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecStreaming(%s): build URL: %w", sessionID, err)
	}

	execr, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: ExecStreaming(%s): build executor: %w", sessionID, err)
	}

	// Pipes bridge the caller's Read/Write against the SPDY stream.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	streamCtx, cancel := context.WithCancel(ctx)
	stream := &k8sExecStream{
		stdinW:   stdinW,
		stdoutR:  stdoutR,
		cancel:   cancel,
		done:     make(chan struct{}),
		resizeCh: make(chan remotecommand.TerminalSize, 4), // unused but needed for interface
	}

	var stderrW io.Writer
	if wantStderr {
		stderrW = opts.SeparateStderr
	}

	go func() {
		defer close(stream.done)
		defer stdoutW.Close()
		defer stdinR.Close()
		err := execr.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: stdoutW,
			Stderr: stderrW,
			Tty:    false, // No PTY — raw stdin/stdout for machine protocols
		})
		code, decodeErr := decodeExitError(err)
		stream.mu.Lock()
		stream.exitCode = code
		stream.exitErr = decodeErr
		stream.mu.Unlock()
	}()

	return stream, nil
}

// ---------------------------------------------------------------------------
// k8sExecStream: sandbox.ExecStream + TerminalSizeQueue
// ---------------------------------------------------------------------------

// k8sExecStream wraps a background StreamWithContext goroutine and
// exposes sandbox.ExecStream to the caller. It also implements
// remotecommand.TerminalSizeQueue so Resize events flow back to the
// server over the SPDY channel.
type k8sExecStream struct {
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader

	cancel   context.CancelFunc
	done     chan struct{}
	resizeCh chan remotecommand.TerminalSize

	mu       sync.Mutex
	exitCode int
	exitErr  error
	closed   bool
}

// Read pulls bytes from the remote stdout (and merged stderr when
// SeparateStderr was nil). Returns io.EOF when the remote stream closes.
func (s *k8sExecStream) Read(p []byte) (int, error) {
	return s.stdoutR.Read(p)
}

// Write forwards bytes to the remote stdin.
func (s *k8sExecStream) Write(p []byte) (int, error) {
	return s.stdinW.Write(p)
}

// Resize posts a new terminal size. Non-blocking: if the queue is full,
// the oldest pending resize is dropped. This matches kubectl's
// best-effort semantics — terminal sizes are idempotent and the next
// resize will reconcile.
func (s *k8sExecStream) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("sandbox/k8s: Resize: rows and cols must be positive")
	}
	sz := remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	select {
	case s.resizeCh <- sz:
	default:
		// Drop-oldest: drain one, then push. This keeps Resize
		// non-blocking even under a stuck consumer.
		select {
		case <-s.resizeCh:
		default:
		}
		select {
		case s.resizeCh <- sz:
		default:
		}
	}
	return nil
}

// Next returns the next terminal size, or nil when the stream closes.
// Called by remotecommand.StreamOptions.TerminalSizeQueue.
func (s *k8sExecStream) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-s.resizeCh:
		return &sz
	case <-s.done:
		return nil
	}
}

// Wait blocks until the remote process exits, returns the exit code
// and any transport error.
func (s *k8sExecStream) Wait() (int, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode, s.exitErr
}

// Close cancels the stream context, closes the stdin pipe, and drains
// the background goroutine. Safe to call multiple times.
func (s *k8sExecStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	_ = s.stdinW.Close()
	_ = s.stdoutR.Close()
	<-s.done
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// decodeExitError translates a remotecommand stream error into an
// (exitCode, transportError) pair:
//
//   - nil error           → (0, nil)
//   - CodeExitError(N)    → (N, nil)  — process exited with code N
//   - anything else       → (-1, err) — transport / protocol failure
func decodeExitError(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var codeErr k8sexec.CodeExitError
	if errors.As(err, &codeErr) {
		return codeErr.ExitStatus(), nil
	}
	return -1, err
}

// homeExecCommand resolves the immutable retained binding, never inferring it
// from chat mode or HOME supplied by a caller.
func (b *K8sBackend) homeExecCommand(ctx context.Context, id string, command []string, workDir string) ([]string, string, error) {
	home, err := b.sessionBotHome(ctx, id, nil, true)
	if err != nil {
		return nil, "", err
	}
	if home != nil {
		return sandbox.RetainedBotHomeCommand(command, workDir), "", nil
	}
	return command, workDir, nil
}

// wrapCommand emits a /bin/sh -c wrapper that applies WorkDir and Env
// before exec'ing the user's command. Env values are single-quoted and
// any embedded single quotes are escaped.
//
// When WorkDir is empty and Env is empty the user command is returned
// verbatim (no shell indirection), preserving exit-code semantics and
// signal handling for plain `exec ["bash", "-lc", ...]` calls.
func wrapCommand(command []string, workDir string, env map[string]string) []string {
	if workDir == "" && len(env) == 0 {
		return command
	}
	// Build: cd <workdir> && export K='V' K2='V2' && exec cmd arg...
	var buf bytes.Buffer
	if workDir != "" {
		buf.WriteString("cd ")
		buf.WriteString(shellQuote(workDir))
		buf.WriteString(" && ")
	}
	if len(env) > 0 {
		buf.WriteString("export")
		// Sort for determinism.
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			buf.WriteByte(' ')
			buf.WriteString(k)
			buf.WriteByte('=')
			buf.WriteString(shellQuote(env[k]))
		}
		buf.WriteString(" && ")
	}
	buf.WriteString("exec")
	for _, a := range command {
		buf.WriteByte(' ')
		buf.WriteString(shellQuote(a))
	}
	return []string{"/bin/sh", "-c", buf.String()}
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quotes using the standard '"'"' idiom.
func shellQuote(s string) string {
	var b bytes.Buffer
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString(`'"'"'`)
			continue
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
	return b.String()
}

// sortStrings is a dependency-free string sort (we don't want to drag
// "sort" into this small helper; callers pass tiny slices).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
