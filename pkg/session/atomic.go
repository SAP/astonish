package session

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWrite writes data to a file atomically using the temp-file + rename pattern.
// This prevents partial writes from corrupting data on crash.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".astonish-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up on failure
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath) // best-effort cleanup
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // best-effort cleanup; write error takes priority
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close() // best-effort cleanup; sync error takes priority
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return nil
}
