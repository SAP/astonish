package sandbox

import (
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

type kindOnlyBackend struct {
	interfaceTestStub
	kind BackendKind
}

func (b kindOnlyBackend) Kind() BackendKind { return b.kind }
