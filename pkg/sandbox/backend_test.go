package sandbox

import (
	"context"
	"io"
	"os"
	"testing"
)

// TestBackendKindConstants sanity-checks that the kind constants have the
// expected string values — they are part of the public API (logged, exposed
// via Capabilities) and must not drift.
func TestBackendKindConstants(t *testing.T) {
	cases := []struct {
		got  BackendKind
		want string
	}{
		{BackendKindIncus, "incus"},
		{BackendKindDocker, "docker"},
		{BackendKindK8s, "k8s"},
		{BackendKindOpenShell, "openshell"},
		{BackendKindMock, "mock"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("BackendKind %q = %q, want %q", c.want, string(c.got), c.want)
		}
	}
}

// TestSessionStateConstants guards against accidental renames of the
// observable session states (SSE payloads, audit log entries).
func TestSessionStateConstants(t *testing.T) {
	cases := []struct {
		got  SessionState
		want string
	}{
		{SessionStateCreating, "creating"},
		{SessionStateRunning, "running"},
		{SessionStateStopped, "stopped"},
		{SessionStateEvicting, "evicting"},
		{SessionStateResuming, "resuming"},
		{SessionStateGone, "gone"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("SessionState %q = %q, want %q", c.want, string(c.got), c.want)
		}
	}
}

// TestSessionTypeConstants is the same guard for the two session flavors.
func TestSessionTypeConstants(t *testing.T) {
	if string(SessionTypeChat) != "chat" {
		t.Errorf("SessionTypeChat = %q, want %q", SessionTypeChat, "chat")
	}
	if string(SessionTypeFleet) != "fleet" {
		t.Errorf("SessionTypeFleet = %q, want %q", SessionTypeFleet, "fleet")
	}
}

// TestBackendInterfaceNilAssertion confirms the Backend interface is
// satisfiable (a nil pointer to a stub type that implements every method
// can be stored in a Backend variable). This is a compile-time guarantee:
// if the interface surface or any method signature drifts, the stub below
// stops compiling and the test fails.
func TestBackendInterfaceNilAssertion(t *testing.T) {
	var _ Backend = (*interfaceTestStub)(nil)
}

// interfaceTestStub exists only to prove the interface compiles. Its methods
// are all no-ops returning zero values; it is never instantiated beyond the
// nil assertion above.
type interfaceTestStub struct{}

// The method set below is mechanically aligned with the Backend interface.
// Any divergence will surface as a "does not implement Backend" error.

func (*interfaceTestStub) CreateSession(ctx context.Context, spec SessionSpec) (*Session, error) {
	return nil, nil
}
func (*interfaceTestStub) StartSession(ctx context.Context, sessionID string) error   { return nil }
func (*interfaceTestStub) StopSession(ctx context.Context, sessionID string) error    { return nil }
func (*interfaceTestStub) DestroySession(ctx context.Context, sessionID string) error { return nil }
func (*interfaceTestStub) SessionState(ctx context.Context, sessionID string) (SessionState, error) {
	return SessionStateRunning, nil
}
func (*interfaceTestStub) WaitForSessionReady(ctx context.Context, sessionID string) error {
	return nil
}
func (*interfaceTestStub) ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error) {
	return nil, nil
}
func (*interfaceTestStub) Exec(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
	return nil, nil
}
func (*interfaceTestStub) ExecInteractive(ctx context.Context, sessionID string, opts PTYSpec) (ExecStream, error) {
	return nil, nil
}
func (*interfaceTestStub) ExecStreaming(ctx context.Context, sessionID string, opts ExecStreamSpec) (ExecStream, error) {
	return nil, nil
}
func (*interfaceTestStub) PushFile(ctx context.Context, sessionID, path string, content io.Reader, mode os.FileMode) error {
	return nil
}
func (*interfaceTestStub) PullFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error) {
	return nil, nil
}
func (*interfaceTestStub) BuildTemplate(ctx context.Context, spec TemplateBuildSpec) (*TemplateArtifact, error) {
	return nil, nil
}
func (*interfaceTestStub) SaveSessionAsTemplate(ctx context.Context, sessionID string) (*TemplateArtifact, error) {
	return nil, nil
}
func (*interfaceTestStub) RefreshTemplate(ctx context.Context, templateID string) (*TemplateArtifact, error) {
	return nil, nil
}
func (*interfaceTestStub) DeleteTemplate(ctx context.Context, templateID string, force bool) error {
	return nil
}
func (*interfaceTestStub) EnsureOrgNetwork(ctx context.Context, orgSlug string) error { return nil }
func (*interfaceTestStub) DeleteOrgNetwork(ctx context.Context, orgSlug string) error { return nil }
func (*interfaceTestStub) ExposePort(ctx context.Context, sessionID string, port int, proto string) (*ExposedAddr, error) {
	return nil, nil
}
func (*interfaceTestStub) UnexposePort(ctx context.Context, sessionID string, port int) error {
	return nil
}
func (*interfaceTestStub) EnsureFleetContainer(ctx context.Context, spec FleetSpec) (*Session, error) {
	return nil, nil
}
func (*interfaceTestStub) Capabilities() BackendCapabilities { return BackendCapabilities{} }
func (*interfaceTestStub) Health(ctx context.Context) (*BackendHealth, error) {
	return nil, nil
}
func (*interfaceTestStub) Kind() BackendKind { return BackendKindMock }

func (*interfaceTestStub) ServerArchitecture() string { return "amd64" }
