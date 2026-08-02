# Dockerfile — astonish-sandbox-base image.
#
# This is the container image every Astonish sandbox pod runs in when
# the K8s backend is active (SandboxConfig.Backend = "k8s"). It is NOT
# the application image (see docker/astonish/Dockerfile for that); it's a
# minimal userland that serves as PID 1 inside the sandbox pod.
#
# The image contains, and is RESTRICTED to:
#
#   1. /usr/local/bin/astonish-sandbox-entrypoint
#        The POSIX-shell script whose source of truth is
#        pkg/sandbox/k8s.EntrypointScript. Generated on the host via
#        `make sandbox-entrypoint` (cmd/astonish-sandbox-entrypoint-script)
#        so Docker does not recompile Go on every image rebuild.
#
#   2. Base userland sufficient to compose the overlay in any of the
#        three supported modes (see pkg/sandbox/k8s.OverlayMode):
#          - tar, zstd — resume-tar extraction from the uppers PVC
#          - util-linux / mount — kernel overlayfs
#          - fuse-overlayfs — userspace overlay composer
#          - busybox-static — minimal fallback tooling
#
#   3. A tiny /sandbox skeleton where the overlay will be composed:
#        /sandbox/rootfs  — overlay mount point (created by the entrypoint)
#        /var/astonish/   — emptyDir mount roots (upper, work)
#        /mnt/astonish-layers, /mnt/astonish-uppers — PVC mount roots
#
#   4. Host-cross-compiled astonish binary at /usr/local/bin/astonish-host
#        plus chroot wrappers at /usr/local/bin/astonish and
#        /usr/local/bin/astonish-shell (see below).
#
# What this image INTENTIONALLY does NOT contain:
#
#   - Language runtimes / agent tool userland (those live in overlay
#     layers on top of this base).
#   - A Docker-side Go toolchain. Binaries and the entrypoint script are
#     produced on the host (same pattern as docker/astonish) so BuildKit
#     can cache the heavy apt + treesitter layers across code changes.
#
# Layer order (stable → volatile) for cache hits on routine rebuilds:
#   1. apt packages          — changes only when this package list changes
#   2. treesitter .so        — changes only when pkg/codeintel/native changes
#   3. static setup/wrappers — changes only when this Dockerfile changes
#   4. entrypoint script     — changes when overlay entrypoint source changes
#   5. astonish-host binary  — changes on almost every agent code edit
#
# Prerequisites (from repo root):
#   make build-linux              # and/or build-linux-arm64 for multi-arch
#   make sandbox-entrypoint
#   # web/dist must exist for go:embed (make build-ui if missing)
#
# Build:
#   make push-sandbox-base-dev-fast   # native arch
#   make push-sandbox-base-dev        # multi-arch push
#
# Publish:
#   The image tag MUST match SandboxKubernetesConfig.SandboxImage's
#   default (pkg/config/app_config.go) and the chart's sandbox.image.tag
#   (deploy/helm/astonish/values.yaml). If that default changes, bump
#   all three in lockstep.
#
# Reference: docs/architecture/sandbox-backends.md §§5.3, 5.14.

# ---------------------------------------------------------------------------
# Stage 1 — native tree-sitter shared library (target-arch C build).
# Kept in Docker because the .so must match TARGETARCH; host cross-compile
# of C from macOS is unreliable. This stage only invalidates when
# pkg/codeintel/native changes.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS treesitter-builder

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
# Stage 2 — final runtime image.
#
# Debian slim is chosen over Alpine because fuse-overlayfs binds more
# predictably against glibc in the wild, and the resume path uses zstd
# which is packaged trivially on Debian. The image stays under ~60 MiB
# after apt-get clean.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="astonish-sandbox-base" \
      org.opencontainers.image.description="Minimal base image for Astonish K8s sandbox pods" \
      org.opencontainers.image.source="https://github.com/SAP/astonish" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV DEBIAN_FRONTEND=noninteractive

# Userland required by the entrypoint script across all OverlayModes.
# Do NOT install ripgrep (or other agent tool userland) here — tools run
# after chroot into the composed overlay (see CoreToolInstallCommands /
# baseconfig.Render).
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
# dynamic musl-linked binary. Without this loader path, backend Exec sees
# exit=127 and no node handshake even though the file exists.
RUN ln -sf x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib/ld-musl-x86_64.so.1

# Disable apt's privilege-drop sandbox. In the Astonish sandbox chroot,
# everything runs as root under squash_to_root semantics, so the _apt
# user cannot access partial-download directories.
RUN echo 'APT::Sandbox::User "root";' > /etc/apt/apt.conf.d/00no-sandbox

# Directory skeleton the entrypoint and pod manifest expect.
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

# ---------------------------------------------------------------------------
# Wrappers (stable Dockerfile text — keep ABOVE binary COPY so code edits
# do not rebuild these layers).
# ---------------------------------------------------------------------------
# Wrapper that routes Backend.Exec tool calls into the composed overlay.
# kubectl exec lands in the pod's base namespace, NOT inside PID 1's chroot;
# tools must see the overlay rootfs. See docs/architecture/sandbox-backends.md
# §11 Phase E.
RUN printf '%s\n' \
      '#!/bin/sh' \
      '# astonish (sandbox-base wrapper) — chroot into the composed overlay' \
      '# before invoking the real astonish binary at /usr/local/bin/astonish-host.' \
      '#' \
      '# DO NOT EDIT. Generated by docker/sandbox-base/Dockerfile during image build.' \
      'set -e' \
      'MOUNT_POINT=/sandbox/rootfs' \
      'BIN=/usr/local/bin/astonish-host' \
      'if [ ! -d "$MOUNT_POINT" ] || [ ! -x "$MOUNT_POINT/usr/local/bin/astonish" ]; then' \
      '  # Overlay not composed yet — fall back to the host binary directly.' \
      '  exec "$BIN" "$@"' \
      'fi' \
      'exec chroot "$MOUNT_POINT" /usr/local/bin/astonish "$@"' \
      > /usr/local/bin/astonish \
    && chmod +x /usr/local/bin/astonish

# Interactive shell wrapper for team-template editor sessions.
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
      'if [ $# -gt 0 ]; then' \
      '  exec chroot "$MOUNT_POINT" "$@"' \
      'fi' \
      'exec chroot "$MOUNT_POINT" /bin/bash -l' \
      > /usr/local/bin/astonish-shell \
    && chmod +x /usr/local/bin/astonish-shell

# ---------------------------------------------------------------------------
# Volatile host-built artifacts (invalidate only these on routine rebuilds).
# ---------------------------------------------------------------------------
# Do not give TARGETARCH a default — BuildKit ignores platform injection when
# a default is set, which ships the wrong-arch binary in multi-arch images.
ARG TARGETARCH

# Generated on the host: make sandbox-entrypoint
COPY astonish-sandbox-entrypoint /usr/local/bin/astonish-sandbox-entrypoint
RUN chmod +x /usr/local/bin/astonish-sandbox-entrypoint && \
    head -1 /usr/local/bin/astonish-sandbox-entrypoint | grep -q '^#!/bin/sh'

# Host-cross-compiled agent binary (make build-linux / build-linux-arm64).
# Installed at a NON-standard path; the entrypoint bind-mounts it into the
# overlay as /usr/local/bin/astonish once the overlay is composed.
COPY astonish-linux-${TARGETARCH} /usr/local/bin/astonish-host
RUN chmod +x /usr/local/bin/astonish-host

# Default to the entrypoint; the pod manifest can override via command/args.
ENTRYPOINT ["/usr/local/bin/astonish-sandbox-entrypoint"]
