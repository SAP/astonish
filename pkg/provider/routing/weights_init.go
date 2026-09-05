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

	// RouterWeightsSHA256 is the expected SHA-256 hex digest of the weights file.
	// Update this when training produces new weights.
	RouterWeightsSHA256 = "073675939061dd2eddcec9e8910a175a6ef83d7be9112b9393bcff6956ccfb72"
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
// When RouterWeightsSHA256 is non-empty, the file's SHA-256 digest is verified
// after download or copy. A mismatch deletes the file and returns an error.
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
				slog.Info("router weights: copied from local training project", "source", expanded)
				if err := verifyWeightsChecksum(destPath); err != nil {
					_ = os.Remove(destPath)
					return "", err
				}
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
		_ = os.Remove(destPath)
		return "", err
	}
	slog.Info("router weights: downloaded successfully", "path", destPath)
	return destPath, nil
}

// verifyWeightsChecksum checks the SHA-256 digest of path against
// RouterWeightsSHA256. If the expected hash is empty, verification is skipped.
func verifyWeightsChecksum(path string) error {
	if RouterWeightsSHA256 == "" {
		return nil // no expected hash configured — skip verification
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify weights checksum: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify weights checksum: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != RouterWeightsSHA256 {
		return fmt.Errorf("router weights checksum mismatch: got %s, want %s", got, RouterWeightsSHA256)
	}
	return nil
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

	if _, err = io.Copy(out, resp.Body); err != nil {
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
