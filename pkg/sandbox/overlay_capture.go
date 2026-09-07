package sandbox

import "fmt"

// OverlayCaptureScript is the POSIX tar-to-layer pipeline used by Docker and
// Kubernetes BuildTemplate / SaveSessionAsTemplate.
//
// It streams the overlay upper through sha256sum into a staging directory on
// layersDir. A named fifo replaces bash process substitution so Debian's
// /bin/sh (dash) can hash and extract in one pass.
//
// The fifo lives on /dev/shm (tmpfs), not on layersDir: Docker Desktop bind
// mounts of the Mac host (virtiofs/osxfs) reject mkfifo with EPERM. Do not
// stage a full tar on /tmp either — that second copy ENOSPC's the Docker VM
// after a base-layer install.
func OverlayCaptureScript(layersDir, upperDir, builderID string) string {
	return fmt.Sprintf(`set -e
STAGING=%q
LAYERS_DIR=%q
UPPER=%q
PIPE_DIR=/dev/shm
if [ ! -d "$PIPE_DIR" ] || [ ! -w "$PIPE_DIR" ]; then
  PIPE_DIR=/tmp
fi
FIFO="$PIPE_DIR/astn-capture-$$.fifo"
HASH_OUT="$PIPE_DIR/astn-capture-$$.sha"
HASHPID=""
cleanup() {
  if [ -n "$HASHPID" ]; then kill "$HASHPID" 2>/dev/null || true; fi
  rm -f "$FIFO" "$HASH_OUT"
  rm -rf "$STAGING"
}
trap cleanup EXIT
mkdir -p "$STAGING/rootfs"
need=$(du -sm "$UPPER" 2>/dev/null | awk '{print $1}')
avail=$(df -Pm "$LAYERS_DIR" 2>/dev/null | awk 'NR==2 { print $4 }')
echo "capture: upper=${need}MB layers_free=${avail}MB"
if [ -n "$need" ] && [ -n "$avail" ] && [ "$avail" -lt "$need" ]; then
  echo "E: layers volume has ${avail}MB free; overlay upper is ${need}MB. Increase Docker Desktop disk or run: docker volume prune -f && docker system prune" >&2
  exit 1
fi
mkfifo "$FIFO"
(sha256sum < "$FIFO" > "$HASH_OUT") &
HASHPID=$!
tar --numeric-owner --xattrs --acls --sort=name --mtime=@0 \
    -C "$UPPER" -cf - . \
  | tee "$FIFO" \
  | tar --numeric-owner --xattrs --acls -C "$STAGING/rootfs" -xf -
wait "$HASHPID"
HASHPID=""
SHA=$(awk '{print $1}' "$HASH_OUT")
rm -f "$FIFO" "$HASH_OUT"
if [ ${#SHA} -ne 64 ]; then
  echo "E: capture sha256 is not 64 hex chars: $SHA" >&2
  exit 1
fi
if [ -d "$LAYERS_DIR/$SHA" ]; then
  rm -rf "$STAGING"
else
  mv "$STAGING" "$LAYERS_DIR/$SHA"
fi
trap - EXIT
SIZE=$(du -sb "$LAYERS_DIR/$SHA/rootfs" | awk '{print $1}')
echo "SHA=$SHA"
echo "SIZE=$SIZE"
`, layersDir+"/__staging-"+builderID, layersDir, upperDir)
}
