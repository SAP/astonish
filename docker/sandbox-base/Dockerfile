# Dockerfile — astonish-sandbox-base image.
#
# This is the container image every Astonish sandbox pod runs in when
# the K8s backend is active (SandboxConfig.Backend = "k8s"). It is NOT
# the application image (see top-level Dockerfile for that); it's a
# minimal userland that serves as PID 1 inside the sandbox pod.
#
# The image contains, and is RESTRICTED to:
#
#   1. /usr/local/bin/astonish-sandbox-entrypoint
#        The POSIX-shell script whose source of truth is
#        pkg/sandbox/k8s.EntrypointScript. Generated at build time via
#        cmd/astonish-sandbox-entrypoint-script so there is no risk of
#        drift between the backend's expectations (env vars, mount
#        paths) and the runtime.
#
#   2. Base userland sufficient to compose the overlay in any of the
#        three supported modes (see pkg/sandbox/k8s.OverlayMode):
#          - tar, zstd — resume-tar extraction from the uppers PVC
#            (§5.14).
#          - util-linux / mount — kernel overlayfs (mount -t overlay)
#            for nodes that can honour it (Sysbox, hostUsers: false,
#            or privileged pods on a real kernel).
#          - fuse-overlayfs — userspace overlay composer for nodes
#            where kernel overlayfs is unavailable (nested LXC hosts,
#            restricted RuntimeClass). Requires /dev/fuse, which is
#            obtained either via a cluster device plugin
#            (smarter-device-manager advertising smarter-devices/fuse)
#            or via privileged: true + an in-entrypoint mknod(1).
#          - busybox-static — minimal fallback tooling.
#
#   3. A tiny /sandbox skeleton where the overlay will be composed:
#        /sandbox/rootfs  — overlay mount point (created by the entrypoint)
#        /var/astonish/   — emptyDir mount roots (upper, work)
#        /mnt/astonish-layers, /mnt/astonish-uppers — PVC mount roots
#
# What this image INTENTIONALLY does NOT contain:
#
#   - The Astonish daemon binary. The daemon connects to the pod from
#     outside via the exec subresource; the entrypoint merely sleeps
#     after composing the overlay and awaits exec sessions
#     (§5.3 step 3). We keep this minimal so base-image updates don't
#     require rebuilding with every Astonish release.
#
#   - Any language runtimes, shells beyond /bin/sh, or user tooling.
#     Those belong in layers composed ON TOP of this base (the @base
#     layer plus org-wide and template-specific layers on CephFS).
#
# Build:
#   docker build -f docker/sandbox-base/Dockerfile -t ghcr.io/sap/astonish-sandbox-base:latest .
#
# Publish:
#   The image tag MUST match SandboxKubernetesConfig.SandboxImage's
#   default (pkg/config/app_config.go) and the chart's sandbox.image.tag
#   (deploy/helm/astonish/values.yaml). If that default changes, bump
#   all three in lockstep.
#
# Reference: docs/architecture/sandbox-backends.md §§5.3, 5.14.

# ---------------------------------------------------------------------------
# Stage 1 — generate the entrypoint script with the Go helper AND build the
# astonish daemon binary itself.
#
# Two artefacts, one toolchain stage, because:
#   - We use the same Go toolchain version as the main Dockerfile so the
#     generated script is bit-identical across builds (EntrypointScript is
#     deterministic, but keeping toolchains aligned avoids future footguns
#     like go fmt / string-builder behavioural drift).
#   - Carrying astonish into the base image (Phase E §11) gives tool calls
#     a binary to exec inside the sandbox without requiring operators to
#     bake it into their @base layer. At overlay-compose time the
#     entrypoint bind-mounts this binary into the overlay so BOTH the
#     PID-1 chroot handoff AND Backend.Exec tool calls resolve to the
#     same trusted build.
#
# IMPORTANT: run the Go toolchain on $BUILDPLATFORM (the native host), then
# cross-compile for $TARGETOS/$TARGETARCH. Running `go mod download` under
# QEMU user-mode emulation (buildx linux/amd64 on Apple Silicon) panics in
# Go 1.26 with "growslice: len out of range" / segfaults — same fix used by
# docker/sandbox-openshell and docker/astonish (host-side cross-compile).
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS entrypoint-builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

# Copy just the module manifests first so the dependency cache is
# reused across source edits.
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire source tree. The entrypoint-script generator needs
# pkg/sandbox/k8s + pkg/sandbox + pkg; the astonish binary additionally
# needs main.go, cmd/astonish/*, and every internal package it imports
# transitively. Copying the whole tree is simpler than maintaining two
# include lists and only costs us cache granularity — the Go build
# below would invalidate on any source change regardless.
COPY . .

RUN CGO_ENABLED=0 go run ./cmd/astonish-sandbox-entrypoint-script \
      > /tmp/astonish-sandbox-entrypoint && \
    chmod +x /tmp/astonish-sandbox-entrypoint && \
    # Sanity check — the file is non-empty and starts with the
    # POSIX shebang; a broken generator is a build-time failure, not a
    # runtime surprise in the cluster.
    head -1 /tmp/astonish-sandbox-entrypoint | grep -q '^#!/bin/sh'

# Build the astonish binary for the *target* platform. CGO is disabled so
# the resulting binary is statically linked and runs in the minimal base
# image without libc surprises. The -s -w strip flags mirror the main
# Dockerfile; version is left as "sandbox-base" because this is an
# infrastructure image, not a shippable daemon build.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -ldflags="-s -w -X github.com/SAP/astonish/cmd/astonish.Version=sandbox-base" \
      -o /tmp/astonish \
      .

# ---------------------------------------------------------------------------
# Stage 2 — native tree-sitter shared library.
# ---------------------------------------------------------------------------
FROM --platform=$TARGETPLATFORM debian:bookworm-slim AS treesitter-builder

WORKDIR /src

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        gcc \
        libc6-dev \
        make \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY pkg/codeintel/native ./pkg/codeintel/native
RUN make -C pkg/codeintel/native OUT=/tmp/libastonish-treesitter.so

# ---------------------------------------------------------------------------
# Stage 3 — final runtime image.
#
# Debian slim is chosen over Alpine because fuse-overlayfs binds more
# predictably against glibc in the wild, and the resume path uses zstd
# which is packaged trivially on Debian. The image stays under ~60 MiB
# after apt-get clean.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim

# Labels make kubectl describe / docker inspect self-explanatory and
# give image-signing tools something stable to chew on.
LABEL org.opencontainers.image.title="astonish-sandbox-base" \
      org.opencontainers.image.description="Minimal base image for Astonish K8s sandbox pods" \
      org.opencontainers.image.source="https://github.com/SAP/astonish" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV DEBIAN_FRONTEND=noninteractive

# Userland required by the entrypoint script across all OverlayModes:
#   - ca-certificates: workloads often fetch HTTPS resources.
#   - tar + zstd: resume-tar extraction in §5.14.
#   - coreutils: mkdir, echo, tee, etc.
#   - busybox-static: fallback tooling when a layer ships a minimal
#       rootfs without GNU utilities.
#   - mount + util-linux: kernel `mount -t overlay` AND the bind-mount
#       step for the host astonish binary. mountpoint(1) lives here.
#   - fuse-overlayfs + fuse3: userspace overlay strategy. Works on any
#       kernel with FUSE support; no CAP_SYS_ADMIN required for the
#       overlay mount itself — only /dev/fuse access.
# We remove apt lists after install to keep the image small; users who
# need additional packages should compose them into higher layers.
#
# Do NOT install ripgrep (or other agent tool userland) here. Tools run
# after chroot into the composed overlay, so packages on this pod image
# are invisible to grep_search/find_files. Ripgrep is installed into
# @base overlay layers via CoreToolInstallCommands() / baseconfig.Render
# (and apt-installed in docker/sandbox-openshell for the OpenShell image).
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tar \
        zstd \
        coreutils \
        busybox-static \
        mount \
        util-linux \
        fuse-overlayfs \
        fuse3 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /usr/lib/astonish
COPY --from=treesitter-builder /tmp/libastonish-treesitter.so /usr/lib/astonish/libastonish-treesitter.so

# Compatibility guard for sandbox astonish-host binaries whose ELF
# interpreter is /lib/ld-musl-x86_64.so.1. The intended build is static
# (CGO_ENABLED=0), but some Go/toolchain combinations still produce a
# dynamic musl-linked binary when built from the Alpine toolchain stage.
# The entrypoint bind-mounts astonish-host into the chroot at
# /usr/local/bin/astonish; without this loader path, backend Exec sees
# exit=127 and no node handshake even though the file exists.
RUN ln -sf x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib/ld-musl-x86_64.so.1

# Disable apt's privilege-drop sandbox. In the Astonish sandbox chroot,
# everything runs as root under squash_to_root semantics, so the _apt
# user cannot access partial-download directories. This suppresses the
# misleading "Download is performed unsandboxed as root" warning that
# would otherwise confuse users running `apt install` in the editor.
RUN echo 'APT::Sandbox::User "root";' > /etc/apt/apt.conf.d/00no-sandbox

# Directory skeleton the entrypoint and pod manifest expect. Creating
# them here (instead of relying on the kubelet to mkdir emptyDirs)
# makes the contract explicit and surfaces misconfigurations at image-
# build time rather than at first pod start.
#
# NOTE: /var/astonish/overlay is a single emptyDir mount point.
# The entrypoint creates upper/ and work/ subdirectories inside it at
# runtime. They MUST share a single mount to avoid cross-device
# renameat failures in fuse-overlayfs (see session.go comments).
RUN mkdir -p \
      /sandbox/rootfs \
      /var/astonish/overlay \
      /mnt/astonish-layers \
      /mnt/astonish-uppers \
    && chmod 0755 /sandbox /sandbox/rootfs /var/astonish

# Pull the generated entrypoint from the builder stage.
COPY --from=entrypoint-builder \
     /tmp/astonish-sandbox-entrypoint \
     /usr/local/bin/astonish-sandbox-entrypoint

# Pull the astonish binary from the builder stage and install it at a
# NON-standard path. The entrypoint bind-mounts this file into the
# overlay as /usr/local/bin/astonish once the overlay is composed (see
# pkg/sandbox/k8s.EntrypointScript). Using a distinct host path
# ("-host") keeps the base-image namespace distinct from the overlay
# namespace so tool Exec callers never accidentally run the binary
# outside the chroot — there is nothing at /usr/local/bin/astonish in
# the base namespace until the entrypoint has set up the bind-mount.
#
# Tool-call execution flow:
#
#   Backend.Exec(sessionID, Command: ["astonish", "node"])
#     → kubectl exec lands in pod base namespace
#     → /usr/local/bin/astonish resolves via the overlay bind-mount
#       (once PID 1 has composed the overlay)
#     → the wrapper nature of the bind-mount means the chroot is
#       implicit: we are ALREADY in the user-namespace rooted at the
#       overlay once the bind-mount replaces the base-image binary.
#
# Wait — clarification on semantics: kubectl exec does NOT inherit PID 1's
# chroot. PID 1's `exec chroot $MOUNT_POINT ...` only affects PID 1 and
# its descendants; new execs start in the container's original root.
# For Backend.Exec to see the overlay, we need a second chroot. That is
# why /usr/local/bin/astonish in the base namespace is a WRAPPER (below),
# not the real binary. The wrapper does its own chroot before handoff.
COPY --from=entrypoint-builder \
     /tmp/astonish \
     /usr/local/bin/astonish-host

# Wrapper that routes Backend.Exec tool calls into the composed overlay.
#
# Why this exists:
#   The daemon invokes tool RPCs by running `astonish node` inside the
#   sandbox via Backend.Exec. kubectl's exec subresource places the new
#   process in the pod's base namespace, NOT inside PID 1's chroot.
#   Tools (read_file /tmp/foo, write_file, shell_command) must see the
#   overlay rootfs, not the bare base image. The wrapper pivots into
#   the overlay mount via chroot before handing off to the real binary.
#
# Why not bind-mount astonish-host into the overlay at
# /usr/local/bin/astonish and skip the wrapper?
#   That covers PID 1 handoff fine, but Backend.Exec commands landing
#   in the base namespace still see /usr/local/bin/astonish in the base
#   (not overlay) — so they would exec without a chroot and hit the
#   wrong rootfs. The wrapper is the minimum that makes both paths
#   correct without special-casing per invocation source.
#
# The wrapper is deliberately plain POSIX (no bash, no arrays) so
# busybox-static covers it even if /bin/sh is unset.
RUN printf '%s\n' \
      '#!/bin/sh' \
      '# astonish (sandbox-base wrapper) — chroot into the composed overlay' \
      '# before invoking the real astonish binary at /usr/local/bin/astonish-host.' \
      '#' \
      '# DO NOT EDIT. Generated by docker/sandbox-base/Dockerfile during image build.' \
      '# See docs/architecture/sandbox-backends.md §11 Phase E for the design note.' \
      'set -e' \
      'MOUNT_POINT=/sandbox/rootfs' \
      'BIN=/usr/local/bin/astonish-host' \
      'if [ ! -d "$MOUNT_POINT" ] || [ ! -x "$MOUNT_POINT/usr/local/bin/astonish" ]; then' \
      '  # Overlay not composed yet — fall back to the host binary directly.' \
      '  # This keeps the image usable for one-shot diagnostics (`docker run ... astonish --help`)' \
      '  # before PID 1 has set up the overlay.' \
      '  exec "$BIN" "$@"' \
      'fi' \
      'exec chroot "$MOUNT_POINT" /usr/local/bin/astonish "$@"' \
      > /usr/local/bin/astonish \
    && chmod +x /usr/local/bin/astonish

# Interactive shell wrapper for team-template editor sessions.
#
# Why this exists:
#   The team-template editor opens an interactive PTY inside the sandbox
#   pod. kubectl exec lands in the pod's base namespace (NOT inside PID
#   1's chroot). The user expects to see and modify the composed overlay
#   filesystem. Without a chroot wrapper, writes (apt install, touch,
#   mkdir) go to the kubelet container's ephemeral layer and never reach
#   /var/astonish/overlay/upper — causing the layer capture to produce an empty
#   tarball.
#
#   astonish-shell chroots into the composed overlay and launches an
#   interactive login shell there, mirroring the pattern of the
#   astonish wrapper but for interactive sessions rather than tool RPCs.
#
# Usage from Backend.ExecInteractive:
#   Command: ["/usr/local/bin/astonish-shell"]
#   — or with explicit shell: ["/usr/local/bin/astonish-shell", "/bin/bash", "-l"]
RUN printf '%s\n' \
      '#!/bin/sh' \
      '# astonish-shell — chroot into the overlay and start an interactive shell.' \
      '#' \
      '# DO NOT EDIT. Generated by docker/sandbox-base/Dockerfile during image build.' \
      'set -e' \
      'MOUNT_POINT=/sandbox/rootfs' \
      'if [ ! -d "$MOUNT_POINT/bin" ]; then' \
      '  # Overlay not composed yet — fall back to a bare shell.' \
      '  exec /bin/bash -l' \
      'fi' \
      '# If arguments are provided, use them as the command inside the chroot.' \
      '# Otherwise default to bash -l for an interactive login session.' \
      'if [ $# -gt 0 ]; then' \
      '  exec chroot "$MOUNT_POINT" "$@"' \
      'fi' \
      'exec chroot "$MOUNT_POINT" /bin/bash -l' \
      > /usr/local/bin/astonish-shell \
    && chmod +x /usr/local/bin/astonish-shell

# Default to the entrypoint; the pod manifest can override via command/
# args if a particular test needs to skip overlay composition. In
# normal operation, the kubelet starts PID 1 here which sets up the
# overlay then execs into the workload via chroot (§5.3 step 3).
ENTRYPOINT ["/usr/local/bin/astonish-sandbox-entrypoint"]
