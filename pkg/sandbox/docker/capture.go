package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// captureUpperToHost streams `tar -cf -` of the overlay upper out of the
// container and extracts it on the host LayersDir (APFS/ext4), hashing the
// tar stream in one pass.
//
// Doing this on the host avoids writing hundreds of thousands of files
// through Colima virtiofs from inside the VM, which is what stalled and
// then killed in-container capture.
func (db *DockerBackend) captureUpperToHost(ctx context.Context, cname string, copied *atomic.Int64) (*sandbox.TemplateArtifact, error) {
	builderID := fmt.Sprintf("%d", time.Now().UnixNano())
	staging := filepath.Join(db.cfg.LayersDir, "__staging-"+builderID)
	rootfs := filepath.Join(staging, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox/docker: mkdir capture staging: %w", err)
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath,
		"exec", cname,
		"tar", "--numeric-owner", "-C", mountUpper, "-cf", "-", ".")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox/docker: start capture tar: %w", err)
	}

	h := sha256.New()
	reader := io.TeeReader(&countingReader{r: stdout, n: copied}, h)
	if err := extractOverlayTar(reader, rootfs); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sandbox/docker: extract overlay tar: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return nil, captureLayerError(fmt.Errorf("%w\nstderr: %s", err, strings.TrimSpace(stderr.String())))
	}

	sha := hex.EncodeToString(h.Sum(nil))
	if len(sha) != 64 {
		return nil, fmt.Errorf("sandbox/docker: capture sha %q is not 64 hex chars", sha)
	}
	dest := db.layerDir(sha)
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		return &sandbox.TemplateArtifact{
			LayerID:    sha,
			SizeBytes:  dirSize(db.layerRootfs(sha)),
			CephFSPath: dest,
			CreatedAt:  time.Now().UTC(),
		}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, dest); err != nil {
		return nil, fmt.Errorf("sandbox/docker: install captured layer: %w", err)
	}
	keepStaging = true
	return &sandbox.TemplateArtifact{
		LayerID:    sha,
		SizeBytes:  dirSize(db.layerRootfs(sha)),
		CephFSPath: dest,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.n != nil {
		c.n.Add(int64(n))
	}
	return n, err
}

func extractOverlayTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	dest = filepath.Clean(dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := tarSafePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, tarFileMode(hdr)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, tarFileMode(hdr))
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(f, tr, hdr.Size)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, err := tarSafePath(dest, hdr.Linkname)
			if err != nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				// Fall back to a copy if the hard-link target is missing
				// or the host FS rejects links.
				if copyErr := copyFile(linkTarget, target); copyErr != nil {
					continue
				}
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Overlay whiteouts and device nodes cannot be created on a
			// macOS bind mount; skipping them is preferable to failing
			// the whole base-layer capture.
			continue
		default:
			continue
		}
	}
}

func tarFileMode(hdr *tar.Header) os.FileMode {
	mode := os.FileMode(hdr.Mode) & 0o777
	if mode == 0 {
		return 0o644
	}
	return mode
}

func tarSafePath(root, name string) (string, error) {
	clean := filepath.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
	rel := strings.TrimPrefix(clean, "/")
	full := filepath.Join(root, rel)
	rootClean := filepath.Clean(root)
	if full != rootClean && !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar path escapes destination: %q", name)
	}
	return full, nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
