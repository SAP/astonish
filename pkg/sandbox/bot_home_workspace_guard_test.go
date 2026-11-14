package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type workspaceApproval struct {
	bytes.Buffer
	closed bool
}

func (w *workspaceApproval) Close() error { w.closed = true; return nil }
func TestBotHomeWorkspaceCommitGuard(t *testing.T) {
	for _, mode := range []string{"live", "revoked", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			ctx = WithBotHomeWorkspaceCommitGuard(ctx, func(context.Context) error {
				calls++
				if mode == "revoked" {
					return errors.New("revoked")
				}
				return nil
			})
			if mode == "cancelled" {
				cancel()
			}
			a := &workspaceApproval{}
			var out bytes.Buffer
			s := &WorkspaceCommitStream{Context: ctx, Approve: a, Output: &out}
			_, err := s.Write([]byte("WORKSPACE_RE"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Write([]byte("ADY\n"))
			if mode == "live" {
				if err != nil || a.String() != "commit\n" || calls != 1 {
					t.Fatal(err, a.String(), calls)
				}
			} else if err == nil || a.Len() != 0 || !a.closed {
				t.Fatal("late publication accepted", err)
			}
		})
	}
}
func TestBotHomeWorkspaceCommitStreamBoundedAndIncomplete(t *testing.T) {
	a := &workspaceApproval{}
	s := &WorkspaceCommitStream{Context: t.Context(), Approve: a, Output: io.Discard}
	if _, err := s.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if s.Finish() == nil {
		t.Fatal("incomplete response accepted")
	}
}
