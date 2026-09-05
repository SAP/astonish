package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// RouterWeightsFilename is the filename stored in the models directory.
	RouterWeightsFilename = "router_weights.npz"

	// RouterWeightsURL is the download URL for the pre-trained router weights.
	// This is used when no local copy is available.
	RouterWeightsURL = "https://github.com/SAP/astonish/releases/latest/download/router_weights.npz"

	// maxWeightsFileSize is the maximum size we'll download for the weights file (10 MB).
	// The actual file is ~99 KB; this is a generous safety cap.
	maxWeightsFileSize = 10 * 1024 * 1024

	// RouterWeightsSHA256 is the expected SHA-256 hex digest of router_weights.npz.
	// When non-empty, EnsureRouterWeights verifies downloaded/copied files against
	// this hash. Set to empty to skip verification during development.
	RouterWeightsSHA256 = ""
)

// localTrainingOutputPaths lists candidate locations for the router_weights.npz
// produced by the astonish-router training project. Checked in order; first
// existing path wins. This avoids a network download in development.
var localTrainingOutputPaths = []string{
	// Sibling project directory (default clone layout)
	"../astonish-router/outputs/router_weights.npz",
	// Absolute path for users who clone astonish-router to their Projects dir
	"~/Projects/astonish-router/outputs/router_weights.npz",
}

// EnsureRouterWeights ensures the router weights file exists at
// filepath.Join(modelsDir, RouterWeightsFilename). If it is already present,
// it returns immediately. Otherwise it tries (in order):
//  1. Copy from any local training output path
//  2. Download from RouterWeightsURL
//
// Returns the path to the weights file on success.
func EnsureRouterWeights(modelsDir string) (string, error) {
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("create models dir: %w", err)
	}
	destPath := filepath.Join(modelsDir, RouterWeightsFilename)

	// Already present.
	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil
	}

	// Try local training output first (development workflow).
	for _, candidate := range localTrainingOutputPaths {
		expanded := expandTilde(candidate)
		if _, err := os.Stat(expanded); err == nil {
			if copyErr := copyFile(expanded, destPath); copyErr == nil {
				if verifyErr := verifyWeightsChecksum(destPath); verifyErr != nil {
					slog.Warn("router weights: local copy failed checksum", "source", expanded, "error", verifyErr)
					continue // try next candidate or fall through to download
				}
				slog.Info("router weights: copied from local training project", "source", expanded)
				return destPath, nil
			}
		}
	}

	// Fall back to download.
	slog.Info("router weights: downloading from GitHub releases (~99KB)...",
		"url", RouterWeightsURL)
	if err := downloadFile(RouterWeightsURL, destPath); err != nil {
		return "", fmt.Errorf("download router weights: %w", err)
	}
	if err := verifyWeightsChecksum(destPath); err != nil {
		return "", err
	}
	slog.Info("router weights: downloaded successfully", "path", destPath)
	return destPath, nil
}

// RouterWeightsPath returns the expected path inside modelsDir without
// downloading or copying anything. Use this when you only want to check
// whether the file exists.
func RouterWeightsPath(modelsDir string) string {
	return filepath.Join(modelsDir, RouterWeightsFilename)
}

// copyFile copies src to dst, creating dst atomically via a temp file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		_ = os.Remove(tmp) // clean up temp on failure
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// downloadFile downloads url to dst, writing atomically via a temp file.
func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		_ = os.Remove(tmp)
	}()

	if _, err = io.Copy(out, io.LimitReader(resp.Body, maxWeightsFileSize)); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// expandTilde expands a leading "~/" in a path to the user's home directory.
func expandTilde(path string) string {
	if len(path) < 2 || path[0] != '~' || path[1] != '/' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// verifyWeightsChecksum computes the SHA-256 of the file at path and compares
// it against RouterWeightsSHA256. If the constant is empty, verification is
// skipped (development mode). Returns an error on mismatch.
func verifyWeightsChecksum(path string) error {
	if RouterWeightsSHA256 == "" {
		return nil // verification disabled
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read for checksum: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != RouterWeightsSHA256 {
		// Remove the untrusted file.
		_ = os.Remove(path)
		return fmt.Errorf("router weights checksum mismatch: got %s, want %s", got, RouterWeightsSHA256)
	}
	return nil
}
