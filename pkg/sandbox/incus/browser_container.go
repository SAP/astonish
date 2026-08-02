package incus

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// kasmvncConfigYAML returns the KasmVNC config written into browsersandboxes.
//
// When desktop.allow_resize is false (and -AcceptSetDesktopSize 0), KasmVNC
// locks to desktop.resolution — package defaults are 1024×768. Width/height
// must match the intended viewport (and -geometry), not those defaults.
func kasmvncConfigYAML(width, height int) string {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	return fmt.Sprintf(`network:
  protocol: http
  interface: 0.0.0.0
  use_ipv4: true
  use_ipv6: false
  ssl:
    require_ssl: false
    pem_certificate: /home/browser/.vnc/snakeoil.pem
    pem_key: /home/browser/.vnc/snakeoil.key
desktop:
  allow_resize: false
  resolution:
    width: %d
    height: %d
user_session:
  concurrent_connections_prompt: false
logging:
  log_writer_name: all
  log_dest: logfile
  level: 30
command_line:
  prompt: false
`, width, height)
}

// Browser container constants.
const (
	// DefaultKasmVNCPort is the default port KasmVNC listens on inside the container.
	DefaultKasmVNCPort = 6901
	// kasmVNCDisplay is the X11 display number used by KasmVNC. The websocket
	// port is kasmVNCPort + (display * 100) = 6901 for display :0.
	kasmVNCDisplay = "0"
	// DefaultCDPPort is the port exposed by the socat bridge inside the container.
	// Chromium's --remote-debugging-port binds to 127.0.0.1 only (ignores
	// --remote-debugging-address=0.0.0.0 since M91+). We use socat to forward
	// from 0.0.0.0:DefaultCDPPort to 127.0.0.1:internalCDPPort, which makes
	// the DevTools endpoint accessible from outside the container.
	DefaultCDPPort = 9222
	// internalCDPPort is the Chromium DevTools port inside the container. Set to
	// 9223 to avoid clashing with the socat bridge on 9222. Chromium listens on
	// this port (localhost only — see DefaultCDPPort comment above for why that's
	// not the full browser) and always binds DevTools to 127.0.0.1. We use socat
	// to forward from 0.0.0.0:DefaultCDPPort to 127.0.0.1:internalCDPPort.
	internalCDPPort = 9223
	// BrowserProfileMountPath is the Chromium profile dir inside containers.
	BrowserProfileMountPath = "/home/browser/.config/chromium"
	// browserLaunchScriptPath is where StartChromiumInContainer writes the
	// launch script before exec. Running by path keeps kill needles out of
	// the launcher process argv (defense in depth against pkill -f self-match).
	browserLaunchScriptPath = "/tmp/astonish-browser-launch.sh"

	// kasmVNCVersion is the KasmVNC release version to install.
	kasmVNCVersion = "1.4.0"
)

// LinuxDistro identifies the Linux distribution of the container's base image,
// which determines package names, repository setup, and KasmVNC .deb variant
// for the browser stack installation.
type LinuxDistro string

const (
	// DistroUbuntuNoble is Ubuntu 24.04 LTS (noble). Used by Incus containers
	// (DefaultBaseImage = "ubuntu/24.04" in template.go).
	DistroUbuntuNoble LinuxDistro = "ubuntu-noble"
	// DistroDebianBookworm is Debian 12 (bookworm). Used by the K8s sandbox-base
	// image (docker/sandbox-base/Dockerfile: FROM debian:bookworm-slim).
	DistroDebianBookworm LinuxDistro = "debian-bookworm"
)

// browserDeps holds distro-specific package names and configuration for the
// browser container install.
type browserDeps struct {
	libasound     string // ALSA library package name
	libglib       string // GLib/GObject package name
	libjpegTurbo  string // libjpeg-turbo package name (KasmVNC dep)
	kasmVNCSuffix string // distro suffix in KasmVNC .deb filename
	useXtradebPPA bool   // whether to use xtradeb PPA for Chromium
}

// depsFor returns the distro-specific package names and flags.
func depsFor(distro LinuxDistro) browserDeps {
	switch distro {
	case DistroDebianBookworm:
		return browserDeps{
			libasound:     "libasound2",
			libglib:       "libglib2.0-0",
			libjpegTurbo:  "libjpeg62-turbo",
			kasmVNCSuffix: "bookworm",
			useXtradebPPA: false, // Debian ships native chromium .debs
		}
	default: // DistroUbuntuNoble
		return browserDeps{
			libasound:     "libasound2t64",
			libglib:       "libglib2.0-0t64",
			libjpegTurbo:  "libjpeg-turbo8",
			kasmVNCSuffix: "noble",
			useXtradebPPA: true, // Ubuntu 24.04 chromium is snap-only
		}
	}
}

// BrowserContainerConfig controls browser runtime configuration inside a container.
type BrowserContainerConfig struct {
	// ViewportWidth is the browser viewport width in pixels. Default: 1920.
	ViewportWidth int
	// ViewportHeight is the browser viewport height in pixels. Default: 1080.
	ViewportHeight int
	// KasmVNCPort is the port KasmVNC listens on. Default: 6901.
	KasmVNCPort int
	// KasmVNCPassword is the VNC password. Empty = auto-generated.
	KasmVNCPassword string
	// Proxy is the HTTP/SOCKS proxy for browser traffic. Empty = direct.
	Proxy string

	// ChromePath is the host-side path to the browser binary. Used to detect
	// the browser engine. Empty = default (apt Chromium).
	ChromePath string
	// FingerprintSeed is a deterministic seed for CloakBrowser fingerprint
	// generation (only effective with CloakBrowser binary).
	FingerprintSeed string
	// FingerprintPlatform overrides the OS platform reported by CloakBrowser.
	// Valid values: "windows", "macos", "linux".
	FingerprintPlatform string
}

// DetectBrowserEngine determines which browser engine is configured based on
// the BrowserContainerConfig fields. Returns "default", "cloakbrowser",
// "custom", or "remote". Only "default" and "cloakbrowser" are supported in
// containers; "custom" and "remote" must fall back to host mode.
func DetectBrowserEngine(cfg BrowserContainerConfig) string {
	if cfg.ChromePath == "" {
		return "default"
	}
	if strings.Contains(cfg.ChromePath, "cloakbrowser") {
		return "cloakbrowser"
	}
	return "custom"
}

// IsContainerCompatibleEngine returns true if the detected engine can run
// inside a container. Custom and remote engines are not supported because
// the host binary may be macOS/Windows and cannot run in a Linux container.
func IsContainerCompatibleEngine(engine string) bool {
	return engine == "default" || engine == "cloakbrowser"
}

// BrowserContainerInstallCommands returns the commands to install the browser
// engine and KasmVNC inside a container template. The commands are engine-aware:
// "default" installs Chromium (from xtradeb PPA on Ubuntu noble, or from
// native repos on Debian bookworm), "cloakbrowser" installs python3 + pip3 +
// xvfb + the CloakBrowser package.
//
// The arch parameter is the container architecture ("x86_64" or "aarch64")
// and is used to select the correct KasmVNC .deb for the platform.
//
// The distro parameter selects distro-specific package names and repository
// configuration. Incus containers use DistroUbuntuNoble; K8s sandbox-base
// uses DistroDebianBookworm. Key differences:
//   - ALSA library: libasound2t64 (noble) vs libasound2 (bookworm)
//   - GLib/GObject: libglib2.0-0t64 (noble) vs libglib2.0-0 (bookworm)
//   - libjpeg-turbo: libjpeg-turbo8 (noble) vs libjpeg62-turbo (bookworm)
//   - KasmVNC .deb: kasmvncserver_noble_*.deb vs kasmvncserver_bookworm_*.deb
//   - Chromium source: xtradeb PPA (noble) vs native apt (bookworm)
//
// Common packages (X11 deps, fonts, KasmVNC, browser user) are shared across
// all engines.
func BrowserContainerInstallCommands(engine, arch string, distro LinuxDistro) [][]string {
	deps := depsFor(distro)

	// Common: apt update
	cmds := [][]string{
		{"apt-get", "update"},
	}

	// Engine-specific packages
	switch engine {
	case "cloakbrowser":
		// CloakBrowser needs python3, pip3, xvfb (for headed stealth mode),
		// plus all the shared X11/font deps that Chromium requires.
		// Unlike the "default" engine where `apt install chromium` pulls in
		// transitive deps automatically, CloakBrowser is a standalone binary
		// download — we must install every shared library it links against.
		cmds = append(cmds, []string{"apt-get", "install", "-y",
			// CloakBrowser runtime deps
			"python3", "python3-pip", "xvfb",
			// Chromium shared deps (CloakBrowser is a patched Chromium).
			// These must be listed explicitly because the binary is not
			// installed via apt and has no automatic dependency resolution.
			"fonts-liberation", "fonts-noto-color-emoji",
			"xdg-utils", deps.libglib, "libnss3", "libatk-bridge2.0-0",
			"libx11-xcb1", "libxcomposite1", "libxrandr2",
			"libgbm1", deps.libasound,
			"libcups2",            // CUPS printing (required by Chromium's print subsystem)
			"libpango-1.0-0",      // text layout and rendering
			"libcairo2",           // 2D graphics
			"libdbus-1-3",         // D-Bus IPC
			"libdrm2",             // Direct Rendering Manager
			"libexpat1",           // XML parsing
			"libxdamage1",         // X11 damage extension
			"libxext6",            // X11 extensions
			"libxfixes3",          // X11 fixes extension
			"libxkbcommon0",       // keyboard handling
			"libatspi2.0-0",       // accessibility
			"libvulkan1",          // Vulkan graphics (optional but avoids warnings)
			"libxcb-dri3-0",       // XCB DRI3 extension (GPU buffer sharing)
			"libatk1.0-0",         // ATK accessibility toolkit (base, needed alongside bridge)
			"libgtk-3-0",          // GTK3 (Chromium UI toolkit dependency)
			"libgdk-pixbuf-2.0-0", // GDK-Pixbuf image loading
			"libnspr4",            // Netscape Portable Runtime (NSS dependency)
			// KasmVNC dependencies
			deps.libjpegTurbo, "libwebp-dev", "libssl3",
			"xfonts-base", "xfonts-75dpi", "xfonts-100dpi",
			"x11-xserver-utils",
			"x11-utils", // xdpyinfo for recording display probe
			// openssl for generating the KasmVNC snakeoil cert directly into
			// /home/browser/.vnc/ (avoids unprivileged container chmod restrictions
			// on /etc/ssl/private/).
			"openssl",
			// CDP port forwarding (Chromium binds DevTools to loopback only)
			"socat",
			// Session recording (ffmpeg x11grab of the KasmVNC display)
			"ffmpeg",
			// Utilities
			"wget", "ca-certificates",
		})
	default: // "default" — Chromium install (method depends on distro)
		if deps.useXtradebPPA {
			// Ubuntu 24.04's chromium-browser package is a snap transitional shim
			// that triggers `snap install chromium`. Snap does not work inside
			// unprivileged LXC containers (requires squashfs mounts and AppArmor
			// confinement), causing the install to hang indefinitely.
			//
			// The xtradeb/apps PPA provides native Chromium .deb packages for both
			// amd64 and arm64. We use the PPA so apt auto-selects the correct
			// architecture — critical for Apple Silicon Macs where Docker+Incus
			// runs arm64 containers.
			//
			// After adding the PPA, apt-get update may fail with exit code 100
			// due to AppStream metadata (dep11/Components) download errors during
			// Ubuntu mirror syncs. These errors are non-fatal for package
			// installation, so we tolerate them with "|| true".
			cmds = append(cmds,
				// Install add-apt-repository tool + shared deps
				[]string{"apt-get", "install", "-y",
					"software-properties-common",
					// Chromium shared deps
					"fonts-liberation", "fonts-noto-color-emoji",
					"xdg-utils", deps.libglib, "libnss3", "libatk-bridge2.0-0",
					"libx11-xcb1", "libxcomposite1", "libxrandr2",
					"libgbm1", deps.libasound,
					// KasmVNC dependencies
					deps.libjpegTurbo, "libwebp-dev", "libssl3",
					"xfonts-base", "xfonts-75dpi", "xfonts-100dpi",
					"x11-xserver-utils",
					"x11-utils", // xdpyinfo for recording display probe
					// openssl for generating the KasmVNC snakeoil cert directly into
					// /home/browser/.vnc/ (avoids unprivileged container chmod restrictions
					// on /etc/ssl/private/).
					"openssl",
					// CDP port forwarding (Chromium binds DevTools to loopback only)
					"socat",
					// Session recording (ffmpeg x11grab of the KasmVNC display)
					"ffmpeg",
					// Utilities
					"wget", "ca-certificates",
				},
				// Add PPA with native Chromium .deb packages (amd64 + arm64)
				[]string{"add-apt-repository", "-y", "ppa:xtradeb/apps"},
				// Refresh package lists. Tolerate AppStream metadata errors (dep11
				// Components download failures during mirror syncs) — these don't
				// affect package installation. The "|| true" prevents exit code 100
				// from aborting the template creation.
				[]string{"sh", "-c", "apt-get update || true"},
				// Install Chromium (apt auto-selects the correct architecture)
				[]string{"apt-get", "install", "-y", "chromium"},
			)
		} else {
			// Debian bookworm ships native Chromium .deb packages (no snap, no
			// PPA needed). Install shared deps + chromium in one step.
			cmds = append(cmds,
				[]string{"apt-get", "install", "-y",
					// Chromium (native .deb on Debian)
					"chromium",
					// Chromium shared deps (listed explicitly for consistency
					// with the Ubuntu path, even though apt would pull most in)
					"fonts-liberation", "fonts-noto-color-emoji",
					"xdg-utils", deps.libglib, "libnss3", "libatk-bridge2.0-0",
					"libx11-xcb1", "libxcomposite1", "libxrandr2",
					"libgbm1", deps.libasound,
					// KasmVNC dependencies
					deps.libjpegTurbo, "libwebp-dev", "libssl3",
					"xfonts-base", "xfonts-75dpi", "xfonts-100dpi",
					"x11-xserver-utils",
					"x11-utils", // xdpyinfo for recording display probe
					// openssl for generating the KasmVNC snakeoil cert directly into
					// /home/browser/.vnc/ (avoids unprivileged container chmod restrictions
					// on /etc/ssl/private/).
					"openssl",
					// CDP port forwarding (Chromium binds DevTools to loopback only)
					"socat",
					// Session recording (ffmpeg x11grab of the KasmVNC display)
					"ffmpeg",
					// Utilities
					"wget", "ca-certificates",
				},
			)
		}
	}

	// Common: create browser user (KasmVNC cannot run as root).
	// Use "id" guard to make this idempotent — the user may already exist
	// when re-configuring an existing @base template.
	cmds = append(cmds,
		[]string{"sh", "-c", "id browser >/dev/null 2>&1 || useradd -m -s /bin/bash browser"},
	)

	// Map Incus server architecture to Debian package architecture.
	// Incus returns "x86_64" or "aarch64"; .deb filenames use "amd64" or "arm64".
	debArch := "amd64"
	if arch == "aarch64" {
		debArch = "arm64"
	}

	// Common: install KasmVNC from release deb (distro-specific .deb).
	// Use apt-get install with the .deb path (not dpkg) — this resolves and
	// installs transitive dependencies in a single step. The dpkg + apt-get -f
	// pattern silently removes the package on Docker+Incus when deps fail.
	kasmURL := fmt.Sprintf(
		"https://github.com/kasmtech/KasmVNC/releases/download/v%s/kasmvncserver_%s_%s_%s.deb",
		kasmVNCVersion, deps.kasmVNCSuffix, kasmVNCVersion, debArch,
	)
	cmds = append(cmds,
		// Download the KasmVNC .deb (architecture-aware)
		[]string{"wget", "-q", "-O", "/tmp/kasmvnc.deb", kasmURL},
		// Install with apt-get which resolves deps properly (requires apt 1.1+, Ubuntu 24.04 has 2.7+)
		[]string{"apt-get", "install", "-y", "/tmp/kasmvnc.deb"},
		// Clean up the .deb
		[]string{"rm", "-f", "/tmp/kasmvnc.deb"},
		// Create the browser user's .vnc directory (owned by the browser user)
		// BEFORE generating the cert. This directory is writable by us and
		// survives into session containers.
		[]string{"install", "-d", "-o", "browser", "-g", "browser", "-m", "755", "/home/browser/.vnc"},
		// Generate a self-signed cert directly into the user-owned .vnc
		// directory. We never touch /etc/ssl/private because in unprivileged
		// Incus containers (Docker+Incus on macOS, and some native setups)
		// the directory lives on a lower overlayfs layer and chmod/chown
		// fails with "Operation not permitted".
		//
		// KasmVNC validates that its configured cert files exist at startup
		// even when require_ssl=false. Pointing it at user-owned files in
		// /home/browser/.vnc/ makes this work reliably in all container modes.
		// The cert is never actually used for TLS (the exec tunnel terminates
		// TLS before it reaches the container).
		[]string{"sh", "-c",
			`openssl req -x509 -newkey rsa:2048 ` +
				`-keyout /home/browser/.vnc/snakeoil.key ` +
				`-out /home/browser/.vnc/snakeoil.pem ` +
				`-days 3650 -nodes -subj '/CN=localhost' 2>/dev/null && ` +
				`chown browser:browser /home/browser/.vnc/snakeoil.key /home/browser/.vnc/snakeoil.pem && ` +
				`chmod 600 /home/browser/.vnc/snakeoil.key`,
		},
	)

	// Common: configure KasmVNC for headless/non-interactive operation.
	// KasmVNC has several interactive prompts that hang ExecSimple (which has
	// no stdin): (1) user creation prompt, (2) desktop environment selection.
	// We pre-create all required files to skip these prompts entirely.
	//
	// All config file operations run as root (not via su/runuser) because in
	// unprivileged LXC containers on Docker+Incus, the UID shift applied by
	// ShiftTemplateRootfs makes /home/browser owned by nobody:nogroup from
	// the perspective of non-mapped users. We create files as root and set
	// ownership explicitly with chown.
	cmds = append(cmds,
		// Write kasmvnc.yaml with SSL disabled (TLS is terminated by the
		// astonish exec tunnel) and explicit paths to the user-owned cert
		// we just generated. This is the key change that makes base template
		// builds succeed on unprivileged Incus (Apple Silicon + Docker).
		[]string{"sh", "-c", fmt.Sprintf(`cat > /home/browser/.vnc/kasmvnc.yaml << 'KASMCFG'
%s
KASMCFG
chown browser:browser /home/browser/.vnc/kasmvnc.yaml`, kasmvncConfigYAML(1920, 1080))},
		// Create a minimal xstartup that just keeps the X session alive.
		// We don't need a desktop environment — KasmVNC's X server provides
		// the display, and the browser is already running headless via CDP.
		// For handoff we just need a window manager so the user can interact.
		[]string{"sh", "-c", `cat > /home/browser/.vnc/xstartup << 'XSTARTUP'
#!/bin/bash
# Minimal session: just keep the X server alive.
# The browser is controlled via CDP; KasmVNC provides visual access only.
exec sleep infinity
XSTARTUP
chmod +x /home/browser/.vnc/xstartup
chown browser:browser /home/browser/.vnc/xstartup`},
		// Mark desktop environment as already selected (skips select-de.sh prompt)
		[]string{"sh", "-c", "touch /home/browser/.vnc/.de-was-selected && chown browser:browser /home/browser/.vnc/.de-was-selected"},
		// Create a default KasmVNC user "user" with write permission.
		// The actual password is set at handoff time; this just ensures the
		// user entry exists so vncserver doesn't prompt for user creation.
		kasmPasswdCmd(distro),
	)

	// ARM64 (aarch64): build a small LD_PRELOAD shim that masks problematic
	// HWCAP/HWCAP2 CPU feature bits. On Apple Silicon Macs, the Docker Desktop
	// VM advertises advanced ARMv9 features (SVE2, SME, SME2, BF16, etc.) via
	// getauxval(AT_HWCAP/AT_HWCAP2). Libraries like libjpeg-turbo, Skia,
	// BoringSSL, and zlib detect these features at runtime and use optimized
	// code paths — but some of these instructions are not fully functional in
	// the nested virtualization stack (macOS → Docker VM → Incus LXC), causing
	// SIGILL crashes when rendering image-heavy pages.
	//
	// The shim intercepts getauxval() and masks out everything beyond baseline
	// ARMv8.0 + safe extensions (NEON, AES, SHA, CRC32, atomics). This forces
	// all libraries to use their baseline NEON code paths which work correctly.
	if arch == "aarch64" {
		// The C source for the HWCAP masking shim.
		hwcapShimSource := `
#define _GNU_SOURCE
#include <sys/auxv.h>
#include <dlfcn.h>

/* Safe HWCAP bits to keep (ARMv8.0 baseline + common extensions):
 *   FP, ASIMD, EVTSTRM, AES, PMULL, SHA1, SHA2, CRC32, ATOMICS,
 *   FPHP, ASIMDHP, CPUID, ASIMDRDM, JSCVT, FCMA, LRCPC, DCPOP,
 *   SHA3, ASIMDDP, SHA512, ASIMDFHM, DIT, USCAT, ILRCPC, FLAGM, SB
 * Masked out: SVE(22), SSBS(28), PACA(30), PACG(31), and bits 32+
 */
#define HWCAP_SAFE_MASK  0x2FBFFFFFul

/* Safe HWCAP2 bits: DCPODP(0), FLAGM2(7), FRINT(8), I8MM(13)
 * Masked out: SVE2, all SVE* variants, BF16, BTI, MTE, SME, SME2, etc.
 */
#define HWCAP2_SAFE_MASK 0x2181ul

unsigned long getauxval(unsigned long type) {
    unsigned long (*real_getauxval)(unsigned long) =
        (unsigned long (*)(unsigned long))dlsym(RTLD_NEXT, "getauxval");
    unsigned long val = real_getauxval(type);
    if (type == AT_HWCAP)  return val & HWCAP_SAFE_MASK;
    if (type == AT_HWCAP2) return val & HWCAP2_SAFE_MASK;
    return val;
}
`
		cmds = append(cmds,
			// Install gcc (needed to compile the shim)
			[]string{"apt-get", "install", "-y", "gcc"},
			// Write the C source, compile to shared library, clean up
			[]string{"sh", "-c",
				fmt.Sprintf(`cat > /tmp/hwcap_mask.c << 'SHIMEOF'
%s
SHIMEOF
gcc -shared -fPIC -o /usr/lib/hwcap_mask.so /tmp/hwcap_mask.c -ldl
rm -f /tmp/hwcap_mask.c`, hwcapShimSource),
			},
			// Remove gcc to keep the template lean (only needed at build time)
			[]string{"sh", "-c", "apt-get remove -y gcc && apt-get autoremove -y"},
		)
	}

	// CloakBrowser-specific: install the Python package and download the binary
	if engine == "cloakbrowser" {
		cmds = append(cmds,
			[]string{"pip3", "install", "--break-system-packages", "cloakbrowser"},
			// Download the CloakBrowser Chromium binary into ~browser/.cloakbrowser/
			// Run as the browser user so the binary lands in the right home directory.
			cloakBrowserEnsureBinaryCmd(distro),
		)
	}

	// Common: fix ownership of browser home directory.
	// In unprivileged LXC containers on Docker+Incus, the UID shift
	// (ShiftTemplateRootfs) makes files owned by nobody:nogroup after
	// the template is snapshotted. Ensure the browser user owns its
	// home directory and all files within it.
	cmds = append(cmds,
		[]string{"chown", "-R", "browser:browser", "/home/browser"},
	)

	// Common: clean up apt cache to reduce image size
	cmds = append(cmds,
		[]string{"apt-get", "clean"},
		[]string{"rm", "-rf", "/var/lib/apt/lists/*"},
	)

	return cmds
}

// kasmPasswdCmd returns the install step for pre-creating the KasmVNC
// "user" account. On Incus (DistroUbuntuNoble), this uses runuser to
// switch to the browser user so the password file is owned correctly.
// On K8s/fuse-overlayfs (DistroDebianBookworm), execve() as non-root
// returns EOPNOTSUPP on fuse.fuse-overlayfs mounts backed by NFS
// lowerdirs, so we run as root with an explicit output path instead.
func kasmPasswdCmd(distro LinuxDistro) []string {
	switch distro {
	case DistroDebianBookworm:
		// K8s sandbox chroot runs under squash_to_root semantics;
		// everything is UID 0. Run kasmvncpasswd directly as root,
		// specifying the password file path explicitly. We write to
		// BOTH /root/.kasmpasswd (where vncserver looks when running
		// as root at runtime) and /home/browser/.kasmpasswd (fallback
		// for when vncserver is launched with HOME=/home/browser).
		return []string{"sh", "-c",
			`printf "kasmvnc\nkasmvnc\n" | /usr/bin/kasmvncpasswd -u user -w /home/browser/.kasmpasswd && ` +
				`cp /home/browser/.kasmpasswd /root/.kasmpasswd`,
		}
	default: // DistroUbuntuNoble — Incus containers with real UID namespaces
		// Use runuser instead of su — in unprivileged LXC containers on
		// Docker+Incus, su fails with "Authentication failure" because
		// PAM can't read /etc/shadow (UID namespace mapping breaks
		// pam_unix). runuser (part of util-linux) bypasses PAM.
		return []string{"sh", "-c",
			`printf "kasmvnc\nkasmvnc\n" | runuser -u browser -- /usr/bin/kasmvncpasswd -u user -w`,
		}
	}
}

// cloakBrowserEnsureBinaryCmd returns the install step for downloading
// the CloakBrowser binary. Same fuse-overlayfs constraint as
// kasmPasswdCmd above: non-root execve is unsupported on K8s.
func cloakBrowserEnsureBinaryCmd(distro LinuxDistro) []string {
	switch distro {
	case DistroDebianBookworm:
		// Run as root; use HOME override so the binary lands in the
		// browser user's home directory.
		return []string{"sh", "-c",
			`HOME=/home/browser python3 -c "import cloakbrowser; print(cloakbrowser.ensure_binary())"`,
		}
	default: // DistroUbuntuNoble — Incus containers
		return []string{"runuser", "-u", "browser", "--",
			"python3", "-c", "import cloakbrowser; print(cloakbrowser.ensure_binary())",
		}
	}
}

// StartKasmVNC starts KasmVNC inside a container for human visual access.
// It runs as the "browser" user on the specified port.
//
// Authentication is disabled via -DisableBasicAuth because the Studio reverse
// proxy already provides access control.
//
// Prerequisites (handled by template install commands):
//   - KasmVNC installed via .deb
//   - "browser" user exists
//   - ~/.vnc/xstartup, ~/.vnc/.de-was-selected, ~/.vnc/kasmvnc.yaml pre-created
//   - Default "user" KasmVNC account pre-created with kasmvncpasswd
func StartKasmVNC(client *IncusClient, containerName string, cfg BrowserContainerConfig) error {
	port := cfg.KasmVNCPort
	if port == 0 {
		port = DefaultKasmVNCPort
	}

	width := cfg.ViewportWidth
	if width == 0 {
		width = 1920
	}
	height := cfg.ViewportHeight
	if height == 0 {
		height = 1080
	}

	// Rewrite user yaml on every start so old session templates (and the
	// package default of 1024×768) cannot win when allow_resize is false.
	writeYAML := []string{"sh", "-c", fmt.Sprintf(`mkdir -p /home/browser/.vnc && cat > /home/browser/.vnc/kasmvnc.yaml << 'KASMCFG'
%s
KASMCFG
chown browser:browser /home/browser/.vnc/kasmvnc.yaml`, kasmvncConfigYAML(width, height))}
	if exitCode, output, err := ExecWithOutput(client, containerName, writeYAML); err != nil {
		return fmt.Errorf("failed to write KasmVNC config: %w (output: %s)", err, output)
	} else if exitCode != 0 {
		return fmt.Errorf("failed to write KasmVNC config (exit %d): %s", exitCode, strings.TrimSpace(output))
	}

	// Use runuser instead of su — in unprivileged LXC containers on
	// Docker+Incus, su fails with "Authentication failure" because PAM
	// can't read /etc/shadow through the UID namespace mapping.
	// runuser (part of util-linux) bypasses PAM authentication.
	//
	// Note: /home/browser ownership is correct because ShiftTemplateRootfs
	// shifts ALL UIDs (not just root) during template creation. The shifted
	// UIDs are captured in the snapshot and inherited by session containers.
	geometry := fmt.Sprintf("%dx%d", width, height)
	// -AcceptSetDesktopSize 0 prevents the Studio/noVNC client from shrinking
	// the X framebuffer to the host canvas (breaks ffmpeg x11grab at configured size).
	startCmd := []string{"runuser", "-l", "browser", "-c",
		fmt.Sprintf("vncserver :%s -geometry %s -depth 24 -websocketPort %d -DisableBasicAuth -AcceptSetDesktopSize 0",
			kasmVNCDisplay,
			geometry,
			port,
		),
	}

	// Use ExecWithOutput to capture stdout+stderr — previous iterations
	// of this code used ExecSimple which discarded all output, making it
	// impossible to diagnose failures without manual incus exec debugging.
	exitCode, output, err := ExecWithOutput(client, containerName, startCmd)
	if err != nil {
		return fmt.Errorf("failed to start KasmVNC: %w (output: %s)", err, output)
	}
	// Exit code 29 means "a VNC server is already running" on this display.
	if exitCode != 0 && exitCode != 29 {
		return fmt.Errorf("KasmVNC start exited with code %d: %s", exitCode, strings.TrimSpace(output))
	}

	return nil
}

// StopKasmVNC is a no-op. Xvnc serves as the X display server for headed
// Chromium and must remain running for the container's lifetime. VNC proxy
// access is controlled by the handoff token registry in the auth middleware.
func StopKasmVNC(_ *IncusClient, _ string, _ int) error {
	return nil
}

// browserStackHealthyScript returns a shell snippet that exits 0 when Chromium
// (or CloakBrowser) and the socat CDP bridge are both listening. Used for
// idempotent StartChromiumInContainer and script-level skip-if-running.
func browserStackHealthyScript() string {
	return fmt.Sprintf(
		`pgrep -u browser -f -- '--remote-debugging-port=' >/dev/null 2>&1 && `+
			`{ ss -tln 2>/dev/null | grep -q ':%d ' || grep -qi ':%04X ' /proc/net/tcp 2>/dev/null; } && `+
			`{ ss -tln 2>/dev/null | grep -q ':%d ' || grep -qi ':%04X ' /proc/net/tcp 2>/dev/null; }`,
		internalCDPPort, internalCDPPort,
		DefaultCDPPort, DefaultCDPPort,
	)
}

// socatBridgeKillPattern is the pkill/pgrep -f pattern for the CDP socat
// bridge. Anchored at ^ so it matches only a real `socat TCP-LISTEN:…`
// argv, never a launcher shell whose cmdline embeds that string in an
// `sh -c` script body.
func socatBridgeKillPattern() string {
	return fmt.Sprintf("^socat TCP-LISTEN:%d,", DefaultCDPPort)
}

// killStaleBrowserStackScript clears half-dead Chromium/socat remnants so a
// relaunch can rebind CDP ports cleanly. Match by --remote-debugging-port=
// (not bare chrome|chromium) and an argv-anchored socat pattern so pkill
// cannot hit a launcher shell whose cmdline embeds those words.
func killStaleBrowserStackScript() string {
	return fmt.Sprintf(
		`pkill -u browser -f -- '--remote-debugging-port=' 2>/dev/null || true
pkill -u browser -f -- '/home/browser/.cloakbrowser/' 2>/dev/null || true
pkill -f -- '%s' 2>/dev/null || true
sleep 0.5
`, socatBridgeKillPattern())
}

// cdpVerifyScript polls until the internal DevTools port is listening, or fails
// with a diagnostic message. engineLabel is used in error text only.
func cdpVerifyScript(engineLabel string) string {
	return fmt.Sprintf(`
CDP_READY=0
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ss -tln 2>/dev/null | grep -q ':%d '; then
    CDP_READY=1
    break
  fi
  sleep 0.5
done
if [ "$CDP_READY" = "0" ]; then
  echo "%s started but DevTools port %d is not listening after 5s." >&2
  echo "Listening ports:" >&2
  ss -tln >&2 2>/dev/null
  if [ -n "$BROWSER_LOG" ] && [ -f "$BROWSER_LOG" ]; then
    echo "--- browser log ---" >&2
    cat "$BROWSER_LOG" >&2 2>/dev/null
    echo "--- end log ---" >&2
  fi
  exit 1
fi
`, internalCDPPort, engineLabel, internalCDPPort)
}

// buildLaunchScript generates the shell script that starts the browser inside
// the container. Extracted from StartChromiumInContainer so it can be tested
// without a real IncusClient.
//
// The script is idempotent: if Chromium + socat are already healthy it exits 0
// without stacking another instance. Half-dead remnants are killed first.
func buildLaunchScript(engine string, cfg BrowserContainerConfig, width, height int) string {
	// socat bridge: expose CDP on all interfaces. Skip if already listening.
	// Use the same argv-anchored pattern as killStale (pgrep is read-only
	// but keep patterns consistent so "already up" detection matches kills).
	socatBridge := fmt.Sprintf(
		"if ! pgrep -f -- '%s' >/dev/null 2>&1; then\n"+
			"  socat TCP-LISTEN:%d,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:%d &\n"+
			"fi\n",
		socatBridgeKillPattern(), DefaultCDPPort, internalCDPPort,
	)

	display := fmt.Sprintf(":%s", kasmVNCDisplay)

	// On Docker+Incus (Apple Silicon Macs), the Docker Desktop VM advertises
	// advanced ARMv9 CPU features (SVE2, SME, SME2, BF16, BTI, etc.) via
	// getauxval(AT_HWCAP/AT_HWCAP2). Libraries bundled in Chromium
	// (libjpeg-turbo, Skia, BoringSSL, zlib) detect these at runtime and use
	// optimized code paths — but some instructions are not fully functional
	// through the nested virtualization stack (macOS → Docker VM → Incus LXC),
	// causing SIGILL crashes on image-heavy pages.
	//
	// The fix is hwcap_mask.so (compiled during template creation for aarch64),
	// which intercepts getauxval() and masks HWCAP/HWCAP2 down to safe ARMv8.0
	// baseline features. This forces all libraries to use their baseline NEON
	// code paths which work correctly.
	ldPreload := ""
	if GetActivePlatform() == PlatformDockerIncus {
		ldPreload = "LD_PRELOAD=/usr/lib/hwcap_mask.so "
	}

	idempotentPreamble := fmt.Sprintf(`# Idempotent: reuse a healthy browser stack (CDP reconnect path).
if %s; then
  %sexit 0
fi
# Half-dead remnants (proc without port, or port without proc) break relaunch.
%s`, browserStackHealthyScript(), socatBridge, killStaleBrowserStackScript())

	switch engine {
	case "cloakbrowser":
		fingerprintFlags := ""
		if cfg.FingerprintSeed != "" {
			fingerprintFlags += fmt.Sprintf(" --fingerprint %s", cfg.FingerprintSeed)
		}
		if cfg.FingerprintPlatform != "" {
			fingerprintFlags += fmt.Sprintf(" --fingerprint-platform %s", cfg.FingerprintPlatform)
		}

		proxyFlag := ""
		if cfg.Proxy != "" {
			proxyFlag = fmt.Sprintf(" --proxy-server=%s", cfg.Proxy)
		}

		return fmt.Sprintf(`%s
BROWSER_BIN=$(runuser -u browser -- python3 -c "from cloakbrowser.config import get_binary_path; print(get_binary_path())")
if [ -z "$BROWSER_BIN" ] || [ ! -f "$BROWSER_BIN" ]; then
  echo "CloakBrowser binary not found" >&2
  exit 1
fi
export DISPLAY=%s
BROWSER_LOG=/tmp/cloakbrowser.log
# Launch CloakBrowser as the browser user on the Xvnc display.
# Redirect stdout+stderr to a log file so we can diagnose crashes.
runuser -l browser -c "%sDISPLAY=%s $BROWSER_BIN \
  --no-sandbox \
  --test-type \
  --disable-gpu \
  --disable-dev-shm-usage \
  --remote-debugging-port=%d \
  --window-size=%d,%d \
  --user-data-dir=%s \
  --disable-background-timer-throttling \
  --disable-backgrounding-occluded-windows \
  --disable-renderer-backgrounding \
  --disable-blink-features=AutomationControlled \
  --no-first-run \
  --no-default-browser-check \
  --noerrdialogs \
  --disable-features=TranslateUI%s%s \
  about:blank >$BROWSER_LOG 2>&1 &"
sleep 2
# Verify the browser process started by checking if any chrome/chromium
# process is running. If not, the binary crashed on startup — report the
# log so the error propagates to the user instead of a generic CDP timeout.
if ! pgrep -u browser -f 'chrome|chromium' >/dev/null 2>&1; then
  echo "CloakBrowser process died on startup. Binary: $BROWSER_BIN" >&2
  echo "--- browser log ---" >&2
  cat $BROWSER_LOG >&2 2>/dev/null
  echo "--- end log ---" >&2
  exit 1
fi
%s
# Bridge CDP port to all interfaces so the host can connect
%s`, idempotentPreamble, display, ldPreload, display, internalCDPPort, width, height, BrowserProfileMountPath, fingerprintFlags, proxyFlag,
			cdpVerifyScript("CloakBrowser"), socatBridge)

	default: // "default" — headed Google Chrome (/usr/bin/chromium via symlink)
		proxyFlag := ""
		if cfg.Proxy != "" {
			proxyFlag = fmt.Sprintf(" --proxy-server=%s", cfg.Proxy)
		}

		return fmt.Sprintf(`%s
export DISPLAY=%s
BROWSER_LOG=/tmp/chromium.log
runuser -l browser -c "%sDISPLAY=%s chromium \
  --no-sandbox \
  --test-type \
  --disable-gpu \
  --disable-dev-shm-usage \
  --remote-debugging-port=%d \
  --window-size=%d,%d \
  --user-data-dir=%s \
  --disable-background-timer-throttling \
  --disable-backgrounding-occluded-windows \
  --disable-renderer-backgrounding \
  --disable-blink-features=AutomationControlled \
  --no-first-run \
  --no-default-browser-check \
  --noerrdialogs \
  --disable-features=TranslateUI%s \
  about:blank >$BROWSER_LOG 2>&1 &"
sleep 2
if ! pgrep -u browser -f 'chrome|chromium' >/dev/null 2>&1; then
  echo "Chromium process died on startup." >&2
  echo "--- browser log ---" >&2
  cat $BROWSER_LOG >&2 2>/dev/null
  echo "--- end log ---" >&2
  exit 1
fi
%s
# Bridge CDP port to all interfaces so the host can connect
%s`, idempotentPreamble, display, ldPreload, display, internalCDPPort, width, height, BrowserProfileMountPath, proxyFlag,
			cdpVerifyScript("Chromium"), socatBridge)
	}
}

// isBrowserStackHealthy reports whether Chromium (or CloakBrowser) and the
// socat CDP bridge are already up inside the container.
func isBrowserStackHealthy(client *IncusClient, containerName string) bool {
	if client == nil || containerName == "" {
		return false
	}
	cmd := []string{"sh", "-c", browserStackHealthyScript()}
	exitCode, err := client.ExecSimple(containerName, cmd)
	return err == nil && exitCode == 0
}

// StartChromiumInContainer launches the browser inside the container with
// remote debugging enabled so go-rod can connect via CDP.
//
// Chromium always binds its DevTools server to 127.0.0.1 (the
// --remote-debugging-address flag only works in content_shell, not the full
// browser). We work around this by running Chromium on internalCDPPort
// (loopback) and using socat to forward from 0.0.0.0:DefaultCDPPort to
// 127.0.0.1:internalCDPPort, making CDP accessible from the host.
//
// Both engines run Chromium in headed mode on DISPLAY=:0. The X server is
// provided by KasmVNC (Xvnc), which is started first and persists for the
// container's lifetime.
//
// Idempotent: if the browser stack is already healthy, this is a no-op so
// CDP reconnect after a dead pipe does not stack Chromium/socat processes.
func StartChromiumInContainer(client *IncusClient, containerName string, cfg BrowserContainerConfig) error {
	width := cfg.ViewportWidth
	if width == 0 {
		width = 1920
	}
	height := cfg.ViewportHeight
	if height == 0 {
		height = 1080
	}

	// Start KasmVNC (Xvnc) as the X server for display :0. This provides
	// both the virtual display for headed Chromium and the VNC web client
	// for human handoff sessions. Xvnc stays alive for the container's
	// lifetime — it IS the display backend.
	if err := StartKasmVNC(client, containerName, cfg); err != nil {
		return fmt.Errorf("failed to start Xvnc display server: %w", err)
	}

	// Fast path: Chromium + socat already healthy (Manager reconnect).
	if isBrowserStackHealthy(client, containerName) {
		slog.Info("browser stack already healthy; skipping relaunch",
			"component", "incus-browser", "container", containerName)
		return nil
	}

	// Brief wait for Xvnc to be ready to accept X11 connections.
	time.Sleep(1 * time.Second)

	// Allow any local user (root, browser) to connect to the Xvnc display.
	xhostCmd := []string{"runuser", "-l", "browser", "-c",
		fmt.Sprintf("DISPLAY=:%s xhost +local:", kasmVNCDisplay),
	}
	exitCode, err := client.ExecSimple(containerName, xhostCmd)
	if err != nil {
		slog.Warn("xhost +local: failed", "container", containerName, "error", err)
	} else if exitCode != 0 {
		slog.Warn("xhost +local: exited with non-zero code", "container", containerName, "exit_code", exitCode)
	}

	engine := DetectBrowserEngine(cfg)

	launchScript := buildLaunchScript(engine, cfg, width, height)

	// Use ExecWithOutput to capture stdout+stderr for diagnostic error messages.
	// The launch script runs background processes (chromium &, socat &) so we
	// wrap it in sh -c ourselves rather than using ExecWithOutput's wrapper.
	if err := runBrowserLaunchScript(client, containerName, launchScript); err != nil {
		// Transient SIGTERM (exit 143) often races idle stop / exec teardown.
		// Retry once after a short pause; launch script itself killStales first.
		if isLaunchExit143(err) {
			slog.Warn("browser launch interrupted by SIGTERM; retrying once",
				"component", "incus-browser", "container", containerName, "error", err)
			time.Sleep(1 * time.Second)
			if retryErr := runBrowserLaunchScript(client, containerName, launchScript); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}

	// Wait briefly for Chromium + socat to settle after launch.
	time.Sleep(1 * time.Second)

	return nil
}

// runBrowserLaunchScript writes the launch script to a temp file inside the
// container, then runs it by path so the launcher process argv does not embed
// pkill needles (killStale patterns). Returns a diagnostic error on non-zero exit.
func runBrowserLaunchScript(client *IncusClient, containerName, launchScript string) error {
	if err := writeBrowserLaunchScript(client, containerName, launchScript); err != nil {
		return err
	}
	cmd := []string{"sh", browserLaunchScriptPath}
	exitCode, output, err := ExecWithOutput(client, containerName, cmd)
	if err != nil {
		return fmt.Errorf("failed to launch browser: %w (output: %s)", err, strings.TrimSpace(output))
	}
	if exitCode == 0 {
		return nil
	}
	return formatBrowserLaunchExitError(client, containerName, exitCode, output)
}

// writeBrowserLaunchScript streams the script to browserLaunchScriptPath via
// tee so the content never appears in a long-lived process argv.
func writeBrowserLaunchScript(client *IncusClient, containerName, script string) error {
	proc, err := ExecNonInteractive(client, containerName, []string{"tee", browserLaunchScriptPath}, ExecOpts{})
	if err != nil {
		return fmt.Errorf("failed to write browser launch script: %w", err)
	}

	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		_, _ = io.Copy(io.Discard, proc.Stdout)
	}()

	if _, err := io.WriteString(proc.Stdin, script); err != nil {
		_ = proc.Close()
		<-drainDone
		return fmt.Errorf("failed to write browser launch script: %w", err)
	}
	// Close stdin (via Close) so tee gets EOF, flushes, and exits.
	_ = proc.Close()

	exitCode, err := proc.Wait()
	<-drainDone
	if err != nil {
		return fmt.Errorf("failed to write browser launch script: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("failed to write browser launch script: tee exited with code %d", exitCode)
	}
	return nil
}

// formatBrowserLaunchExitError builds a launch failure error. Exit 143 is
// labeled as SIGTERM. When stdout/stderr is empty, pull browser logs from the
// container so agents see a real diagnostic instead of inventing causes.
func formatBrowserLaunchExitError(client *IncusClient, containerName string, exitCode int, output string) error {
	out := strings.TrimSpace(output)
	if out == "" || exitCode == 143 {
		if logTail := readBrowserLaunchLog(client, containerName); logTail != "" {
			if out == "" {
				out = logTail
			} else {
				out = out + "\n" + logTail
			}
		}
	}
	if exitCode == 143 {
		if out == "" {
			return fmt.Errorf("browser launch exited with code 143 (interrupted by SIGTERM)")
		}
		return fmt.Errorf("browser launch exited with code 143 (interrupted by SIGTERM): %s", out)
	}
	if out == "" {
		return fmt.Errorf("browser launch exited with code %d", exitCode)
	}
	return fmt.Errorf("browser launch exited with code %d: %s", exitCode, out)
}

func isLaunchExit143(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "exited with code 143") || strings.Contains(s, "interrupted by SIGTERM")
}

func readBrowserLaunchLog(client *IncusClient, containerName string) string {
	if client == nil || containerName == "" {
		return ""
	}
	cmd := []string{"sh", "-c",
		`for f in /tmp/cloakbrowser.log /tmp/chromium.log; do
  if [ -f "$f" ]; then
    echo "--- $f ---"
    tail -n 80 "$f" 2>/dev/null || cat "$f" 2>/dev/null
    echo "--- end $f ---"
  fi
done`,
	}
	_, out, err := ExecWithOutput(client, containerName, cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
