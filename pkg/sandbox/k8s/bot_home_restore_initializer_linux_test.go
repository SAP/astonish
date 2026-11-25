package k8s

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the production initializer with Linux /bin/sh, not the host's shell
// or a string assertion. In particular, Debian dash interprets [^.] differently
// from bash; an empty restore generation must not mistake '..' for metadata.
func TestK8sRestoreInitializerPOSIXShell(t *testing.T) {
	for _, name := range []string{"empty", "empty-data", "visible", "hidden", "double-dot", "dangling-link", "populated-data", "wrong-owner"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var rejectedPath string
			switch name {
			case "empty-data", "populated-data":
				if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
					t.Fatal(err)
				}
				if name == "populated-data" {
					rejectedPath = "data/existing"
				}
			case "visible":
				rejectedPath = "existing"
			case "hidden":
				rejectedPath = ".existing"
			case "double-dot":
				rejectedPath = "..existing"
			case "wrong-owner":
				rejectedPath = ".owner"
			case "dangling-link":
				if err := os.Symlink("missing", filepath.Join(root, ".existing")); err != nil {
					t.Fatal(err)
				}
			}
			original := []byte("unowned bytes\x00\xff\n")
			if rejectedPath != "" {
				if err := os.WriteFile(filepath.Join(root, rejectedPath), original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			owner := "k8s-home-v1:" + strings.Repeat("a", 64)
			script := strings.ReplaceAll(botHomeInitScript, mountBotHome, shellQuote(root))
			run := func() ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
				cmd.Env = append(os.Environ(), "EXPECTED_OWNER="+owner)
				return cmd.CombinedOutput()
			}
			out, err := run()
			if name != "empty" && name != "empty-data" {
				if err == nil {
					t.Fatalf("adopted unowned target: %s", out)
				}
				if rejectedPath != "" {
					got, readErr := os.ReadFile(filepath.Join(root, rejectedPath))
					if readErr != nil || !bytes.Equal(got, original) {
						t.Fatalf("changed unowned bytes: %q %v", got, readErr)
					}
				}
				if name == "dangling-link" {
					if target, linkErr := os.Readlink(filepath.Join(root, ".existing")); linkErr != nil || target != "missing" {
						t.Fatalf("changed unowned link: %q %v", target, linkErr)
					}
				}
				if name != "wrong-owner" {
					if _, statErr := os.Lstat(filepath.Join(root, ".owner")); !os.IsNotExist(statErr) {
						t.Fatalf("wrote owner for rejected target: %v", statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("empty restore target initialization: %v: %s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(root, ".owner"))
			if err != nil || string(got) != owner+"\n" {
				t.Fatalf("owner mismatch: %q %v", got, err)
			}
			info, err := os.Stat(filepath.Join(root, "data"))
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatalf("invalid data directory: %v %v", info, err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".initializing")); !os.IsNotExist(err) {
				t.Fatalf("initialization marker remains: %v", err)
			}
			if out, err := run(); err != nil {
				t.Fatalf("owned target initialization not idempotent: %v: %s", err, out)
			}
		})
	}
}
