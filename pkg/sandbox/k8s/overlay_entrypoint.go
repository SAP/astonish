// Package k8s — sandbox-pod entrypoint script generator.
//
// The sandbox container runs a single PID-1 entrypoint whose job is:
//
//  1. Streaming the persisted upper layer back onto the local emptyDir
//     when resuming a previously-evicted session. Session pods mount only
//     their own uppers PVC subdirectory, so the live path is
//     /mnt/astonish-uppers/upper.tar.zst.
//  2. Composing the overlay chain encoded in $ASTONISH_LAYER_CHAIN as
//     lowerdirs (top-most first, per overlayfs syntax), with the
//     emptyDir at /var/astonish/overlay/upper as upperdir and
//     /var/astonish/overlay/work as workdir, mounted at /sandbox/rootfs.
//     Both upper and work MUST reside under the same mount point to
//     avoid cross-device renameat failures in fuse-overlayfs.
//  3. Handing off to the workload via chroot into the composed rootfs.
//
// The overlay in step 2 is formed by one of three strategies — see
// OverlayMode. The chosen strategy determines which capability /
// runtime mechanism the operator relies on (kernel overlayfs inside a
// user namespace, fuse-overlayfs via /dev/fuse, or a dynamic pick). The
// script itself is parameterised so a single base image can serve any
// deployment.
//
// This file owns the **source of truth** for the entrypoint script.
// Host builds emit it via `make sandbox-entrypoint` (wrapper around
// `go run ./cmd/astonish-sandbox-entrypoint-script`); the
// docker/sandbox-base image then COPYs the file to
// /usr/local/bin/astonish-sandbox-entrypoint. Keeping the generator in
// Go lets us unit-test its shape without a CI shell harness and lets
// Astonish evolve the runtime contract (env var names, mount paths,
// resume semantics) in lockstep with the backend that produces those
// inputs.
//
// Design invariants enforced by the script:
//
//   - `set -euo pipefail` — fail fast; unset variables are errors.
//   - Resume-tar extraction MUST happen before the overlay mount so
//     the emptyDir at /var/astonish/overlay/upper is populated first.
//   - Resume-tar extraction is SKIPPED if the tarball is absent (fresh
//     session path). Absence is not an error.
//   - Layer-chain composition reverses the comma-separated
//     $ASTONISH_LAYER_CHAIN so the top-most (last element) becomes the
//     first lowerdir — overlayfs expects the layer closest to upper
//     first. This mirrors buildFleetPodManifest's / buildPodManifest's
//     conventional oldest-first slice order.
//   - The handoff is `exec chroot` so PID 1 semantics transfer to the
//     workload (signal propagation, reaping).
//   - fuse-overlayfs, when chosen, runs as a background daemon BEFORE
//     the chroot handoff; PID 1 still hands off to the workload via
//     exec chroot, and the daemon is reaped by the container when PID 1
//     exits.
//
// Reference: docs/architecture/sandbox-backends.md §5.3 step 3; §5.14
// for the resume-tar layout.

package k8s

import "strings"

// OverlayMode selects how the entrypoint composes the rootfs overlay.
// See the package-level comment for the deployment matrix that drives
// the choice.
type OverlayMode string

const (
	// OverlayModeFuse runs fuse-overlayfs as a background daemon. It
	// is the most portable option: works on any kernel ≥4.18 and
	// doesn't require CAP_SYS_ADMIN for the mount itself — only
	// access to /dev/fuse, which operators grant either via a
	// device plugin (unprivileged path) or via privileged: true (dev
	// escape hatch). This is the default.
	OverlayModeFuse OverlayMode = "fuse"

	// OverlayModeKernel runs `mount -t overlay -o userxattr`. It's
	// native performance but requires one of:
	//   - hostUsers: false on the Pod (K8s 1.33+; kernel 5.11+)
	//   - privileged: true
	//   - a specialised runtime (Sysbox, Kata) that provides the
	//     needed capability inside a user namespace.
	// It's also blocked on nested-LXC Kubernetes nodes (e.g.
	// Proxmox-hosted dev clusters) where the host kernel refuses the
	// overlay mount type regardless of in-container privileges.
	OverlayModeKernel OverlayMode = "kernel"

	// OverlayModeAuto tries kernel overlayfs first and falls back to
	// fuse-overlayfs on failure. Both binaries must be present in
	// the base image. The entrypoint emits a stderr line identifying
	// which path succeeded so operators can diagnose capability
	// mismatches without digging into mount(2) return codes.
	OverlayModeAuto OverlayMode = "auto"
)

// EntrypointScriptOptions tunes the emitted script for non-default
// deployments. The zero value produces the canonical script used by
// the astonish-sandbox-base image.
//
// Fields are all optional; empty means "use the spec's default".
type EntrypointScriptOptions struct {
	// UppersMount is the in-container mount path of the uppers PVC.
	// Default: /mnt/astonish-uppers (mountUppers).
	UppersMount string

	// LayersMount is the in-container mount path of the layers PVC.
	// Default: /mnt/astonish-layers (mountLayers).
	LayersMount string

	// UpperDir is the emptyDir path that overlayfs uses as its
	// upperdir. Default: /var/astonish/overlay/upper (mountUpper).
	UpperDir string

	// WorkDir is the emptyDir path that overlayfs uses as its
	// workdir. Default: /var/astonish/overlay/work (mountWork).
	WorkDir string

	// MountPoint is where the composed overlay is mounted. Default:
	// /sandbox/rootfs.
	MountPoint string

	// Handoff is the absolute path of the command to exec after the
	// overlay is in place (PID 1 hands off via `exec chroot`).
	// Default: /usr/local/bin/astonish (the daemon's node binary;
	// §5.3 step 3).
	Handoff string

	// HandoffArgs are the arguments passed to Handoff.
	// Default: []string{"node"}.
	HandoffArgs []string

	// HostBinaryPath, when non-empty, is the absolute path of a
	// BASE-IMAGE astonish binary that the entrypoint will bind-mount
	// over $MOUNT_POINT/usr/local/bin/astonish before the chroot
	// handoff. This lets the sandbox-base image ship a trusted binary
	// that covers BOTH the PID-1 handoff AND backend-driven tool calls
	// (Backend.Exec).
	//
	// When empty, the bind-mount step is skipped and Handoff must
	// already exist inside the overlay at its default path — matches
	// pre-bind-mount behaviour and keeps the shape unchanged for
	// tests that don't care about Backend.Exec.
	//
	// Default: /usr/local/bin/astonish-host (the path used by
	// docker/sandbox-base/Dockerfile). Tests that want to assert on
	// the pre-bind-mount handoff-only shape can set this to "-" to
	// explicitly suppress the bind-mount.
	HostBinaryPath string

	// HostTreeSitterLibPath, when non-empty, is the absolute path of the
	// BASE-IMAGE tree-sitter shared library that the entrypoint will
	// bind-mount into the overlay at
	// /usr/lib/astonish/libastonish-treesitter.so before the chroot
	// handoff. Code intelligence tools (repo_map, code_definition,
	// code_references) dlopen this path inside astonish node after
	// chroot; baking the library into the pod image alone is not
	// enough because the overlay rootfs does not include the pod
	// base-image filesystem.
	//
	// Default: /usr/lib/astonish/libastonish-treesitter.so (the path
	// used by docker/sandbox-base/Dockerfile and pkg/codeintel).
	// Value "-" suppresses the bind-mount entirely.
	HostTreeSitterLibPath string

	// Mode selects the overlay mount strategy; see OverlayMode.
	// Default: OverlayModeFuse — the most portable option.
	Mode OverlayMode

	// EnsureFuseDevice, when true, instructs the fuse path to create
	// /dev/fuse via mknod(1) before invoking fuse-overlayfs. This is
	// needed in privileged pods on clusters without a FUSE device
	// plugin: the device node doesn't exist by default in the
	// container's /dev tmpfs, but the container can create it because
	// the privileged context disables the device cgroup.
	// When false, /dev/fuse is assumed to be pre-mounted by a device
	// plugin (e.g. smarter-device-manager).
	//
	// Default: true (match the privileged/dev deployment). Operators
	// using a device plugin should set this to false.
	EnsureFuseDevice *bool

	// BindKernelFS, when true (the default), instructs the entrypoint
	// to bind-mount /dev, /proc, and /sys from the pod's base namespace
	// into the overlay rootfs before the chroot handoff. This makes the
	// kubelet-populated /dev (including /dev/ptmx for PTY allocation),
	// /proc, and /sys visible inside the chroot.
	//
	// Without this, tools that require a PTY (shell_command, ssh) fail
	// with "open /dev/ptmx: no such file or directory" because the
	// overlay's own /dev directory is empty.
	//
	// The bind-mounts use --rbind so submounts (e.g. /dev/pts, /dev/shm)
	// come along, and --make-rslave so unmounts inside the chroot do not
	// propagate back to the pod's mount table.
	//
	// Default: true. Set to false only if your @base layer already ships
	// a fully-populated /dev or if you mount kernel filesystems via a
	// custom init process.
	BindKernelFS *bool
}

func (o *EntrypointScriptOptions) applyDefaults() {
	if o.UppersMount == "" {
		o.UppersMount = mountUppers
	}
	if o.LayersMount == "" {
		o.LayersMount = mountLayers
	}
	if o.UpperDir == "" {
		o.UpperDir = mountUpper
	}
	if o.WorkDir == "" {
		o.WorkDir = mountWork
	}
	if o.MountPoint == "" {
		o.MountPoint = "/sandbox/rootfs"
	}
	if o.Handoff == "" {
		o.Handoff = "/bin/sleep"
	}
	if len(o.HandoffArgs) == 0 {
		o.HandoffArgs = []string{"infinity"}
	}
	if o.HostBinaryPath == "" {
		o.HostBinaryPath = "/usr/local/bin/astonish-host"
	}
	if o.HostTreeSitterLibPath == "" {
		o.HostTreeSitterLibPath = "/usr/lib/astonish/libastonish-treesitter.so"
	}
	if o.Mode == "" {
		o.Mode = OverlayModeFuse
	}
	if o.EnsureFuseDevice == nil {
		b := true
		o.EnsureFuseDevice = &b
	}
	if o.BindKernelFS == nil {
		b := true
		o.BindKernelFS = &b
	}
}

// EntrypointScript returns the POSIX-shell source of the sandbox-pod
// entrypoint, parameterised by opts. The returned script is
// self-contained (no external files required) and must be written to
// the image at /usr/local/bin/astonish-sandbox-entrypoint with
// executable mode.
//
// Env vars consumed at runtime:
//
//   - ASTONISH_SESSION_ID (required): key used to locate the persisted
//     upper tarball on the uppers PVC when resuming.
//   - ASTONISH_LAYER_CHAIN (required): comma-separated list of layer
//     IDs, oldest → newest. The script reverses this for overlayfs.
//
// Failure modes:
//
//   - Missing env var → script exits non-zero (via `set -u`).
//   - Empty layer chain → script exits non-zero with a clear error
//     message; a session with no layers is a configuration bug.
//   - Mount failure → script exits non-zero; the pod transitions to
//     Failed and the kubelet surfaces the stderr in logs.
func EntrypointScript(opts EntrypointScriptOptions) string {
	opts.applyDefaults()

	var b strings.Builder
	b.WriteString(`#!/bin/sh
# astonish-sandbox-entrypoint — PID-1 overlay composer.
#
# DO NOT EDIT BY HAND. Generated by pkg/sandbox/k8s.EntrypointScript.
# See docs/architecture/sandbox-backends.md §5.3 step 3 for the
# design rationale.

set -eu
# pipefail is not POSIX but is supported by bash/ash/busybox/zsh. In
# dash (Debian /bin/sh), "set -o pipefail" aborts the whole shell when
# pipefail is unknown — even with a trailing "|| true" — so we wrap it
# in a subshell to isolate the failure, then re-apply it to the parent
# shell only if the probe succeeded. Best-effort; absence is tolerated.
if ( set -o pipefail ) 2>/dev/null; then
  set -o pipefail
fi

: "${ASTONISH_SESSION_ID:?ASTONISH_SESSION_ID must be set}"
: "${ASTONISH_LAYER_CHAIN:?ASTONISH_LAYER_CHAIN must be set}"

`)
	// Emit path vars up top so the script reads top-down and the
	// later commands are obvious.
	b.WriteString("UPPERS_DIR=")
	writeSingleQuoted(&b, opts.UppersMount)
	b.WriteByte('\n')
	b.WriteString("LAYERS_DIR=")
	writeSingleQuoted(&b, opts.LayersMount)
	b.WriteByte('\n')
	b.WriteString("UPPER_DIR=")
	writeSingleQuoted(&b, opts.UpperDir)
	b.WriteByte('\n')
	b.WriteString("WORK_DIR=")
	writeSingleQuoted(&b, opts.WorkDir)
	b.WriteByte('\n')
	b.WriteString("MOUNT_POINT=")
	writeSingleQuoted(&b, opts.MountPoint)
	b.WriteString("\n")
	// Overlay mode is observable so that the mount helpers can emit
	// consistent stderr markers regardless of which branch was taken.
	b.WriteString("OVERLAY_MODE=")
	writeSingleQuoted(&b, string(opts.Mode))
	b.WriteString("\n\n")

	// Resume path.
	b.WriteString(`# --- 1. Resume from persisted upper (if present) -----------------------
# When a previously-evicted session is restarted, Astonish mounts only that
# session's uppers PVC subdirectory at $UPPERS_DIR. Stream the saved upper back
# onto the local emptyDir before the overlay mount. Older pods mounted the PVC
# root and used $UPPERS_DIR/<session>/upper.tar.zst; the fallback keeps those
# sessions resumable during rolling upgrades without exposing other sessions to
# the chrooted sandbox.
RESUME_TAR="$UPPERS_DIR/upper.tar.zst"
if [ ! -f "$RESUME_TAR" ] && [ -f "$UPPERS_DIR/$ASTONISH_SESSION_ID/upper.tar.zst" ]; then
  RESUME_TAR="$UPPERS_DIR/$ASTONISH_SESSION_ID/upper.tar.zst"
fi
if [ -f "$RESUME_TAR" ]; then
  echo "astonish-entrypoint: resuming upper from $RESUME_TAR" 1>&2
  mkdir -p "$UPPER_DIR"
  tar --numeric-owner --xattrs --acls -I zstd -xf "$RESUME_TAR" -C "$UPPER_DIR"
fi

# Always ensure the overlay dirs exist; on fresh sessions they're
# already present (emptyDir mounts) but this is cheap and idempotent.
mkdir -p "$UPPER_DIR" "$WORK_DIR" "$MOUNT_POINT"
`)

	// Pre-overlay: ensure /dev, /proc, /sys exist in the upper dir so
	// they'll be visible through the overlay after mount. This must
	// happen BEFORE the overlay is composed because fuse-overlayfs
	// takes a snapshot of the upperdir at mount time — modifications
	// to the upperdir after mount are not visible through the FUSE
	// mount (unlike kernel overlayfs which sees them immediately).
	if opts.BindKernelFS != nil && *opts.BindKernelFS {
		b.WriteString(`# Ensure /dev, /proc, /sys directories exist in the upper layer so
# they are visible through the overlay after mount (required for the
# kernel-filesystem bind-mounts in section 2b below). On fuse-overlayfs
# the upper must contain these dirs BEFORE the overlay is composed.
mkdir -p "$UPPER_DIR/dev" "$UPPER_DIR/proc" "$UPPER_DIR/sys"

`)
	}

	b.WriteString(`# --- 2. Compose overlay from layer chain -------------------------------
# $ASTONISH_LAYER_CHAIN is oldest-first (e.g., @base,org-layer,template).
# Overlayfs wants the top-most layer FIRST in its comma-separated
# lowerdir list, so we reverse. We pass $LAYERS_DIR into awk via -v so
# path substitution happens in awk (not a second eval pass), which
# keeps the script robust against metacharacters in the mount path.
LOWER=$(echo "$ASTONISH_LAYER_CHAIN" | awk -F, -v dir="$LAYERS_DIR" '
  {
    for (i = NF; i > 0; i--) {
      printf "%s/%s/rootfs%s", dir, $i, (i > 1 ? ":" : "")
    }
  }')
if [ -z "$LOWER" ]; then
  echo "astonish-entrypoint: empty ASTONISH_LAYER_CHAIN" 1>&2
  exit 1
fi

`)

	// Pre-seed: create all first-level directories from the bottommost
	// lowerdir inside the upperdir. fuse-overlayfs v1.10 (and older) on
	// NFS-backed lowerdirs cannot copy-up directories whose parent
	// inode lives on the NFS lowerdir (the internal rename/link across
	// NFS → local upper triggers EXDEV). By ensuring all top-level dirs
	// already exist in the upper BEFORE the mount, fuse-overlayfs never
	// needs to copy-up these directory inodes — only their children,
	// which succeeds because the parent is already local.
	//
	// This is harmless for kernel overlayfs (which handles cross-device
	// copy-up natively) and for non-NFS setups — an empty dir in the
	// upper simply merges with the lower's content via the overlay.
	//
	// We extract the bottommost layer (last entry in $LOWER, which is
	// top-first) and enumerate its immediate child directories.
	b.WriteString(`# --- 2pre. Pre-seed upper with first-level directories from lowest layer
# fuse-overlayfs cannot copy-up directory inodes whose parent lives on
# a different filesystem (NFS lowerdir → local upper triggers EXDEV).
# Pre-creating these dirs in the upper avoids the copy-up entirely.
# Harmless for kernel overlayfs and non-NFS setups.
#
# IMPORTANT: Only real directories are pre-seeded. Symlinks (e.g.
# /bin → usr/bin) must NOT be turned into directories in the upper,
# as that would shadow the symlink from the lower and break the
# filesystem layout.
BOTTOM_LAYER="${LOWER##*:}"
# If $LOWER has no colon, BOTTOM_LAYER == LOWER (single layer), which
# is still the bottom.
if [ -d "$BOTTOM_LAYER" ]; then
  for _d in "$BOTTOM_LAYER"/*/; do
    [ -d "$_d" ] || continue
    # Skip symlinks — they resolve as directories but must not be
    # replaced with real dirs in the upper.
    [ -L "${_d%/}" ] && continue
    _name="${_d%/}"
    _name="${_name##*/}"
    # Skip dirs already handled (dev/proc/sys from the kernel-fs step)
    # and avoid clobbering existing upper entries (resume path).
    if [ ! -d "$UPPER_DIR/$_name" ]; then
      mkdir -p "$UPPER_DIR/$_name"
    fi
  done
fi

`)

	// Emit overlay-mount helpers. Both helpers share contract:
	// on success they leave $MOUNT_POINT as a valid mountpoint; on
	// failure they return non-zero WITHOUT calling `exit` so the
	// caller (the dispatcher for `auto` mode) can retry.
	b.WriteString(`# Overlay-mount helpers. Each returns 0 on success and non-zero on
# failure; the caller decides whether to retry with a different
# strategy.

mount_overlay_kernel() {
  echo "astonish-entrypoint: trying kernel overlayfs at $MOUNT_POINT" 1>&2
  # userxattr enables overlayfs to use user.overlay.* xattrs instead
  # of trusted.overlay.*; this is what makes the mount work in a
  # user namespace where CAP_SYS_ADMIN is namespaced (kernel 5.11+).
  # It's a no-op in the privileged path, so safe to always pass.
  mount -t overlay overlay \
    -o "userxattr,lowerdir=$LOWER,upperdir=$UPPER_DIR,workdir=$WORK_DIR" \
    "$MOUNT_POINT"
}

mount_overlay_fuse() {
  echo "astonish-entrypoint: trying fuse-overlayfs at $MOUNT_POINT" 1>&2
`)
	if opts.EnsureFuseDevice != nil && *opts.EnsureFuseDevice {
		b.WriteString(`  # Some privileged-but-not-device-plugin environments don't expose
  # /dev/fuse in the container's /dev tmpfs. In a privileged context
  # the device cgroup is disabled, so the container can materialise
  # the character node itself (major 10, minor 229). If mknod fails
  # (unprivileged context) we continue: fuse-overlayfs will error
  # out with a clearer message, and the auto-mode dispatcher can
  # retry with kernel overlayfs.
  if [ ! -c /dev/fuse ]; then
    mknod /dev/fuse c 10 229 2>/dev/null || true
    chmod 0666 /dev/fuse 2>/dev/null || true
  fi
`)
	}
	b.WriteString(`  # fuse-overlayfs is a userspace FUSE daemon; it forks into the
  # background on success (default behaviour). Mount readiness is
  # asynchronous from the main-thread perspective, so we poll
  # /proc/self/mountinfo for the MOUNT_POINT entry with a short
  # timeout. 5s is generous — the daemon usually takes <50ms.
  #
  # squash_to_root: makes all files in the merged view appear owned by
  # uid/gid 0 from the FUSE perspective. Without this, the FUSE daemon
  # returns EOPNOTSUPP for open()/execve() calls made by non-root users
  # (e.g. apt's _apt sandbox user, uid 42). This is a known limitation
  # of fuse-overlayfs running as root outside a user namespace — the
  # daemon rejects cross-uid file operations via its default_permissions
  # policy. squash_to_root eliminates this restriction while preserving
  # actual ownership in the upper layer for tar capture.
  fuse-overlayfs \
    -o "lowerdir=$LOWER,upperdir=$UPPER_DIR,workdir=$WORK_DIR,squash_to_root" \
    "$MOUNT_POINT" || return $?
  i=0
  while [ "$i" -lt 100 ]; do
    if mountpoint -q "$MOUNT_POINT" 2>/dev/null; then
      return 0
    fi
    # Some busybox/util-linux builds lack mountpoint(1); fall back
    # to /proc/self/mountinfo directly.
    if grep -qE " $MOUNT_POINT " /proc/self/mountinfo 2>/dev/null; then
      return 0
    fi
    sleep 0.05
    i=$((i + 1))
  done
  echo "astonish-entrypoint: fuse-overlayfs mount did not appear within 5s" 1>&2
  return 1
}

`)

	// Dispatcher
	switch opts.Mode {
	case OverlayModeKernel:
		b.WriteString(`mount_overlay_kernel || {
  echo "astonish-entrypoint: kernel overlayfs mount failed (OVERLAY_MODE=kernel)" 1>&2
  exit 1
}
`)
	case OverlayModeAuto:
		b.WriteString(`# Auto mode: try kernel overlayfs first, fall back to fuse-overlayfs
# on any failure. The stderr lines emitted by each helper make the
# chosen path observable in pod logs.
if mount_overlay_kernel 2>&1; then
  echo "astonish-entrypoint: overlay composed via kernel overlayfs" 1>&2
elif mount_overlay_fuse; then
  echo "astonish-entrypoint: overlay composed via fuse-overlayfs (kernel path failed)" 1>&2
else
  echo "astonish-entrypoint: no overlay strategy succeeded (OVERLAY_MODE=auto)" 1>&2
  exit 1
fi
`)
	default: // OverlayModeFuse
		b.WriteString(`mount_overlay_fuse || {
  echo "astonish-entrypoint: fuse-overlayfs mount failed (OVERLAY_MODE=fuse)" 1>&2
  exit 1
}
`)
	}

	b.WriteString("\n")

	// Optional host-binary bind-mount. This is load-bearing for
	// Backend.Exec: it installs a trusted astonish binary into the
	// overlay at /usr/local/bin/astonish so BOTH the PID-1 handoff
	// below AND later Backend.Exec tool calls (which chroot into the
	// same overlay via the base image's wrapper) find the same
	// binary. Value "-" is a sentinel meaning "skip the bind-mount
	// entirely" — useful for backward-compat tests that pin the
	// pre-bind-mount shape.
	if opts.HostBinaryPath != "" && opts.HostBinaryPath != "-" {
		b.WriteString(`# --- 2a. Overlay astonish binary from the base image ------------------
# Bind-mount a trusted astonish binary from the base image on top of
# whatever the @base layer provided (or didn't) at the canonical path.
# This ensures both PID-1 handoff and Backend.Exec tool calls resolve
# to the same build, so operators don't have to ship astonish in every
# @base layer themselves.
`)
		b.WriteString("HOST_BIN=")
		writeSingleQuoted(&b, opts.HostBinaryPath)
		b.WriteByte('\n')
		b.WriteString(`OVERLAY_BIN="$MOUNT_POINT/usr/local/bin/astonish"
if [ -x "$HOST_BIN" ]; then
  # Ensure the destination exists; on a minimal @base layer the
  # directory may be missing entirely.
  mkdir -p "$MOUNT_POINT/usr/local/bin"
  # The destination must be a regular file for a file bind-mount.
  # We create an empty one if absent — overlayfs records this on
  # upperdir which is harmless and fast. Using ":" (the POSIX no-op)
  # with output redirection is the portable way to touch a file
  # without depending on touch(1).
  if [ ! -e "$OVERLAY_BIN" ]; then
    : > "$OVERLAY_BIN"
    chmod 0755 "$OVERLAY_BIN"
  fi
  mount --bind "$HOST_BIN" "$OVERLAY_BIN"
else
  echo "astonish-entrypoint: HOST_BIN=$HOST_BIN missing or not executable; " \
       "skipping bind-mount (PID-1 handoff will use overlay's binary)" 1>&2
fi

`)
	}

	// Optional tree-sitter library bind-mount. Code intelligence tools
	// run inside the chrooted overlay and dlopen this path; the library
	// is shipped on the sandbox-base image but is invisible after
	// chroot unless we inject it here (same contract as HostBinaryPath).
	if opts.HostTreeSitterLibPath != "" && opts.HostTreeSitterLibPath != "-" {
		b.WriteString(`# --- 2a2. Overlay tree-sitter library from the base image ------------
# Bind-mount the native tree-sitter shared library so codeintel tools
# (repo_map / code_definition / code_references) can dlopen it after
# chroot into the overlay.
`)
		b.WriteString("HOST_TS_LIB=")
		writeSingleQuoted(&b, opts.HostTreeSitterLibPath)
		b.WriteByte('\n')
		b.WriteString(`OVERLAY_TS_LIB="$MOUNT_POINT/usr/lib/astonish/libastonish-treesitter.so"
if [ -f "$HOST_TS_LIB" ]; then
  mkdir -p "$MOUNT_POINT/usr/lib/astonish"
  if [ ! -e "$OVERLAY_TS_LIB" ]; then
    : > "$OVERLAY_TS_LIB"
    chmod 0644 "$OVERLAY_TS_LIB"
  fi
  mount --bind "$HOST_TS_LIB" "$OVERLAY_TS_LIB"
else
  echo "astonish-entrypoint: HOST_TS_LIB=$HOST_TS_LIB missing; " \
       "skipping tree-sitter library bind-mount (codeintel tools will degrade)" 1>&2
fi

`)
	}

	// Section 2b: bind-mount kernel filesystems into the overlay rootfs
	// so the chroot has a working /dev (for PTY allocation), /proc, and
	// /sys. Without this, tools like shell_command fail with "open
	// /dev/ptmx: no such file or directory".
	if opts.BindKernelFS != nil && *opts.BindKernelFS {
		b.WriteString(`# --- 2b. Bind kernel filesystems into the overlay rootfs --------------
# A chroot inherits only the filesystem subtree under MOUNT_POINT.
# The pod's /dev (kubelet-populated, including /dev/ptmx for PTY
# allocation), /proc, and /sys are NOT visible inside the chroot
# unless we bind them in first. Tools that need a PTY (shell_command,
# ssh, screen) require /dev/pts to be reachable inside the chroot.
#
# --rbind: submounts (/dev/pts, /dev/shm, /dev/mqueue) come along.
# --make-rslave: unmounts inside the chroot do not propagate back to
# the pod's mount table (defence in depth).
#
# The target directories were pre-created in the upper dir (before
# the overlay mount) so they are guaranteed visible here.
#
# Idempotent: skips sources that are already mounted at the target.
for _src in /dev /proc /sys; do
  _dst="$MOUNT_POINT$_src"
  if ! grep -qE " $_dst " /proc/self/mountinfo 2>/dev/null; then
    if mount --rbind "$_src" "$_dst"; then
      mount --make-rslave "$_dst" 2>/dev/null || true
    else
      echo "astonish-entrypoint: warning: failed to rbind $_src at $_dst" 1>&2
    fi
  fi
done

# Bind the pod's /etc/resolv.conf into the overlay so DNS inside the
# chroot always uses the cluster's nameserver, regardless of what the
# base image ships. The target file must exist for mount --bind.
_RESOLV_SRC="/etc/resolv.conf"
_RESOLV_DST="$MOUNT_POINT/etc/resolv.conf"
if [ -f "$_RESOLV_SRC" ] && [ -e "$_RESOLV_DST" ]; then
  if ! grep -qE " $_RESOLV_DST " /proc/self/mountinfo 2>/dev/null; then
    mount --bind "$_RESOLV_SRC" "$_RESOLV_DST" 2>/dev/null || \
      echo "astonish-entrypoint: warning: failed to bind resolv.conf" 1>&2
  fi
fi

`)
	}

	b.WriteString(`# --- 3. Hand off to workload ------------------------------------------
# ASTONISH_HANDOFF / ASTONISH_HANDOFF_ARGS allow the pod manifest to
# override the PID-1 process after overlay composition WITHOUT
# requiring an image rebuild. The baked-in defaults below keep the
# image self-contained for docker-run diagnostics.
`)
	// Emit the baked-in defaults that the shell will use when the env
	// vars are unset. These are overridable per-pod via env vars.
	b.WriteString("_DEFAULT_HANDOFF=")
	b.WriteString(shellQuote(opts.Handoff))
	b.WriteByte('\n')

	b.WriteString("_DEFAULT_HANDOFF_ARGS=")
	// Build a space-separated, properly-quoted default args string for
	// the shell fallback. We use single-quote shell quoting so spaces
	// and special chars in individual args are preserved.
	for i, a := range opts.HandoffArgs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(a))
	}
	b.WriteByte('\n')

	b.WriteString(`HANDOFF="${ASTONISH_HANDOFF:-$_DEFAULT_HANDOFF}"
HANDOFF_ARGS="${ASTONISH_HANDOFF_ARGS:-$_DEFAULT_HANDOFF_ARGS}"
echo "astonish-entrypoint: handing off to $HANDOFF $HANDOFF_ARGS" 1>&2
# shellcheck disable=SC2086
exec chroot "$MOUNT_POINT" "$HANDOFF" $HANDOFF_ARGS
`)

	return b.String()
}

// writeSingleQuoted writes shellQuote(s) into b. Introduced so the
// top-of-script path initialisers read naturally; the real quoting is
// delegated to shellQuote in exec.go.
func writeSingleQuoted(b *strings.Builder, s string) {
	b.WriteString(shellQuote(s))
}
