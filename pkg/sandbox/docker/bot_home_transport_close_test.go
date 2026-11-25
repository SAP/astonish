package docker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These are local process/transport tests, not Docker or compiled-node evidence.
// A notification from Close establishes that the EOF waiter owns finish's once
// before concurrent callers attempt cancellation; no timing sleeps are needed.
type homeFilesJoinSignal struct {
	once   sync.Once
	closed chan struct{}
}

func (s *homeFilesJoinSignal) Write(p []byte) (int, error) { return len(p), nil }
func (s *homeFilesJoinSignal) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestBotHomeFilesDockerCloseMatrix(t *testing.T) {
	for _, owner := range []string{"session", "offline-attachment"} {
		for _, order := range []string{"close-first", "eof-join-first", "cancel-first"} {
			t.Run(owner+"/"+order, func(t *testing.T) {
				parent, stopParent := context.WithCancel(t.Context())
				defer stopParent()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				// exec eliminates a shell child which could outlive CommandContext.
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 30")
				stderr := &artifactBuffer{remaining: 4096}
				cmd.Stderr = stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				input := &homeFilesJoinSignal{closed: make(chan struct{})}
				var released atomic.Int32
				cleanupErr := errors.New("owned attachment cleanup sentinel")
				p := &homeFileProcess{sessionOwned: owner == "session", ctx: ctx, cancel: cancel, cmd: cmd, stderr: stderr, input: input, reader: bufio.NewReader(strings.NewReader("")), done: make(chan struct{})}
				// Even when supplied, a shared session must never release ownership.
				p.release = func(healthy bool) error {
					released.Add(1)
					if healthy {
						return errors.New("cancelled attachment marked reusable")
					}
					return cleanupErr
				}
				joined := make(chan error, 1)
				if order == "eof-join-first" {
					go func() { _, err := io.Copy(io.Discard, p); joined <- err }()
					select {
					case <-input.closed:
					case <-time.After(3 * time.Second):
						cancel()
						t.Fatal("EOF waiter did not enter join")
					}
				} else if order == "cancel-first" {
					cancel()
				}
				const closers = 8
				results := make(chan error, closers)
				for range closers {
					go func() { results <- p.Close() }()
				}
				for range closers {
					select {
					case err := <-results:
						if err == nil {
							t.Fatal("cancelled transport returned success")
						}
						if errors.Is(err, cleanupErr) != (owner == "offline-attachment") {
							t.Fatalf("wrong cleanup ownership: %v", err)
						}
					case <-time.After(3 * time.Second):
						cancel()
						t.Fatal("Close blocked behind pending join")
					}
				}
				if order == "eof-join-first" {
					select {
					case err := <-joined:
						if err == nil {
							t.Fatal("EOF hid cancelled transport")
						}
					case <-time.After(3 * time.Second):
						t.Fatal("EOF reader not joined")
					}
				}
				wantReleases := int32(0)
				if owner == "offline-attachment" {
					wantReleases = 1
				}
				if released.Load() != wantReleases {
					t.Fatalf("release calls=%d want=%d", released.Load(), wantReleases)
				}
				if cmd.ProcessState == nil || ctx.Err() == nil || parent.Err() != nil {
					t.Fatalf("wrong process/cancellation scope: joined=%t operation=%v parent=%v", cmd.ProcessState != nil, ctx.Err(), parent.Err())
				}
				select {
				case <-p.done:
				default:
					t.Fatal("close returned without finishing join")
				}
			})
		}
	}
}
