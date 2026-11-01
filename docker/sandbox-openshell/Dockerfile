# Dockerfile — astonish-sandbox-openshell image.
#
# Fully-featured sandbox image for Astonish sessions running on the OpenShell
# backend. Includes all standard tools baked in:
#   - Core dev tools (docker.io, jq, unzip, socat, etc.)
#   - CloakBrowser (anti-detection stealth Chromium) + KasmVNC (VNC/web access)
#
# The native supervisor does not consume the overlay initialization marker.
# It uses its existing workspace PVC path, not the Docker/K8s overlay wrapper.
#
# The OpenShell gateway handles:
#   - Supervisor sideloading (via init-container or ImageVolume)
#   - Workspace PVC provisioning (via Agent Sandbox CRD)
#   - mTLS and policy enforcement
#   - Landlock/seccomp enforcement; agent process demoted to user "sandbox"
#
# Architecture: amd64 only.
#
# Go binary is cross-compiled on the host (no QEMU needed) and copied in.
# This avoids Go 1.26 segfaults when running `go mod download` under QEMU
# user-mode emulation on Apple Silicon Macs.
#
# Build (from repo root):
#   make push-sandbox-openshell-dev-fast
#
# Configure in OpenShell gateway values:
#   server:
#     sandboxImage: ghcr.io/sap/astonish-sandbox-openshell:latest

# ---------------------------------------------------------------------------
# Native tree-sitter shared library used by pkg/codeintel.
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

FROM ghcr.io/nvidia/openshell-community/sandboxes/base:latest

LABEL org.opencontainers.image.title="astonish-sandbox-openshell" \
      org.opencontainers.image.description="Astonish agent sandbox image for NVIDIA OpenShell (full toolset)" \
      org.opencontainers.image.source="https://github.com/SAP/astonish" \
      org.opencontainers.image.licenses="Apache-2.0"

# All install steps run as root. The OpenShell supervisor demotes the agent
# process to user "sandbox" at runtime via Landlock/seccomp.
USER root

# --- Astonish agent binary (cross-compiled on host, see Makefile build-linux) ---
# Do not give TARGETARCH a default — BuildKit ignores platform injection when
# a default is set, which ships the wrong-arch binary in multi-arch images.
ARG TARGETARCH
COPY astonish-linux-${TARGETARCH} /usr/local/bin/astonish
RUN chmod +x /usr/local/bin/astonish

RUN mkdir -p /usr/lib/astonish
COPY --from=treesitter-builder /tmp/libastonish-treesitter.so /usr/lib/astonish/libastonish-treesitter.so

# ===========================================================================
# System Packages (single layer)
# ===========================================================================
# The NVIDIA OpenShell base already provides:
#   Python 3.12 (system) + 3.14 (uv venv at /sandbox/.venv), Node.js 22,
#   git, gh, vim, nano, build-essential, curl, ca-certificates, gnupg,
#   openssl, uv, passwd
#
# We install only what's missing for full Astonish parity + CloakBrowser/KasmVNC.

# Ensure apt lists directory exists (NVIDIA base cleans it aggressively)
RUN mkdir -p /var/lib/apt/lists/partial

# Enable universe repo (Ubuntu). Safe no-op on non-Ubuntu images.
RUN grep -q 'Components:.*universe' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null \
    || sed -i '/^Components:/ s/$/ universe/' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null \
    || true

RUN apt-get update && apt-get install -y --no-install-recommends \
      # --- Core tools not in base ---
      wget jq unzip socat \
      docker.io \
      python3-pip python3-venv \
      # ripgrep — fast code search used by grep_search and find_files tools
      ripgrep \
      # --- CloakBrowser shared library dependencies ---
      # CloakBrowser is a standalone binary (not apt-installed), so all shared
      # libraries it links against must be explicitly listed.
      xvfb \
      ffmpeg \
      fonts-liberation fonts-noto-color-emoji \
      fonts-freefont-ttf fonts-wqy-zenhei fonts-ipafont-gothic \
      xdg-utils libnss3 libnss3-tools libatk-bridge2.0-0 \
      libx11-xcb1 libxcomposite1 libxrandr2 \
      libgbm1 libasound2t64 \
      libcups2 libpango-1.0-0 libcairo2 \
      libdbus-1-3 libdrm2 libexpat1 \
      libxdamage1 libxext6 libxfixes3 \
      libxkbcommon0 libatspi2.0-0 libvulkan1 \
      libxcb-dri3-0 libatk1.0-0 libgtk-3-0 \
      libgdk-pixbuf-2.0-0 libnspr4 \
      libjpeg-turbo8 libwebp-dev libssl3 \
      xfonts-base xfonts-75dpi xfonts-100dpi \
      x11-xserver-utils \
      x11-utils \
    && rm -rf /var/lib/apt/lists/*

# ===========================================================================
# Global npm packages (MCP servers pre-installed for faster startup)
# ===========================================================================
# The OpenShell gateway runs processes as the "sandbox" user who has HOME=/sandbox.
# npx can download packages at runtime (network presets allow registry.npmjs.org),
# but pre-installing common MCP servers globally avoids download latency on first use.
RUN npm install -g tavily-mcp@latest

# ===========================================================================
# CloakBrowser + KasmVNC
# ===========================================================================
# CloakBrowser is a patched Chromium with anti-detection (fingerprint spoofing).
# KasmVNC provides VNC/websocket access for visual browser handoff.
# Architecture: amd64 only.

# --- Browser user (KasmVNC cannot run as root) ---
RUN /usr/sbin/useradd -m -s /bin/bash browser

# --- KasmVNC v1.4.0 (amd64) ---
RUN wget -q -O /tmp/kasmvnc.deb \
      "https://github.com/kasmtech/KasmVNC/releases/download/v1.4.0/kasmvncserver_noble_1.4.0_amd64.deb" \
    && apt-get update \
    && apt-get install -y /tmp/kasmvnc.deb \
    && rm -f /tmp/kasmvnc.deb \
    && rm -rf /var/lib/apt/lists/*

# --- KasmVNC configuration ---
# Self-signed cert (required by KasmVNC even with SSL disabled)
RUN install -d -o browser -g browser -m 755 /home/browser/.vnc \
    && openssl req -x509 -newkey rsa:2048 \
         -keyout /home/browser/.vnc/snakeoil.key \
         -out /home/browser/.vnc/snakeoil.pem \
         -days 3650 -nodes -subj '/CN=localhost' 2>/dev/null \
    && chown browser:browser /home/browser/.vnc/snakeoil.key /home/browser/.vnc/snakeoil.pem \
    && chmod 600 /home/browser/.vnc/snakeoil.key

# KasmVNC config: SSL disabled (TLS terminated by exec tunnel), no prompts.
# Written to BOTH system-level (/etc/kasmvnc/) and user-level (~browser/.vnc/).
# The system-level config is the authoritative source — Xkasmvnc reads it
# regardless of HOME. The user-level copy is belt-and-suspenders for when
# the vncserver wrapper is used instead of calling Xkasmvnc directly.
RUN mkdir -p /etc/kasmvnc && printf '%s\n' \
      'network:' \
      '  protocol: http' \
      '  interface: 0.0.0.0' \
      '  use_ipv4: true' \
      '  use_ipv6: false' \
      '  ssl:' \
      '    require_ssl: false' \
      '    pem_certificate: /home/browser/.vnc/snakeoil.pem' \
      '    pem_key: /home/browser/.vnc/snakeoil.key' \
      'desktop:' \
      '  allow_resize: false' \
      '  resolution:' \
      '    width: 1920' \
      '    height: 1080' \
      'user_session:' \
      '  concurrent_connections_prompt: false' \
      'logging:' \
      '  log_writer_name: all' \
      '  log_dest: logfile' \
      '  level: 30' \
      'command_line:' \
      '  prompt: false' \
      | tee /etc/kasmvnc/kasmvnc.yaml > /home/browser/.vnc/kasmvnc.yaml \
    && chown browser:browser /home/browser/.vnc/kasmvnc.yaml

# Minimal xstartup — keeps X session alive for visual handoff
RUN printf '#!/bin/bash\nexec sleep infinity\n' > /home/browser/.vnc/xstartup \
    && chmod +x /home/browser/.vnc/xstartup \
    && chown browser:browser /home/browser/.vnc/xstartup

# Mark desktop env as selected (skips KasmVNC interactive prompt)
RUN touch /home/browser/.vnc/.de-was-selected \
    && chown browser:browser /home/browser/.vnc/.de-was-selected

# Pre-create KasmVNC user account (password overridden at handoff time)
RUN printf "kasmvnc\nkasmvnc\n" | /usr/bin/kasmvncpasswd -u user -w /home/browser/.kasmpasswd \
    && cp /home/browser/.kasmpasswd /root/.kasmpasswd \
    && chmod 644 /home/browser/.kasmpasswd

# --- CloakBrowser ---
# Install into system Python (3.12) so it's importable at runtime via:
#   python3 -c "from cloakbrowser.config import get_binary_path; print(get_binary_path())"
# Then download the stealth Chromium binary into /home/browser/.cloakbrowser/
# NOTE: The installer tries to launch chrome to verify, which segfaults under QEMU
# emulation (the binary is native amd64 but QEMU can't emulate it fully). We ignore
# the exit code and verify the binary exists instead.
RUN /usr/local/bin/uv pip install --system --break-system-packages --python /usr/bin/python3 cloakbrowser \
    && (HOME=/home/browser /usr/bin/python3 -m cloakbrowser install || true) \
    && test -x /home/browser/.cloakbrowser/chromium-*/chrome

# Fix ownership of browser home directory and ensure CloakBrowser + KasmVNC
# are accessible to the sandbox user (supervisor demotes exec to user "sandbox").
# The sandbox user needs to:
#   - Traverse /home/browser (useradd creates it with 0750 — must be 0755)
#   - Execute the CloakBrowser binary (under .cloakbrowser/)
#   - Read KasmVNC config, certs, and xstartup (under .vnc/)
#   - Write KasmVNC runtime files: pid, log, lock (under .vnc/ — must be 0777)
#   - Start vncserver with HOME=/home/browser
RUN chown -R browser:browser /home/browser \
    && chmod 755 /home/browser \
    && chmod -R 755 /home/browser/.cloakbrowser \
    && chmod -R 777 /home/browser/.vnc \
    && chmod 644 /home/browser/.vnc/snakeoil.key /home/browser/.vnc/snakeoil.pem \
    && touch /home/browser/.Xauthority && chmod 666 /home/browser/.Xauthority

# ===========================================================================
# X11 socket directory — required by libxtrans (KasmVNC)
# ===========================================================================
# libxtrans checks that /tmp/.X11-unix exists, is owned by root, and has the
# sticky bit set (1777). Since the Dockerfile runs as root, creating it here
# satisfies the check at runtime when Xkasmvnc runs as the sandbox user.
RUN mkdir -p /tmp/.X11-unix && chmod 1777 /tmp/.X11-unix

# ===========================================================================
# TLS CA bundle — writable for OpenShell proxy CA injection
# ===========================================================================
# The OpenShell supervisor runs a TLS-intercepting proxy and provides its CA
# at /etc/openshell-tls/openshell-ca.pem (runtime mount). Chromium reads the
# system CA bundle at /etc/ssl/certs/ca-certificates.crt. Making it writable
# allows the browser launch script to append the proxy CA at runtime so Chrome
# trusts the re-signed certificates.
RUN chmod 666 /etc/ssl/certs/ca-certificates.crt

# ===========================================================================
# Cleanup
# ===========================================================================
RUN apt-get clean && rm -rf /var/lib/apt/lists/*

# The workspace directory. OpenShell mounts a PVC here via the Agent Sandbox
# CRD's volumeClaimTemplates.
WORKDIR /sandbox
