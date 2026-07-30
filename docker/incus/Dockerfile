# docker/incus/Dockerfile — Incus runtime container for Astonish sandbox on macOS/Windows.
#
# This image runs the Incus daemon inside a Docker container, providing
# Linux container isolation on non-Linux hosts. Astonish on the host
# connects to this container's Incus API via TCP (localhost:8443).
#
# Build (single-arch, local dev — preferred via Makefile):
#   make docker-incus
#   # Builds astonish-linux-$(arch), docker buildx --load, tags :dev and :latest.
#
# Manual equivalent (Apple Silicon example):
#   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o astonish-linux-arm64 .
#   docker buildx build --platform linux/arm64 -f docker/incus/Dockerfile \
#     -t ghcr.io/sap/astonish-incus:dev -t ghcr.io/sap/astonish-incus:latest --load .
#
# Build (multi-arch, CI):
#   # Pre-build both binaries, then:
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     -f docker/incus/Dockerfile -t ghcr.io/sap/astonish-incus:v1.2.3 --push .
#
# Runtime (EnsureIncusDockerContainer) selects the tag from host version:
#   version "dev" → ghcr.io/sap/astonish-incus:latest
#   version "vX.Y.Z" → ghcr.io/sap/astonish-incus:vX.Y.Z
# A plain :dev tag is NOT selected unless you retag or set VERSION accordingly.
#
# The image is published to ghcr.io by the docker-images GitHub Action.
# Local `make docker-incus` is required for macOS/Windows sandbox tool updates.

# ---------------------------------------------------------------------------
# Native tree-sitter shared library used by pkg/codeintel. Docker-backed Incus
# copies this into template containers alongside the astonish binary.
# ---------------------------------------------------------------------------
FROM ubuntu:24.04 AS treesitter-builder

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /src

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        build-essential \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY pkg/codeintel/native ./pkg/codeintel/native
RUN make -C pkg/codeintel/native OUT=/tmp/libastonish-treesitter.so

FROM ubuntu:24.04

# Avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Add Zabbly repository for Incus (Ubuntu 24.04 base image only ships
# the 'main' component; Incus is in 'universe' which is not enabled by
# default in Docker images. Zabbly provides the official Incus packages
# with up-to-date stable releases.)
#
# The Zabbly incus-base package depends on systemd and apparmor which
# need to be present before installing. We also install dnsmasq-base
# and iptables which Incus needs for container networking.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
        systemd \
        apparmor \
        dnsmasq-base \
        iptables \
    && mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc \
    && printf 'Enabled: yes\nTypes: deb\nURIs: https://pkgs.zabbly.com/incus/stable\nSuites: noble\nComponents: main\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/zabbly.asc\n' \
        "$(dpkg --print-architecture)" \
        > /etc/apt/sources.list.d/zabbly-incus-stable.sources \
    && apt-get update \
    && apt-get install -y incus \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Verify incusd was installed correctly
RUN test -x /opt/incus/bin/incusd || { echo "ERROR: incusd not found"; exit 1; }

# Copy the pre-built Linux astonish binary for the target architecture.
# TARGETARCH is set automatically by docker buildx (amd64 or arm64).
# Do not give TARGETARCH a default — BuildKit ignores platform injection when
# a default is set, which ships the wrong-arch binary in multi-arch images.
ARG TARGETARCH
COPY astonish-linux-${TARGETARCH} /usr/local/bin/astonish
RUN chmod +x /usr/local/bin/astonish

RUN mkdir -p /usr/lib/astonish
COPY --from=treesitter-builder /tmp/libastonish-treesitter.so /usr/lib/astonish/libastonish-treesitter.so

# Copy the entrypoint script
COPY docker/incus/entrypoint-incus.sh /usr/local/bin/entrypoint-incus.sh
RUN chmod +x /usr/local/bin/entrypoint-incus.sh

# Incus API port (HTTPS with self-signed cert)
EXPOSE 8443

# Persistent storage for Incus data (containers, images, storage pools, certs)
VOLUME /var/lib/incus

ENTRYPOINT ["/usr/local/bin/entrypoint-incus.sh"]
