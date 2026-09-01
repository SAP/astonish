# Astonish Docker image (API / Studio daemon)
#
# Go binaries are cross-compiled on the host (or CI runner) and copied in.
# This avoids Go 1.26 segfaults when running `go mod download` under QEMU
# user-mode emulation on multi-arch buildx (arm64 on amd64 runners and vice
# versa).
#
# Base must be glibc (Debian), not Alpine/musl: pkg/codeintel uses purego.Dlopen,
# which produces a dynamically linked binary needing /lib64/ld-linux-*.so.2.
#
# Runtime tooling for host-side MCP (npx / uvx when no sandbox is wired):
#   - Node.js + npx: copied from the official multi-arch Node image (prebuilt
#     layers — no apt install of nodejs/npm under QEMU on arm64 CI builds).
#   - uv + uvx: copied from the official astral-sh/uv multi-arch image.
#   - git + ca-certificates: minimal apt (small under QEMU).
#
# Layer order (stable → volatile) so code-only rebuilds cache deps:
#   1. minimal apt
#   2. node + uv binaries
#   3. COPY astonish-linux-${TARGETARCH}
#
# Pins — bump intentionally; changing them invalidates the deps layers.
#   NODE_IMAGE  node:22-bookworm-slim
#   UV_VERSION  0.12.1  (ghcr.io/astral-sh/uv tag)
#
# Build (multi-arch, from Makefile / CI):
#   make push-dev   # cross-compiles both arch binaries, then buildx pushes
#
# Build (single-arch, local dev):
#   make push-dev-fast

# Global build args used by FROM lines (must appear before the first FROM).
# Buildx multi-arch builds select the matching platform variant of each image
# automatically (no need for explicit --platform=$TARGETPLATFORM on FROM).
ARG NODE_IMAGE=node:22-bookworm-slim
ARG UV_VERSION=0.12.1

# Prebuilt Node toolchain (multi-arch official image).
FROM ${NODE_IMAGE} AS node

# Prebuilt uv/uvx binaries (multi-arch official image).
FROM ghcr.io/astral-sh/uv:${UV_VERSION} AS uv

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# Minimal system packages only — avoid apt nodejs/npm/python3-pip (slow under
# QEMU on GitHub Actions multi-arch arm64 and inflate the image).
# Chromium shared-library deps are needed for headless Chrome (rod) used by
# slide thumbnail generation and PDF export. Without them Chrome fails with
# "error while loading shared libraries: libgobject-2.0.so.0".
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        # --- Chromium headless runtime dependencies ---
        libglib2.0-0 \
        libnss3 \
        libnspr4 \
        libdbus-1-3 \
        libatk1.0-0 \
        libatk-bridge2.0-0 \
        libcups2 \
        libdrm2 \
        libxkbcommon0 \
        libatspi2.0-0 \
        libxcomposite1 \
        libxdamage1 \
        libxfixes3 \
        libxrandr2 \
        libgbm1 \
        libpango-1.0-0 \
        libcairo2 \
        libasound2 \
        fonts-liberation \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Official Node layout: binaries and npm/npx under /usr/local.
COPY --from=node /usr/local /usr/local

# Official uv distroless image ships /uv and /uvx at the image root.
COPY --from=uv /uv /uvx /usr/local/bin/

# Fail the build early if a platform mismatch shipped the wrong arch binary.
RUN node --version && npx --version && uv --version && uvx --version && git --version

WORKDIR /app

# Install the slide PPTX workers and their production dependencies.
COPY web/package.json web/package-lock.json /app/web/
RUN cd /app/web && npm ci --omit=dev --ignore-scripts && npm cache clean --force
COPY pkg/docs/slides/pptxworker/worker.mjs pkg/docs/slides/pptxworker/import_worker.mjs /app/pkg/docs/slides/pptxworker/
ENV ASTONISH_ROOT=/app

# Copy the pre-built Linux astonish binary for the target architecture.
# TARGETARCH is set automatically by docker buildx (amd64 or arm64).
# Do not give TARGETARCH a default — BuildKit overrides are ignored when a
# default is set, which previously shipped an amd64 binary in the arm64 image.
ARG TARGETARCH
COPY astonish-linux-${TARGETARCH} /usr/local/bin/astonish
RUN chmod +x /usr/local/bin/astonish

EXPOSE 9393

ENTRYPOINT ["astonish"]
CMD ["daemon", "run"]
