// exec.go — Docker exec and file I/O methods for DockerBackend.
//
// All exec operations use `docker exec` which runs a process inside a running
// container. File transfers use `docker cp`.
//
// ExecInteractive and ExecStreaming return an execStream that wraps a docker
// exec subprocess with stdin/stdout piped through. The process is started as a
// background goroutine and the caller communicates via io.Reader/Writer pipes.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
)

// Exec runs a command non-interactively inside the container and returns the
// captured stdout/stderr + exit code. Blocks until the process exits or ctx is
// cancelled.
func (db *DockerBackend) Exec(ctx context.Context, sessionID string, opts sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cname := containerName(sessionID)

	args := buildDockerExecArgs(cname, opts.WorkDir, opts.Env, false, wrapShell(opts.Command))
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := isExitError(err, &exitErr); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("sandbox/docker: exec in %s: %w", sessionID, err)
		}
	}
	return &sandbox.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}, nil
}

// ExecInteractive starts a PTY-attached process inside the container.
// The returned ExecStream gives the caller bidirectional PTY I/O.
func (db *DockerBackend) ExecInteractive(ctx context.Context, sessionID string, opts sandbox.PTYSpec) (sandbox.ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cname := containerName(sessionID)

	// -i -t allocates a PTY and attaches stdin.
	args := []string{"exec", "-i", "-t"}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, cname)
	args = append(args, wrapShell(opts.Command)...)

	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, args...)
	return newExecStream(ctx, cmd, opts.SeparateStderr, true)
}

// ExecStreaming starts a non-interactive bidirectional streaming exec (no PTY).
// Used for machine-to-machine protocols (MCP JSON-RPC, node stdio).
func (db *DockerBackend) ExecStreaming(ctx context.Context, sessionID string, opts sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cname := containerName(sessionID)

	// -i without -t: interactive stdin but no PTY.
	args := []string{"exec", "-i"}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, cname)
	args = append(args, wrapShell(opts.Command)...)

	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, args...)
	return newExecStream(ctx, cmd, opts.SeparateStderr, false)
}

// PushFile writes content to a path inside the container.
// Uses `docker exec` with an in-container `sh -c 'cat > path'` pipeline to
// avoid the tar-file limitation of `docker cp` for non-root callers.
func (db *DockerBackend) PushFile(ctx context.Context, sessionID, path string, content io.Reader, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cname := containerName(sessionID)

	// Ensure parent directory exists inside the container.
	dir := path[:strings.LastIndexByte(path, '/')+1]
	if dir != "" && dir != "/" {
		if _, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
			"exec", cname, astonishShell, "mkdir", "-p", dir,
		); err != nil {
			return fmt.Errorf("sandbox/docker: PushFile mkdir %q in %s: %w", dir, sessionID, err)
		}
	}

	// Stream content via docker exec + stdin pipe.
	args := []string{
		"exec", "-i", cname, astonishShell,
		"sh", "-c", fmt.Sprintf("cat > %q && chmod %04o %q", path, mode, path),
	}
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, args...)
	cmd.Stdin = content
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandbox/docker: PushFile %q in %s: %w (stderr: %s)",
			path, sessionID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// PullFile reads a file from inside the container. Returns the raw bytes as an
// io.ReadCloser. The caller MUST close the returned reader.
func (db *DockerBackend) PullFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cname := containerName(sessionID)

	// Stream via docker exec + cat.
	args := []string{"exec", cname, astonishShell, "cat", path}
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath, args...)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: PullFile %q from %s: %w", path, sessionID, err)
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func wrapShell(command []string) []string {
	if len(command) == 0 {
		return []string{astonishShell}
	}
	if command[0] == astonishShell {
		return command
	}
	return append([]string{astonishShell}, command...)
}

// buildDockerExecArgs assembles the arguments for `docker exec` from the
// common fields (container name, workdir, env, command). Does not include
// -t/-i flags; callers add those.
func buildDockerExecArgs(cname, workDir string, env map[string]string, pty bool, command []string) []string {
	args := []string{"exec"}
	if pty {
		args = append(args, "-t")
	}
	args = append(args, "-i")
	if workDir != "" {
		args = append(args, "-w", workDir)
	}
	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, cname)
	args = append(args, command...)
	return args
}

// execStream wraps a docker exec subprocess and satisfies sandbox.ExecStream.
type execStream struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser // non-nil only when SeparateStderr != nil

	doneCh chan struct{}
	exit   int
	err    error
}

// newExecStream starts cmd and wires up stdin/stdout/stderr pipes.
// separateStderr, when non-nil, receives stderr separately.
func newExecStream(ctx context.Context, cmd *exec.Cmd, separateStderr io.Writer, pty bool) (*execStream, error) {
	_ = ctx // used by cmd context

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: exec stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: exec stdout pipe: %w", err)
	}

	es := &execStream{
		cmd:    cmd,
		stdin:  stdinPipe,
		stdout: stdoutPipe,
		doneCh: make(chan struct{}),
	}

	if separateStderr != nil {
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("sandbox/docker: exec stderr pipe: %w", err)
		}
		es.stderr = stderrPipe
		// Drain stderr into the caller-provided writer.
		go func() {
			_, _ = io.Copy(separateStderr, stderrPipe)
		}()
	} else if !pty {
		// Merge stderr into stdout for non-PTY non-separated case.
		cmd.Stderr = cmd.Stdout
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox/docker: start exec subprocess: %w", err)
	}

	// Wait in background; signal via doneCh.
	go func() {
		defer close(es.doneCh)
		exitErr := cmd.Wait()
		var exitE *exec.ExitError
		if exitErr != nil {
			if isExitError(exitErr, &exitE) {
				es.exit = exitE.ExitCode()
			} else {
				es.err = exitErr
			}
		}
	}()

	return es, nil
}

// Read reads from the exec's stdout.
func (es *execStream) Read(p []byte) (int, error) {
	return es.stdout.Read(p)
}

// Write writes to the exec's stdin.
func (es *execStream) Write(p []byte) (int, error) {
	return es.stdin.Write(p)
}

// Resize is a no-op for Docker exec. PTY resize requires a docker exec resize
// API call; implement as an enhancement if needed.
func (es *execStream) Resize(rows, cols int) error {
	return nil
}

// Wait blocks until the exec process exits and returns its exit code.
func (es *execStream) Wait() (int, error) {
	<-es.doneCh
	return es.exit, es.err
}

// Close terminates the subprocess and releases resources.
func (es *execStream) Close() error {
	_ = es.stdin.Close()
	if es.cmd.Process != nil {
		_ = es.cmd.Process.Kill()
	}
	<-es.doneCh
	return nil
}

// isExitError type-asserts an error to *exec.ExitError.
func isExitError(err error, target **exec.ExitError) bool {
	var e *exec.ExitError
	if ok := (err != nil); ok {
		if e2, ok2 := err.(*exec.ExitError); ok2 {
			if target != nil {
				*target = e2
			}
			return true
		}
		_ = e
	}
	return false
}
