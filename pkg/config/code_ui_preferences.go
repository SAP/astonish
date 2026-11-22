package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const codeUIPreferencesVersion = 1

// CodeUIPreferences is local UI state, independent of application settings and sessions.
// Theme IDs are strings so configuration has no dependency on terminal rendering.
type CodeUIPreferences struct {
	Version int    `json:"version"`
	ThemeID string `json:"theme_id"`
}

func GetCodeUIPreferencesPath() (string, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve Code UI preferences directory: %w", err)
	}
	return filepath.Join(dir, "code-ui-preferences.json"), nil
}

// LoadCodeUIPreferences never creates, repairs or rewrites a file. A missing file is unset.
func LoadCodeUIPreferences() (CodeUIPreferences, error) {
	path, err := GetCodeUIPreferencesPath()
	if err != nil {
		return CodeUIPreferences{}, err
	}
	return loadCodeUIPreferences(path)
}

func loadCodeUIPreferences(path string) (CodeUIPreferences, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return CodeUIPreferences{}, nil
	}
	if err != nil {
		return CodeUIPreferences{}, fmt.Errorf("read Code UI preferences: %w", err)
	}
	var p CodeUIPreferences
	// Do not include decoder errors: malformed input may contain sensitive data.
	if json.Unmarshal(data, &p) != nil {
		return CodeUIPreferences{}, errors.New("decode Code UI preferences: invalid JSON record")
	}
	if p.Version != codeUIPreferencesVersion {
		return CodeUIPreferences{}, fmt.Errorf("load Code UI preferences: unsupported version %d", p.Version)
	}
	return p, nil
}

// SaveCodeUIPreferences syncs a private same-directory temporary file then renames
// it atomically. Concurrent readers see complete records; the last successful rename
// wins. This does not promise directory-sync crash durability or cross-process merging.
func SaveCodeUIPreferences(p CodeUIPreferences) error {
	path, err := GetCodeUIPreferencesPath()
	if err != nil {
		return err
	}
	return saveCodeUIPreferences(path, p, codeUIPreferenceFileOps{})
}

// The narrow operation seam lets tests exercise pre-rename failures without global
// hooks, and without affecting concurrent preference writers.
type codeUIPreferenceFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}
type codeUIPreferenceFileOps struct {
	createTemp func(string, string) (codeUIPreferenceFile, error)
	rename     func(string, string) error
}

func saveCodeUIPreferences(path string, p CodeUIPreferences, ops codeUIPreferenceFileOps) error {
	// Refuse to downgrade a record owned by a newer binary. An explicit save
	// can replace invalid older state; loads themselves never repair it.
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read Code UI preferences before save: %w", readErr)
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(existing, &header) == nil && header.Version > codeUIPreferencesVersion {
		return fmt.Errorf("save Code UI preferences: unsupported version %d", header.Version)
	}
	p.Version = codeUIPreferencesVersion
	data, err := json.Marshal(p)
	if err != nil {
		return errors.New("encode Code UI preferences: invalid record")
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create Code UI preferences directory: %w", err)
	}
	createTemp := ops.createTemp
	if createTemp == nil {
		createTemp = func(dir, pattern string) (codeUIPreferenceFile, error) { return os.CreateTemp(dir, pattern) }
	}
	rename := ops.rename
	if rename == nil {
		rename = os.Rename
	}
	f, err := createTemp(dir, ".code-ui-preferences-*")
	if err != nil {
		return fmt.Errorf("create Code UI preferences temporary file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		_ = f.Close()
		return fmt.Errorf("write Code UI preferences: %w", writeErr)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync Code UI preferences: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close Code UI preferences: %w", err)
	}
	if err := rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace Code UI preferences: %w", err)
	}
	return nil
}
