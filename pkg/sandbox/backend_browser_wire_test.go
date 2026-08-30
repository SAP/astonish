package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
)

func TestBuildBackendBrowserLaunchScript_UsesSandboxBrowser(t *testing.T) {
	script := buildBackendBrowserLaunchScript(browser.BrowserConfig{
		ViewportWidth:       1366,
		ViewportHeight:      768,
		FingerprintSeed:     "seed-1",
		FingerprintPlatform: "linux",
		Proxy:               "http://proxy.local:8080",
	}, 1366, 768)

	wants := []string{
		"HOME=/home/browser python3 -c 'from cloakbrowser.config import get_binary_path",
		"/home/browser/.cloakbrowser",
		"Xkasmvnc :0",
		"-websocketPort 6901",
		"resolution:\n    width: 1366\n    height: 768",
		"Xvfb :99",
		"--remote-debugging-port=9223",
		"socat TCP-LISTEN:9222",
		"--window-size=1366,768",
		"--fingerprint seed-1",
		"--fingerprint-platform linux",
		"--proxy-server=http://proxy.local:8080",
		"DONE: browser ready",
	}
	for _, want := range wants {
		if !strings.Contains(script, want) {
			t.Fatalf("launch script missing %q\nscript:\n%s", want, script)
		}
	}
}

func TestStartBackendBrowserReportsStderrAndExitCode(t *testing.T) {
	stream := &failedBrowserStream{stdout: strings.NewReader("partial stdout"), exitCode: 127}
	backend := &browserExecBackend{stream: stream, stderr: "No browser binary found"}

	_, err := startBackendBrowser(context.Background(), backend, "slides-pdf-user", browser.DefaultConfig())
	if err == nil {
		t.Fatal("expected launch error")
	}
	for _, want := range []string{"exit code 127", "partial stdout", "No browser binary found"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestWireBackendBrowserManager_K8sOnly(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := &SessionRegistry{}

	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, nil) {
		t.Fatal("WireBackendBrowserManager returned false for K8s backend")
	}
	if !mgr.SandboxEnabled {
		t.Fatal("manager SandboxEnabled not set")
	}
	if mgr.ContainerResolveFunc == nil || mgr.ContainerStartBrowserFunc == nil || mgr.ContainerDialFunc == nil {
		t.Fatal("manager browser callbacks were not wired")
	}

	mgr = browser.NewManager(browser.DefaultConfig())
	if WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindIncus}, reg, nil) {
		t.Fatal("WireBackendBrowserManager should not wire non-K8s backend")
	}
}

type browserExecBackend struct {
	interfaceTestStub
	stream ExecStream
	stderr string
}

func (b *browserExecBackend) ExecStreaming(_ context.Context, _ string, spec ExecStreamSpec) (ExecStream, error) {
	if spec.SeparateStderr == nil {
		return nil, errors.New("SeparateStderr was not configured")
	}
	_, _ = io.WriteString(spec.SeparateStderr, b.stderr)
	return b.stream, nil
}

type failedBrowserStream struct {
	stdout   io.Reader
	exitCode int
}

func (s *failedBrowserStream) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *failedBrowserStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *failedBrowserStream) Resize(_, _ int) error       { return nil }
func (s *failedBrowserStream) Wait() (int, error)          { return s.exitCode, nil }
func (s *failedBrowserStream) Close() error                { return nil }

type kindOnlyBackend struct {
	interfaceTestStub
	kind BackendKind
}

func (b kindOnlyBackend) Kind() BackendKind { return b.kind }
