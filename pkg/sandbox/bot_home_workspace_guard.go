package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
)

type workspaceCommitGuardKey struct{}

// WithBotHomeWorkspaceCommitGuard installs a live lease/permission check used
// after staging, while the backend's storage lock is held, immediately before
// durable publication. It is not serialized into workload input.
func WithBotHomeWorkspaceCommitGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, workspaceCommitGuardKey{}, guard)
}

// WorkspaceCommitStream is the trusted helper's two-way commit handshake.
// Workloads never execute in this helper or receive the control stream.
type WorkspaceCommitStream struct {
	Context   context.Context
	Output    io.Writer
	Approve   io.WriteCloser
	RunScript func([]byte) ([]byte, error)
	pending   []byte
}

func (s *WorkspaceCommitStream) Write(p []byte) (int, error) {
	s.pending = append(s.pending, p...)
	if len(s.pending) > 192<<20 {
		_ = s.Approve.Close()
		return 0, errors.New("sandbox: workspace protocol limit")
	}
	marker := []byte("WORKSPACE_READY\n")
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := s.pending[:i+1]
		s.pending = s.pending[i+1:]
		if bytes.Equal(line, marker) {
			err := s.Context.Err()
			if err == nil {
				if guard, ok := s.Context.Value(workspaceCommitGuardKey{}).(func(context.Context) error); ok {
					err = guard(s.Context)
				}
			}
			if err == nil {
				err = s.Context.Err()
			}
			if err != nil {
				_ = s.Approve.Close()
				return 0, err
			}
			if _, err = io.WriteString(s.Approve, "commit\n"); err != nil {
				return 0, err
			}
		} else if bytes.HasPrefix(line, []byte("WORKSPACE_SCRIPT ")) {
			if s.RunScript == nil {
				return 0, errors.New("sandbox: script capability unavailable")
			}
			response, err := s.RunScript(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("WORKSPACE_SCRIPT "))))
			if err != nil {
				_ = s.Approve.Close()
				return 0, err
			}
			if _, err = s.Approve.Write(append(bytes.TrimSpace(response), '\n')); err != nil {
				return 0, err
			}
		} else {
			if _, err := s.Output.Write(line); err != nil {
				return 0, err
			}
		}
	}
}
func (s *WorkspaceCommitStream) Finish() error {
	if len(s.pending) != 0 {
		return errors.New("sandbox: incomplete workspace protocol")
	}
	return nil
}
