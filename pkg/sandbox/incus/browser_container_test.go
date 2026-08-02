package incus

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestKasmvncConfigYAML_LocksViewportResolution(t *testing.T) {
	yaml := kasmvncConfigYAML(1920, 1080)
	for _, want := range []string{
		"allow_resize: false",
		"width: 1920",
		"height: 1080",
		"require_ssl: false",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("kasmvncConfigYAML missing %q", want)
		}
	}
	// Zero dims must not fall through to KasmVNC package defaults (1024×768).
	def := kasmvncConfigYAML(0, 0)
	if !strings.Contains(def, "width: 1920") || !strings.Contains(def, "height: 1080") {
		t.Fatalf("zero viewport should default to 1920x1080, got:\n%s", def)
	}
	if strings.Contains(def, "width: 1024") || strings.Contains(def, "height: 768") {
		t.Fatal("must not use KasmVNC package default 1024x768")
	}
}

func TestDetectBrowserEngine(t *testing.T) {
	tests := []struct {
		name     string
		cfg      BrowserContainerConfig
		expected string
	}{
		{
			name:     "empty path returns default",
			cfg:      BrowserContainerConfig{},
			expected: "default",
		},
		{
			name:     "explicit empty string returns default",
			cfg:      BrowserContainerConfig{ChromePath: ""},
			expected: "default",
		},
		{
			name:     "cloakbrowser in path",
			cfg:      BrowserContainerConfig{ChromePath: "/usr/bin/cloakbrowser"},
			expected: "cloakbrowser",
		},
		{
			name:     "cloakbrowser in subdirectory",
			cfg:      BrowserContainerConfig{ChromePath: "/home/user/.cloakbrowser/chrome"},
			expected: "cloakbrowser",
		},
		{
			name:     "custom chrome path",
			cfg:      BrowserContainerConfig{ChromePath: "/opt/google/chrome/chrome"},
			expected: "custom",
		},
		{
			name:     "custom chromium path",
			cfg:      BrowserContainerConfig{ChromePath: "/snap/bin/chromium"},
			expected: "custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectBrowserEngine(tt.cfg)
			if got != tt.expected {
				t.Errorf("DetectBrowserEngine(%+v) = %q, want %q", tt.cfg, got, tt.expected)
			}
		})
	}
}

func TestIsContainerCompatibleEngine(t *testing.T) {
	tests := []struct {
		engine   string
		expected bool
	}{
		{"default", true},
		{"cloakbrowser", true},
		{"custom", false},
		{"remote", false},
		{"", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.engine, func(t *testing.T) {
			got := IsContainerCompatibleEngine(tt.engine)
			if got != tt.expected {
				t.Errorf("IsContainerCompatibleEngine(%q) = %v, want %v", tt.engine, got, tt.expected)
			}
		})
	}
}

// TestBrowserContainerInstallCommands_SharedCommands verifies that commands
// shared across all engines are present: browser user creation, KasmVNC
// config (with explicit user-owned cert paths), openssl cert generation
// into /home/browser/.vnc/, socat, and runuser usage.
func TestBrowserContainerInstallCommands_SharedCommands(t *testing.T) {
	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cmds := BrowserContainerInstallCommands(engine, "x86_64", DistroUbuntuNoble)
			flat := flattenCommands(cmds)

			// Browser user creation (useradd, NOT adduser)
			assertContainsCmd(t, cmds, "useradd", "browser user creation")

			// KasmVNC config file
			assertContainsStr(t, flat, "kasmvnc.yaml", "KasmVNC config file creation")
			assertContainsStr(t, flat, "require_ssl: false", "SSL disabled in KasmVNC config")
			assertContainsStr(t, flat, "use_ipv6: false", "IPv6 disabled in KasmVNC config")
			assertContainsStr(t, flat, "allow_resize: false", "client desktop resize disabled")
			assertContainsStr(t, flat, "width: 1920", "locked resolution width matches viewport default")
			assertContainsStr(t, flat, "height: 1080", "locked resolution height matches viewport default")
			assertContainsStr(t, flat, "x11-utils", "x11-utils for xdpyinfo recording probe")

			// SSL cert is now generated with openssl directly into the browser
			// user's .vnc directory (avoids unprivileged container restrictions
			// on /etc/ssl/private/). Key is 600, owned by browser:browser.
			assertContainsStr(t, flat, "snakeoil.key", "snakeoil private key written to user .vnc dir")
			assertContainsStr(t, flat, "snakeoil.pem", "snakeoil cert written to user .vnc dir")
			assertContainsStr(t, flat, "openssl req -x509", "cert generated with raw openssl (no make-ssl-cert)")
			assertCmdSequence(t, cmds, []string{"chmod", "600", "snakeoil.key"}, "snakeoil key must be 600")

			// socat installed (for CDP port forwarding)
			assertContainsStr(t, flat, "socat", "socat package for CDP bridge")

			// ffmpeg for browser session recording (x11grab of KasmVNC display)
			assertContainsStr(t, flat, "ffmpeg", "ffmpeg package for session recording")

			// Uses runuser (NOT su) for unprivileged container compatibility
			assertContainsStr(t, flat, "runuser", "must use runuser, not su")

			// KasmVNC password setup
			assertContainsStr(t, flat, "kasmvncpasswd", "KasmVNC password pre-creation")

			// No ssl-cert group at all anymore — the cert lives in the browser
			// user's own directory with direct ownership, no group gymnastics.
			assertNotContainsStr(t, flat, "ssl-cert", "no references to ssl-cert group or package")

			// apt-get clean at the end of the browser install sequence
			assertContainsCmd(t, cmds, "apt-get", "apt-get clean")
		})
	}
}

// TestBrowserContainerInstallCommands_ArchAwareKasmVNC verifies that the
// KasmVNC .deb URL uses the correct architecture suffix.
func TestBrowserContainerInstallCommands_ArchAwareKasmVNC(t *testing.T) {
	tests := []struct {
		arch    string
		debArch string
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
	}

	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			cmds := BrowserContainerInstallCommands("default", tt.arch, DistroUbuntuNoble)
			flat := flattenCommands(cmds)

			expectedURL := fmt.Sprintf("kasmvncserver_noble_%s_%s.deb", kasmVNCVersion, tt.debArch)
			assertContainsStr(t, flat, expectedURL, "KasmVNC .deb URL for "+tt.arch)
		})
	}
}

// TestBrowserContainerInstallCommands_DefaultEngine verifies engine-specific
// commands for the default (Chromium from xtradeb PPA) engine on Ubuntu noble.
func TestBrowserContainerInstallCommands_DefaultEngine(t *testing.T) {
	cmds := BrowserContainerInstallCommands("default", "x86_64", DistroUbuntuNoble)
	flat := flattenCommands(cmds)

	// Must add xtradeb PPA
	assertContainsStr(t, flat, "ppa:xtradeb/apps", "xtradeb PPA for native Chromium .deb")

	// Must install chromium package
	assertCmdSequence(t, cmds, []string{"apt-get", "install", "-y", "chromium"}, "chromium package install")

	// Must tolerate apt-get update failures (|| true) for AppStream metadata errors
	assertContainsStr(t, flat, "apt-get update || true", "tolerant apt-get update after PPA")
}

// TestBrowserContainerInstallCommands_CloakBrowserEngine verifies engine-specific
// commands for the CloakBrowser engine.
func TestBrowserContainerInstallCommands_CloakBrowserEngine(t *testing.T) {
	cmds := BrowserContainerInstallCommands("cloakbrowser", "x86_64", DistroUbuntuNoble)
	flat := flattenCommands(cmds)

	// Must install python3 and pip3
	assertContainsStr(t, flat, "python3", "python3 for CloakBrowser")
	assertContainsStr(t, flat, "python3-pip", "pip3 for CloakBrowser")

	// Must install cloakbrowser pip package
	assertCmdSequence(t, cmds, []string{"pip3", "install"}, "pip install cloakbrowser")
	assertContainsStr(t, flat, "cloakbrowser", "cloakbrowser pip package")

	// Must NOT add xtradeb PPA (that's for the default engine)
	if strings.Contains(flat, "ppa:xtradeb") {
		t.Error("CloakBrowser engine should not add xtradeb PPA")
	}
}

// TestCloakBrowserInstallCommands_RequiredSharedLibs verifies that the
// CloakBrowser install command list includes every shared library that the
// standalone Chromium binary needs. These cannot be auto-resolved by apt
// because CloakBrowser is installed via pip, not as a .deb package.
func TestCloakBrowserInstallCommands_RequiredSharedLibs(t *testing.T) {
	cmds := BrowserContainerInstallCommands("cloakbrowser", "x86_64", DistroUbuntuNoble)
	flat := flattenCommands(cmds)

	// Each of these packages was confirmed required during live testing.
	// Missing any one causes the CloakBrowser binary to fail with a
	// "cannot open shared object file" error on startup.
	requiredLibs := []struct {
		pkg  string
		desc string
	}{
		{"libcups2", "CUPS printing (Chromium print subsystem)"},
		{"libpango-1.0-0", "text layout and rendering"},
		{"libcairo2", "2D graphics"},
		{"libdbus-1-3", "D-Bus IPC"},
		{"libdrm2", "Direct Rendering Manager"},
		{"libexpat1", "XML parsing"},
		{"libxdamage1", "X11 damage extension"},
		{"libxext6", "X11 extensions"},
		{"libxfixes3", "X11 fixes extension"},
		{"libxkbcommon0", "keyboard handling"},
		{"libatspi2.0-0", "accessibility"},
		{"libvulkan1", "Vulkan graphics"},
		{"libxcb-dri3-0", "XCB DRI3 extension (GPU buffer sharing)"},
		{"libatk1.0-0", "ATK accessibility toolkit"},
		{"libgtk-3-0", "GTK3 (Chromium UI toolkit)"},
		{"libgdk-pixbuf-2.0-0", "GDK-Pixbuf image loading"},
		{"libnspr4", "Netscape Portable Runtime (NSS dependency)"},
		{"libglib2.0-0t64", "GLib/GObject (libgobject-2.0.so.0)"},
		{"libnss3", "Network Security Services"},
		{"libatk-bridge2.0-0", "ATK-Bridge accessibility"},
		{"libx11-xcb1", "X11-XCB bridge"},
		{"libxcomposite1", "X11 composite extension"},
		{"libxrandr2", "X11 RandR extension"},
		{"libgbm1", "Mesa GBM (buffer management)"},
		{"libasound2t64", "ALSA sound"},
	}

	for _, lib := range requiredLibs {
		if !strings.Contains(flat, lib.pkg) {
			t.Errorf("missing required shared library package %q (%s)", lib.pkg, lib.desc)
		}
	}
}

// TestLaunchScript_NoExtraTabFlags verifies that both the default and
// CloakBrowser launch scripts include Chromium flags that prevent extra
// tabs from opening on startup (first-run page, default browser check, etc.).
func TestLaunchScript_NoExtraTabFlags(t *testing.T) {
	requiredFlags := []string{
		"--no-first-run",
		"--no-default-browser-check",
		"--noerrdialogs",
		"--disable-features=TranslateUI",
	}

	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1280, 720)

			for _, flag := range requiredFlags {
				if !strings.Contains(script, flag) {
					t.Errorf("launch script missing flag %q", flag)
				}
			}
		})
	}
}

// TestLaunchScript_CloakBrowser_HasDiagnostics verifies that the CloakBrowser
// launch script includes startup diagnostics: stderr log capture, process
// liveness check via pgrep, and CDP port binding verification via ss.
// These diagnostics surface the real error when CloakBrowser crashes on
// startup instead of a generic "CDP timeout after 15s" message.
func TestLaunchScript_CloakBrowser_HasDiagnostics(t *testing.T) {
	script := buildLaunchScript("cloakbrowser", BrowserContainerConfig{
		ChromePath: "cloakbrowser",
	}, 1280, 720)

	checks := []struct {
		substr string
		desc   string
	}{
		{"BROWSER_LOG=/tmp/cloakbrowser.log", "stderr log file path"},
		{">$BROWSER_LOG 2>&1", "stdout+stderr redirect to log file"},
		{"pgrep -u browser", "process liveness check via pgrep"},
		{"CloakBrowser process died on startup", "crash error message"},
		{"cat $BROWSER_LOG", "log dump on failure"},
		{"ss -tln", "CDP port binding check via ss"},
		{"CDP_READY=", "CDP readiness loop variable"},
		{"CloakBrowser started but DevTools port", "CDP port failure message"},
	}

	for _, c := range checks {
		if !strings.Contains(script, c.substr) {
			t.Errorf("CloakBrowser launch script missing diagnostic: %s (expected %q)", c.desc, c.substr)
		}
	}
}

// TestLaunchScript_Default_HasDiagnostics verifies the default Chromium launch
// script checks process liveness and CDP binding (same resilience as CloakBrowser)
// and is idempotent for reconnect.
func TestLaunchScript_Default_HasDiagnostics(t *testing.T) {
	script := buildLaunchScript("default", BrowserContainerConfig{}, 1280, 720)

	checks := []struct {
		substr string
		desc   string
	}{
		{"BROWSER_LOG=/tmp/chromium.log", "stderr log file path"},
		{"pgrep -u browser", "process liveness check via pgrep"},
		{"Chromium process died on startup", "crash error message"},
		{"CDP_READY=", "CDP readiness loop variable"},
		{"Chromium started but DevTools port", "CDP port failure message"},
		{"Idempotent:", "skip-if-running preamble"},
		{"pkill -u browser", "stale process cleanup before relaunch"},
	}

	for _, c := range checks {
		if !strings.Contains(script, c.substr) {
			t.Errorf("default launch script missing %s (expected %q)", c.desc, c.substr)
		}
	}
	if strings.Contains(script, "CloakBrowser") {
		t.Error("default launch script should not reference CloakBrowser")
	}
}

func TestLaunchScript_IdempotentPreamble(t *testing.T) {
	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1280, 720)
			if !strings.Contains(script, "Idempotent:") {
				t.Error("missing idempotent preamble")
			}
			if !strings.Contains(script, "pkill -u browser") {
				t.Error("missing stale browser cleanup")
			}
			if !strings.Contains(script, "--remote-debugging-port=") {
				t.Error("stale cleanup should match --remote-debugging-port=")
			}
			if !strings.Contains(script, fmt.Sprintf("socat.*TCP-LISTEN:%d", DefaultCDPPort)) &&
				!strings.Contains(script, fmt.Sprintf("TCP-LISTEN:%d", DefaultCDPPort)) {
				t.Error("missing socat bridge")
			}
		})
	}
}

func TestBrowserStackHealthyScript_MentionsPorts(t *testing.T) {
	s := browserStackHealthyScript()
	if !strings.Contains(s, fmt.Sprintf(":%d ", internalCDPPort)) {
		t.Errorf("healthy script missing internal CDP port %d", internalCDPPort)
	}
	if !strings.Contains(s, fmt.Sprintf(":%d ", DefaultCDPPort)) {
		t.Errorf("healthy script missing bridge CDP port %d", DefaultCDPPort)
	}
	if !strings.Contains(s, "--remote-debugging-port=") {
		t.Error("healthy script should match CDP flag, not bare chrome|chromium")
	}
}

func TestKillStaleBrowserStackScript_MatchesCDPFlag(t *testing.T) {
	s := killStaleBrowserStackScript()
	if !strings.Contains(s, "--remote-debugging-port=") {
		t.Error("killStale should match --remote-debugging-port=")
	}
	if strings.Contains(s, "chrome|chromium") {
		t.Error("killStale must not use bare chrome|chromium pattern")
	}
	if !strings.Contains(s, "/home/browser/.cloakbrowser/") {
		t.Error("killStale should also match CloakBrowser install path")
	}
	if !strings.Contains(s, socatBridgeKillPattern()) {
		t.Errorf("killStale should use anchored socat pattern %q", socatBridgeKillPattern())
	}
	if strings.Contains(s, "socat.*TCP-LISTEN") {
		t.Error("killStale must not use unscoped socat.*TCP-LISTEN pattern (matches sh -c launcher argv)")
	}
}

// TestSocatBridgeKillPattern_DoesNotMatchLauncherArgv documents the invariant
// that pkill -f on the socat pattern must not SIGTERM the launcher shell whose
// argv embeds the full launch script (including killStale / socat bridge text).
func TestSocatBridgeKillPattern_DoesNotMatchLauncherArgv(t *testing.T) {
	pat := socatBridgeKillPattern()
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("compile %q: %v", pat, err)
	}
	if !strings.HasPrefix(pat, "^socat") {
		t.Fatalf("pattern %q must start with ^socat", pat)
	}

	realSocat := fmt.Sprintf(
		"socat TCP-LISTEN:%d,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:%d",
		DefaultCDPPort, internalCDPPort,
	)
	if !re.MatchString(realSocat) {
		t.Fatalf("pattern %q must match real socat cmdline %q", pat, realSocat)
	}

	script := buildLaunchScript("cloakbrowser", BrowserContainerConfig{ChromePath: "cloakbrowser"}, 1280, 720)
	launcherArgv := "sh -c " + script
	if re.MatchString(launcherArgv) {
		t.Fatalf("pattern %q must not match launcher argv that embeds the launch script", pat)
	}
}

func TestFormatBrowserLaunchExitError_SIGTERM(t *testing.T) {
	err := formatBrowserLaunchExitError(nil, "astn-sess-x", 143, "")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "code 143") || !strings.Contains(msg, "SIGTERM") {
		t.Fatalf("got %q, want code 143 + SIGTERM", msg)
	}
	if !isLaunchExit143(err) {
		t.Fatal("isLaunchExit143 should be true")
	}
}

func TestFormatBrowserLaunchExitError_NonZeroWithOutput(t *testing.T) {
	err := formatBrowserLaunchExitError(nil, "astn-sess-x", 1, "CloakBrowser process died on startup")
	if err == nil || !strings.Contains(err.Error(), "CloakBrowser process died") {
		t.Fatalf("got %v", err)
	}
	if isLaunchExit143(err) {
		t.Fatal("exit 1 must not be classified as 143")
	}
}

// TestLaunchScript_SharedFlags verifies that both engines include core
// Chromium flags required for container operation (sandbox disabled, CDP port,
// user data dir, anti-detection, etc.).
func TestLaunchScript_SharedFlags(t *testing.T) {
	sharedFlags := []string{
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		fmt.Sprintf("--remote-debugging-port=%d", internalCDPPort),
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-blink-features=AutomationControlled",
		BrowserProfileMountPath,
		"about:blank",
	}

	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1280, 720)

			for _, flag := range sharedFlags {
				if !strings.Contains(script, flag) {
					t.Errorf("launch script missing shared flag/value %q", flag)
				}
			}
		})
	}
}

// TestLaunchScript_SocatBridge verifies that both engines include the socat
// bridge command that forwards CDP from 0.0.0.0:DefaultCDPPort to the
// internal loopback port.
func TestLaunchScript_SocatBridge(t *testing.T) {
	expected := fmt.Sprintf("socat TCP-LISTEN:%d,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:%d",
		DefaultCDPPort, internalCDPPort)

	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1280, 720)

			if !strings.Contains(script, expected) {
				t.Errorf("launch script missing socat bridge command")
			}
		})
	}
}

// TestLaunchScript_CloakBrowser_FingerprintFlags verifies that CloakBrowser
// fingerprint flags are included when configured.
func TestLaunchScript_CloakBrowser_FingerprintFlags(t *testing.T) {
	script := buildLaunchScript("cloakbrowser", BrowserContainerConfig{
		ChromePath:          "cloakbrowser",
		FingerprintSeed:     "42",
		FingerprintPlatform: "windows",
	}, 1280, 720)

	if !strings.Contains(script, "--fingerprint 42") {
		t.Error("launch script missing --fingerprint seed")
	}
	if !strings.Contains(script, "--fingerprint-platform windows") {
		t.Error("launch script missing --fingerprint-platform")
	}
}

// TestLaunchScript_ProxyFlag verifies that the proxy flag is included when
// configured, for both engines.
func TestLaunchScript_ProxyFlag(t *testing.T) {
	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{Proxy: "socks5://127.0.0.1:1080"}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1280, 720)

			if !strings.Contains(script, "--proxy-server=socks5://127.0.0.1:1080") {
				t.Error("launch script missing proxy flag")
			}
		})
	}
}

// TestLaunchScript_ViewportSize verifies that the viewport dimensions are
// passed to the --window-size flag.
func TestLaunchScript_ViewportSize(t *testing.T) {
	for _, engine := range []string{"default", "cloakbrowser"} {
		t.Run(engine, func(t *testing.T) {
			cfg := BrowserContainerConfig{}
			if engine == "cloakbrowser" {
				cfg.ChromePath = "cloakbrowser"
			}
			script := buildLaunchScript(engine, cfg, 1920, 1080)

			if !strings.Contains(script, "--window-size=1920,1080") {
				t.Error("launch script missing correct --window-size")
			}
		})
	}
}

// TestBrowserContainerInstallCommands_DebianBookworm_DefaultEngine verifies
// that the default engine on Debian bookworm uses native chromium packages
// without the xtradeb PPA, and uses the correct Debian-specific package names.
func TestBrowserContainerInstallCommands_DebianBookworm_DefaultEngine(t *testing.T) {
	cmds := BrowserContainerInstallCommands("default", "x86_64", DistroDebianBookworm)
	flat := flattenCommands(cmds)

	// Must install chromium from native repos
	assertCmdSequence(t, cmds, []string{"apt-get", "install", "-y", "chromium"}, "chromium from native repos")

	// Must NOT add xtradeb PPA (only for Ubuntu)
	if strings.Contains(flat, "ppa:xtradeb") {
		t.Error("Debian bookworm should not use xtradeb PPA")
	}
	if strings.Contains(flat, "add-apt-repository") {
		t.Error("Debian bookworm should not call add-apt-repository")
	}
	if strings.Contains(flat, "software-properties-common") {
		t.Error("Debian bookworm should not install software-properties-common")
	}

	// Must use Debian-specific package names
	assertContainsStr(t, flat, "libasound2", "Debian libasound2 (not libasound2t64)")
	if strings.Contains(flat, "libasound2t64") {
		t.Error("Debian bookworm should use libasound2, not libasound2t64")
	}
	assertContainsStr(t, flat, "libglib2.0-0", "Debian GLib/GObject package for libgobject-2.0.so.0")
	if strings.Contains(flat, "libglib2.0-0t64") {
		t.Error("Debian bookworm should use libglib2.0-0, not libglib2.0-0t64")
	}
	assertContainsStr(t, flat, "libjpeg62-turbo", "Debian libjpeg62-turbo (not libjpeg-turbo8)")
	if strings.Contains(flat, "libjpeg-turbo8") {
		t.Error("Debian bookworm should use libjpeg62-turbo, not libjpeg-turbo8")
	}

	// KasmVNC URL must use bookworm suffix
	expectedURL := fmt.Sprintf("kasmvncserver_bookworm_%s_amd64.deb", kasmVNCVersion)
	assertContainsStr(t, flat, expectedURL, "KasmVNC bookworm .deb URL")
	if strings.Contains(flat, "kasmvncserver_noble_") {
		t.Error("Debian bookworm should use kasmvncserver_bookworm_*, not noble")
	}
}

// TestBrowserContainerInstallCommands_DebianBookworm_CloakBrowserEngine verifies
// that CloakBrowser on Debian bookworm uses the correct Debian-specific packages.
func TestBrowserContainerInstallCommands_DebianBookworm_CloakBrowserEngine(t *testing.T) {
	cmds := BrowserContainerInstallCommands("cloakbrowser", "x86_64", DistroDebianBookworm)
	flat := flattenCommands(cmds)

	// Must use Debian-specific package names
	assertContainsStr(t, flat, "libasound2", "Debian libasound2")
	if strings.Contains(flat, "libasound2t64") {
		t.Error("Debian bookworm CloakBrowser should use libasound2, not libasound2t64")
	}
	assertContainsStr(t, flat, "libglib2.0-0", "Debian GLib/GObject package for libgobject-2.0.so.0")
	if strings.Contains(flat, "libglib2.0-0t64") {
		t.Error("Debian bookworm CloakBrowser should use libglib2.0-0, not libglib2.0-0t64")
	}
	assertContainsStr(t, flat, "libjpeg62-turbo", "Debian libjpeg62-turbo")
	if strings.Contains(flat, "libjpeg-turbo8") {
		t.Error("Debian bookworm CloakBrowser should use libjpeg62-turbo, not libjpeg-turbo8")
	}

	// KasmVNC URL must use bookworm suffix
	expectedURL := fmt.Sprintf("kasmvncserver_bookworm_%s_amd64.deb", kasmVNCVersion)
	assertContainsStr(t, flat, expectedURL, "KasmVNC bookworm .deb URL")

	// CloakBrowser-specific commands still present
	assertContainsStr(t, flat, "python3", "python3 for CloakBrowser")
	assertContainsStr(t, flat, "python3-pip", "pip3 for CloakBrowser")
	assertCmdSequence(t, cmds, []string{"pip3", "install"}, "pip install cloakbrowser")
}

// TestBrowserContainerInstallCommands_DebianBookworm_ArchAwareKasmVNC verifies
// that the KasmVNC .deb URL on Debian bookworm uses the correct architecture.
func TestBrowserContainerInstallCommands_DebianBookworm_ArchAwareKasmVNC(t *testing.T) {
	tests := []struct {
		arch    string
		debArch string
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
	}

	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			cmds := BrowserContainerInstallCommands("default", tt.arch, DistroDebianBookworm)
			flat := flattenCommands(cmds)

			expectedURL := fmt.Sprintf("kasmvncserver_bookworm_%s_%s.deb", kasmVNCVersion, tt.debArch)
			assertContainsStr(t, flat, expectedURL, "KasmVNC bookworm .deb URL for "+tt.arch)
		})
	}
}

// flattenCommands joins all command slices into a single string for substring matching.
func flattenCommands(cmds [][]string) string {
	var parts []string
	for _, cmd := range cmds {
		parts = append(parts, strings.Join(cmd, " "))
	}
	return strings.Join(parts, "\n")
}

// assertContainsStr fails if s does not contain substr.
func assertContainsStr(t *testing.T, s, substr, desc string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s: expected to find %q in commands", desc, substr)
	}
}

// assertContainsCmd fails if no command starts with the given binary.
func assertContainsCmd(t *testing.T, cmds [][]string, binary, desc string) {
	t.Helper()
	for _, cmd := range cmds {
		if len(cmd) > 0 && cmd[0] == binary {
			return
		}
		// Also check inside sh -c commands
		if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" && strings.Contains(cmd[2], binary) {
			return
		}
	}
	t.Errorf("%s: expected a command starting with %q", desc, binary)
}

// assertCmdSequence fails if no command contains all the given tokens in order.
func assertCmdSequence(t *testing.T, cmds [][]string, tokens []string, desc string) {
	t.Helper()
	for _, cmd := range cmds {
		joined := strings.Join(cmd, " ")
		allFound := true
		pos := 0
		for _, tok := range tokens {
			idx := strings.Index(joined[pos:], tok)
			if idx < 0 {
				allFound = false
				break
			}
			pos += idx + len(tok)
		}
		if allFound {
			return
		}
	}
	t.Errorf("%s: expected command containing %v in sequence", desc, tokens)
}

// assertNotContainsStr fails if s contains substr.
func assertNotContainsStr(t *testing.T, s, substr, desc string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("%s: did not expect to find %q in commands", desc, substr)
	}
}
