package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CodeWorkspacePreferences stores only explicit local new-session UI choices.
// It deliberately contains no copy lists, credentials, or project paths.
type CodeWorkspacePreferences struct {
	Version        int    `json:"version"`
	NewSessionMode string `json:"new_session_mode"`
}

func GetCodeWorkspacePreferencesPath() (string, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "code-workspace-preferences.json"), nil
}
func LoadCodeWorkspacePreferences() (CodeWorkspacePreferences, error) {
	path, err := GetCodeWorkspacePreferencesPath()
	if err != nil {
		return CodeWorkspacePreferences{}, err
	}
	return loadCodeWorkspacePreferences(path)
}
func loadCodeWorkspacePreferences(path string) (CodeWorkspacePreferences, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return CodeWorkspacePreferences{}, nil
	}
	if err != nil {
		return CodeWorkspacePreferences{}, err
	}
	var p CodeWorkspacePreferences
	if json.Unmarshal(data, &p) != nil {
		return p, errors.New("invalid Code workspace preference record")
	}
	if p.Version != 1 {
		return CodeWorkspacePreferences{}, fmt.Errorf("unsupported Code workspace preference version %d", p.Version)
	}
	if p.NewSessionMode != "" && p.NewSessionMode != "current" && p.NewSessionMode != "independent" {
		return CodeWorkspacePreferences{}, errors.New("invalid Code workspace preference mode")
	}
	return p, nil
}
func SaveCodeWorkspacePreferences(p CodeWorkspacePreferences) error {
	path, err := GetCodeWorkspacePreferencesPath()
	if err != nil {
		return err
	}
	return saveCodeWorkspacePreferences(path, p)
}
func saveCodeWorkspacePreferences(path string, p CodeWorkspacePreferences) error {
	if p.NewSessionMode != "current" && p.NewSessionMode != "independent" {
		return errors.New("invalid Code workspace preference mode")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) == nil && header.Version > 1 {
		return fmt.Errorf("unsupported Code workspace preference version %d", header.Version)
	}
	p.Version = 1
	data, err = json.Marshal(p)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".code-workspace-preferences-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
