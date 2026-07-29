# Testing the Docker+Incus Sandbox Locally

This guide covers how to test the cross-platform sandbox support
(Phase 5) without going through the full release cycle.

## Prerequisites

- Docker Desktop (macOS/Windows) or Docker Engine (Linux)
- Go 1.24+ (for cross-compiling the Linux binary)
- The astonish source code checked out

## 1. Testing on Linux (Regression Check)

On Linux with native Incus, the refactoring should be a clean no-op.
The `remote_ops.go` abstraction falls through to direct `os.*` and
`exec.Command()` calls when `activePlatform == PlatformLinuxNative`.

```bash
# Build
make build

# Verify sandbox still works exactly as before
./astonish sandbox status
./astonish sandbox init          # if not already initialized
./astonish sandbox template list

# Start a chat session and verify container creation (~200ms)
./astonish

# In the chat, run a tool that triggers container creation
# (e.g., ask it to list files, create a file, etc.)

# Check containers were created
./astonish sandbox list

# Refresh templates (verifies binary push still works)
./astonish sandbox refresh
```

If all of the above works identically to before, the abstraction
layer is transparent on Linux.

## 2. Testing Docker+Incus on macOS (or Linux with Docker)

You can test the Docker+Incus path on any machine with Docker,
including Linux (useful for development before testing on a real Mac).

### Step 1–2: Build Linux binary + load Incus image (one target)

```bash
# Preferred: native-arch binary, buildx --load into local Docker,
# tags :dev and :latest (runtime looks for :latest when host version is "dev").
make docker-incus
```

Do **not** rely on a plain `docker build` with the `docker-container` buildx
driver — without `--load` the result stays only in the build cache and never
appears in `docker images`. On Apple Silicon the Dockerfile COPYs
`astonish-linux-arm64` (`TARGETARCH`); building only amd64 leaves a stale arm64
binary in the image.

After a tool-code change, also recreate the running runtime container and
refresh templates (existing session containers keep the old binary):

```bash
docker rm -f astonish-incus
./astonish sandbox refresh --force
# then start a NEW chat session
```

Manual equivalent of `make docker-incus` (Apple Silicon):

```bash
make build-linux-arm64
docker buildx build --platform linux/arm64 \
  -f docker/incus/Dockerfile \
  -t ghcr.io/sap/astonish-incus:dev \
  -t ghcr.io/sap/astonish-incus:latest \
  --load .
```

This builds the image with:
- Ubuntu 24.04 + Incus daemon
- The cross-compiled `astonish-linux-{arch}` binary at `/usr/local/bin/astonish`
- The entrypoint script that initializes Incus on first boot

### Step 3: Build astonish for your host platform

```bash
# On macOS:
go build -o astonish .

# On Linux (if testing the Docker path instead of native):
# Force the Docker path by temporarily faking non-Linux detection.
# See "Testing Docker path on Linux" section below.
go build -o astonish .
```

### Step 4: Run sandbox init

```bash
# This will:
# 1. Detect Docker (PlatformDockerIncus)
# 2. Use the locally-built ghcr.io/sap/astonish-incus:latest image (no pull needed)
# 3. Create the astonish-incus container with privileged mode
# 4. Wait for Incus API on localhost:8443
# 5. Initialize the @base template inside Incus
./astonish sandbox init
```

Expected output:
```
Setting up Docker+Incus runtime...
[sandbox] Pulling Docker image ghcr.io/sap/astonish-incus:latest...
[sandbox] Creating Docker container astonish-incus...
[sandbox] Docker container astonish-incus created, waiting for Incus API...
Docker+Incus runtime ready.
? Optional tools to install: (none selected)
Creating base template from ubuntu/24.04...
...
Base template initialized successfully.
```

### Step 5: Verify status

```bash
./astonish sandbox status
```

Expected output:
```
Platform:         Docker + Incus
Docker container: running
Docker version:   dev
Incus connected:  yes
Incus version:    6.x
Storage backend:  dir
Session creation: instant (overlayfs)
Templates:        1
Session containers: 0
```

### Step 6: Test session creation

```bash
# Start a chat session — this triggers container creation
./astonish

# Or run in Studio mode
./astonish studio
```

When a tool executes, it should create a session container inside the
Docker VM's Incus instance. Verify with:

```bash
./astonish sandbox list
```

### Step 7: Test template shell

```bash
# Shell into the @base template (chains through docker exec)
./astonish sandbox template shell base
```

This should open a bash shell inside the template container. On
Docker+Incus, the command chain is:
`docker exec -it astonish-incus incus exec astn-tpl-base -- bash -l`

### Step 8: Clean up

```bash
# Stop the Docker container (Incus data is preserved in the volume)
docker stop astonish-incus

# To fully reset (destroys all Incus data):
docker rm astonish-incus
docker volume rm astonish-incus-data
```

## 3. Testing Docker Path on Linux

If you want to test the Docker+Incus code path on a Linux machine
(where `DetectPlatform()` normally returns `PlatformLinuxNative`),
you can temporarily override the detection.

**Option A: Remove Incus temporarily**

If Incus is not installed or the daemon is stopped, and Docker is
running, `DetectPlatformReason()` will fall back to checking Docker
on non-Linux systems. On Linux, it checks Incus first — so you'd
need to stop Incus:

```bash
sudo systemctl stop incus
# Now DetectPlatform() returns PlatformUnsupported on Linux
# (It doesn't check Docker on Linux — only on macOS/Windows)
```

This won't help because the Docker path is only for non-Linux.

**Option B: Test inside a Docker container on Linux**

Run the tests inside a Docker container that has Docker-in-Docker:

```bash
# This simulates a non-Linux environment with Docker available
docker run -it --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v $(pwd):/workspace \
  -w /workspace \
  golang:1.24 bash

# Inside the container, install Docker CLI
apt-get update && apt-get install -y docker.io

# Build and test
make build-linux
docker build -f docker/incus/Dockerfile -t ghcr.io/sap/astonish-incus:latest .
go build -o astonish .
./astonish sandbox status
```

**Option C: Unit test the remote_ops functions directly**

The `remote_ops.go` functions dispatch based on `activePlatform`.
You can test by setting it explicitly:

```go
sandbox.SetActivePlatform(sandbox.PlatformDockerIncus)
// Now all remote ops will use docker exec
```

## 4. Verifying the Docker Image Contents

To inspect what's inside the built image:

```bash
# Check the astonish binary is present and executable
docker run --rm ghcr.io/sap/astonish-incus:latest /usr/local/bin/astonish --version

# Check Incus is installed
docker run --rm ghcr.io/sap/astonish-incus:latest incus --version

# Check the entrypoint script
docker run --rm ghcr.io/sap/astonish-incus:latest cat /usr/local/bin/entrypoint-incus.sh
```

## 5. Troubleshooting

### "Docker image not found" error

The image needs to exist locally. Build it with:
```bash
make build-linux
docker build -f docker/incus/Dockerfile -t ghcr.io/sap/astonish-incus:latest .
```

### "Incus API not reachable" timeout

Check the Docker container logs:
```bash
docker logs astonish-incus
```

Common causes:
- Incus daemon failed to start (check for port conflicts on 8443)
- First-time preseed initialization failed
- Container needs `--privileged` (should be set automatically)

### Overlay mount failures

On Docker+Incus, overlay mounts run inside the Docker container.
Debug with:
```bash
docker exec astonish-incus cat /proc/mounts | grep overlay
docker exec astonish-incus ls -la /var/lib/incus/disks/astonish-overlays/
```

### Session container has empty rootfs

The overlay lower layer (template snapshot) may be missing:
```bash
docker exec astonish-incus ls /var/lib/incus/storage-pools/default/containers-snapshots/
```

### Resetting everything

```bash
docker stop astonish-incus
docker rm astonish-incus
docker volume rm astonish-incus-data
docker rmi ghcr.io/sap/astonish-incus:latest
```

Then re-run `./astonish sandbox init` to start fresh.
