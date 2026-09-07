package sandbox

import "fmt"

// OverlayCaptureOpts is the in-container tar-to-layer pipeline used by Docker
// and Kubernetes BuildTemplate / SaveSessionAsTemplate.
type OverlayCaptureOpts struct {
	LayersDir string
	UpperDir  string
	BuilderID string
	// ExtractXattrs is true on Linux PVCs (CephFS). False on Docker Desktop /
	// Colima virtiofs bind mounts, which reject xattrs and acls (EPERM) and
	// abort the extract after a full package install.
	ExtractXattrs bool
}

// OverlayCaptureScript streams the overlay upper through sha256sum into a
// staging directory on layersDir.
//
// It is bash with pipefail (no fifo, no /tmp tar):
//   - A fifo on the layers volume fails on virtiofs (EPERM).
//   - A fifo+tee pipeline can deadlock and OOM a 8GiB Colima VM.
//   - A full tar on /tmp ENOSPC's the Docker VM after CloakBrowser+apt.
//
// Hash and extract are two sequential tar streams of the overlay upper
// (Linux volume, cheap). The extract lands on layersDir.
func OverlayCaptureScript(opts OverlayCaptureOpts) string {
	extractFlags := "--numeric-owner"
	sizeLine := `echo "SIZE=0"`
	if opts.ExtractXattrs {
		extractFlags = "--numeric-owner --xattrs --acls"
		sizeLine = `SIZE=$(du -sb "$LAYERS_DIR/$SHA/rootfs" | awk '{print $1}')
echo "SIZE=$SIZE"`
	}
	return fmt.Sprintf(`set -euo pipefail
STAGING=%q
LAYERS_DIR=%q
UPPER=%q
cleanup() { rm -rf "$STAGING"; }
trap cleanup EXIT
mkdir -p "$STAGING/rootfs"
echo "capture: hashing overlay" >&2
SHA=$(tar --numeric-owner --xattrs --acls --sort=name --mtime=@0 -C "$UPPER" -cf - . | sha256sum | awk '{print $1}')
echo "capture: sha=$SHA" >&2
if [ ${#SHA} -ne 64 ]; then
  echo "E: capture sha256 is not 64 hex chars: $SHA" >&2
  exit 1
fi
if [ -d "$LAYERS_DIR/$SHA" ]; then
  rm -rf "$STAGING"
else
  echo "capture: extracting overlay to layers" >&2
  tar --numeric-owner --xattrs --acls --sort=name --mtime=@0 -C "$UPPER" -cf - . \
    | tar %s -C "$STAGING/rootfs" -xf -
  mv "$STAGING" "$LAYERS_DIR/$SHA"
fi
trap - EXIT
echo "SHA=$SHA"
%s
`, opts.LayersDir+"/__staging-"+opts.BuilderID, opts.LayersDir, opts.UpperDir, extractFlags, sizeLine)
}
