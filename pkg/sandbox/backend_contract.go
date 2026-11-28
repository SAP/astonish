// Package sandbox — Backend contract helper (Phase B.3).
//
// This file is deliberately NOT a _test.go file: RunBackendContract is a
// test helper that MUST be callable from external packages (e.g.,
// pkg/sandbox/mock) to validate their Backend implementations against a
// shared suite. Go only permits cross-package access to symbols in
// regular source files.
//
// The helper itself is a pure testing.T–based function; it carries no
// runtime cost in production binaries because it is only reachable from
// test code that imports it explicitly. The standard library uses the
// same pattern (e.g., net/http/httptest, testing/fstest).
//
// Every Backend implementation MUST pass RunBackendContract. The suite
// exercises interface-level behavior that is independent of daemon state:
//   - Kind() returns a stable non-empty identifier.
//   - Capabilities().Kind matches Kind().
//   - A cancelled context causes every state-mutating method to return
//     ctx.Err() before touching backend state.
//   - PullFile has the documented io.ReadCloser signature.
//
// Implementations provide a constructor closure via BackendContractFactory
// that returns a fresh Backend for each sub-test. Implementations that
// require external services (Incus daemon, K8s cluster) MAY make this
// factory return (nil, skipReason); the suite then t.Skip's those tests.

package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// BackendContractFactory returns a fresh Backend for one sub-test. If the
// second return value is non-empty, the sub-test is skipped with that
// reason (used by backends that require external services).
type BackendContractFactory func(t *testing.T) (Backend, string)

// RunBackendContract runs the full contract suite against a Backend
// implementation. Call this from the package that owns the implementation,
// passing a factory that constructs fresh backends.
//
// Example:
//
//	func TestIncusBackendContract(t *testing.T) {
//	    sandbox.RunBackendContract(t, func(t *testing.T) (sandbox.Backend, string) {
//	        return &sandbox.IncusBackend{...}, ""
//	    })
//	}
func RunBackendContract(t *testing.T, factory BackendContractFactory) {
	t.Helper()

	t.Run("Kind_Stable", func(t *testing.T) {
		b, skip := factory(t)
		if skip != "" {
			t.Skip(skip)
		}
		k := b.Kind()
		if k == "" {
			t.Fatal("Backend.Kind() returned empty string")
		}
		if string(k) != strings.ToLower(string(k)) {
			t.Errorf("Backend.Kind() = %q, want lowercase", k)
		}
	})

	t.Run("Capabilities_KindMatches", func(t *testing.T) {
		b, skip := factory(t)
		if skip != "" {
			t.Skip(skip)
		}
		if got, want := b.Capabilities().Kind, b.Kind(); got != want {
			t.Errorf("Capabilities().Kind = %q, want %q", got, want)
		}
	})

	t.Run("ContextCancelled_ShortCircuits", func(t *testing.T) {
		b, skip := factory(t)
		if skip != "" {
			t.Skip(skip)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Every state-mutating method must refuse a cancelled context.
		// Each assertion is wrapped so one failure doesn't mask the rest.
		check := func(name string, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: err = %v, want context.Canceled", name, err)
			}
		}

		_, err := b.CreateSession(ctx, SessionSpec{SessionID: "s", TemplateID: "t"})
		if homes, ok := b.(BotHomeBackend); ok {
			_, homeErr := homes.EnsureBotHome(ctx, BotHomeScope{UserID: "u", BotID: "b"})
			check("EnsureBotHome", homeErr)
		}
		check("CreateSession", err)
		check("StartSession", b.StartSession(ctx, "s"))
		check("StopSession", b.StopSession(ctx, "s"))
		check("DestroySession", b.DestroySession(ctx, "s"))
		_, err = b.ListSessions(ctx, SessionFilter{})
		check("ListSessions", err)
		_, err = b.SessionState(ctx, "s")
		check("SessionState", err)
		check("WaitForSessionReady", b.WaitForSessionReady(ctx, "s"))
		_, err = b.Exec(ctx, "s", ExecSpec{Command: []string{"true"}})
		check("Exec", err)
		_, err = b.ExecInteractive(ctx, "s", PTYSpec{Command: []string{"sh"}})
		check("ExecInteractive", err)
		_, err = b.ExecStreaming(ctx, "s", ExecStreamSpec{Command: []string{"cat"}})
		check("ExecStreaming", err)
		check("PushFile", b.PushFile(ctx, "s", "/x", strings.NewReader(""), os.FileMode(0o644)))
		_, err = b.PullFile(ctx, "s", "/x")
		check("PullFile", err)
		check("EnsureOrgNetwork", b.EnsureOrgNetwork(ctx, "org"))
		check("DeleteOrgNetwork", b.DeleteOrgNetwork(ctx, "org"))
		_, err = b.ExposePort(ctx, "s", 8080, "tcp")
		check("ExposePort", err)
		check("UnexposePort", b.UnexposePort(ctx, "s", 8080))
		_, err = b.EnsureFleetContainer(ctx, FleetSpec{FleetKey: "f", TemplateID: "t"})
		check("EnsureFleetContainer", err)
		_, err = b.Health(ctx)
		check("Health", err)
	})

	// Sanity: PullFile returning an io.ReadCloser must compile — purely a
	// type check, not a runtime assertion.
	t.Run("PullFile_ReturnsReadCloser", func(t *testing.T) {
		b, skip := factory(t)
		if skip != "" {
			t.Skip(skip)
		}
		var _ func(context.Context, string, string) (io.ReadCloser, error) = b.PullFile
	})
}
