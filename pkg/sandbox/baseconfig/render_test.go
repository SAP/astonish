package baseconfig

import (
	"strings"
	"testing"
)

func TestRender_CoreToolsProducesExpectedCommands(t *testing.T) {
	cfg := BaseConfig{
		Core:         true,
		Architecture: "amd64",
		Browser:      BrowserConfig{Engine: "none"},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	expected := []string{
		"apt-get update",
		"apt-get install -y git",
		"ffmpeg",
		"ripgrep",
		"nodejs",
		"curl -LsSf https://astral.sh/uv/install.sh | sh",
		"apt-get clean",
	}
	joined := strings.Join(steps, "\n")
	for _, needle := range expected {
		if !strings.Contains(joined, needle) {
			t.Errorf("rendered steps missing %q:\n%s", needle, joined)
		}
	}
}

func TestRender_BrowserDefault(t *testing.T) {
	cfg := BaseConfig{
		Core:         true,
		Architecture: "amd64",
		Browser:      BrowserConfig{Engine: "default"},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "chromium") {
		t.Errorf("expected chromium install for engine=default, got:\n%s", joined)
	}
	// Render uses K8s sandbox-base distro (Debian bookworm) — verify
	// Debian-specific package names are used, not Ubuntu noble ones.
	if strings.Contains(joined, "libasound2t64") {
		t.Errorf("expected libasound2 (Debian), got libasound2t64 (Ubuntu)")
	}
	if strings.Contains(joined, "libjpeg-turbo8") {
		t.Errorf("expected libjpeg62-turbo (Debian), got libjpeg-turbo8 (Ubuntu)")
	}
	if strings.Contains(joined, "ppa:xtradeb") {
		t.Errorf("Debian bookworm render should not use xtradeb PPA")
	}
	if !strings.Contains(joined, "kasmvncserver_bookworm_") {
		t.Errorf("expected KasmVNC bookworm .deb, got:\n%s", joined)
	}
}

func TestRender_BrowserCloakbrowser(t *testing.T) {
	cfg := BaseConfig{
		Core:         false,
		Architecture: "amd64",
		Browser: BrowserConfig{
			Engine:              "cloakbrowser",
			FingerprintPlatform: "linux",
			FingerprintSeed:     "auto",
		},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "cloakbrowser") {
		t.Errorf("expected cloakbrowser install, got:\n%s", joined)
	}
	if !strings.Contains(joined, "kasmvnc") && !strings.Contains(joined, "KasmVNC") {
		// KasmVNC deb download URL contains "kasmvnc"
		if !strings.Contains(joined, "kasmvncserver") {
			t.Errorf("expected kasmvnc install for container-compatible engine, got:\n%s", joined)
		}
	}
}

func TestRender_BrowserNone(t *testing.T) {
	cfg := BaseConfig{
		Core:         true,
		Architecture: "amd64",
		Browser:      BrowserConfig{Engine: "none"},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	joined := strings.Join(steps, "\n")
	if strings.Contains(joined, "chromium") || strings.Contains(joined, "cloakbrowser") {
		t.Errorf("engine=none should not install browser packages, got:\n%s", joined)
	}
}

func TestRender_ExtraSteps(t *testing.T) {
	cfg := BaseConfig{
		Core:         true,
		Architecture: "amd64",
		Browser:      BrowserConfig{Engine: "none"},
		ExtraSteps:   []string{"echo hello", "  ", "pip install numpy"},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Blank lines should be skipped
	for _, s := range steps {
		if strings.TrimSpace(s) == "" {
			t.Error("expected blank extra steps to be filtered out")
		}
	}

	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "echo hello") {
		t.Errorf("expected 'echo hello' in steps")
	}
	if !strings.Contains(joined, "pip install numpy") {
		t.Errorf("expected 'pip install numpy' in steps")
	}
}

func TestRender_EmptyConfig(t *testing.T) {
	cfg := BaseConfig{
		Core:         false,
		Architecture: "amd64",
		Browser:      BrowserConfig{Engine: "none"},
	}
	_, err := cfg.Render()
	if err == nil {
		t.Error("expected error for empty config (zero steps)")
	}
}

func TestRender_Arm64Architecture(t *testing.T) {
	cfg := BaseConfig{
		Core:         false,
		Architecture: "arm64",
		Browser:      BrowserConfig{Engine: "default"},
	}
	steps, err := cfg.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	joined := strings.Join(steps, "\n")
	// KasmVNC downloads an arch-specific deb; the filename uses "arm64"
	// and the HWCAP shim for aarch64 should be compiled.
	if !strings.Contains(joined, "arm64") && !strings.Contains(joined, "aarch64") {
		t.Errorf("expected arm64 or aarch64 reference for arm64 arch, got:\n%s", joined)
	}
	// The HWCAP mask shim is compiled on aarch64 — verify it's present
	if !strings.Contains(joined, "hwcap_mask") {
		t.Errorf("expected hwcap_mask shim compilation for arm64, got:\n%s", joined)
	}
	if !strings.Contains(joined, "/etc/ld.so.preload") {
		t.Errorf("expected hwcap_mask to be installed in ld.so.preload for arm64, got:\n%s", joined)
	}
}

func TestShellJoin_ShCommand(t *testing.T) {
	argv := []string{"sh", "-c", "curl -fsSL https://example.com | bash"}
	got := shellJoin(argv)
	want := "curl -fsSL https://example.com | bash"
	if got != want {
		t.Errorf("shellJoin(%v) = %q, want %q", argv, got, want)
	}
}

func TestShellJoin_SimpleCommand(t *testing.T) {
	argv := []string{"apt-get", "install", "-y", "git", "curl"}
	got := shellJoin(argv)
	want := "apt-get install -y git curl"
	if got != want {
		t.Errorf("shellJoin(%v) = %q, want %q", argv, got, want)
	}
}

func TestShellJoin_QuotingNeeded(t *testing.T) {
	argv := []string{"rm", "-rf", "/var/lib/apt/lists/*"}
	got := shellJoin(argv)
	if !strings.Contains(got, "'/var/lib/apt/lists/*'") {
		t.Errorf("expected quoted path with *, got %q", got)
	}
}
