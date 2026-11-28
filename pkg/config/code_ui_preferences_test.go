package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCodeUIMissingAndIndependentRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	path, err := GetCodeUIPreferencesPath()
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadCodeUIPreferences()
	if err != nil || p.ThemeID != "" {
		t.Fatalf("missing: %+v %v", p, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("load created a directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(filepath.Dir(path), "config.yaml")
	original := []byte("# preserve credentials and formatting\ngeneral: {}\n")
	if err := os.WriteFile(cfg, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dark-orange", "blue-gray", "future-palette"} {
		if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: id}); err != nil {
			t.Fatal(err)
		}
		p, err := LoadCodeUIPreferences()
		if err != nil || p.Version != 1 || p.ThemeID != id {
			t.Fatalf("round trip: %+v %v", p, err)
		}
	}
	got, err := os.ReadFile(cfg)
	if err != nil || string(got) != string(original) {
		t.Fatal("config.yaml changed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file permissions: %v %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected side effects: %v %v", entries, err)
	}
}

func TestCodeUIInvalidAndFutureLoadsNeverWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	path, _ := GetCodeUIPreferencesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{"private-secret not json", `{"version":0,"theme_id":"private-secret"}`, `{"version":2,"theme_id":"private-secret"}`, `null`, `{"version":1} trailing`} {
		if err := os.WriteFile(path, []byte(record), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCodeUIPreferences(); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("invalid load: %v", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != record {
			t.Fatal("load repaired invalid data")
		}
	}
	future := `{"version":999,"theme_id":"unknown","future_field":true}`
	if err := os.WriteFile(path, []byte(future), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: "blue-gray"}); err == nil {
		t.Fatal("future version overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != future {
		t.Fatal("future bytes changed")
	}
}

type codeUIFaultFile struct {
	*os.File
	stage string
}

func (f codeUIFaultFile) Write(p []byte) (int, error) {
	if f.stage == "write" {
		return 0, errors.New("write fault")
	}
	if f.stage == "short" {
		return 1, nil
	}
	return f.File.Write(p)
}
func (f codeUIFaultFile) Sync() error {
	if f.stage == "sync" {
		return errors.New("sync fault")
	}
	return f.File.Sync()
}
func (f codeUIFaultFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return errors.New("close fault")
	}
	return err
}

func TestCodeUIAtomicFailuresPreserveDestination(t *testing.T) {
	for _, stage := range []string{"create", "write", "short", "sync", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "code-ui-preferences.json")
			old := `{"version":1,"theme_id":"blue-gray"}`
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			ops := codeUIPreferenceFileOps{
				createTemp: func(dir, pattern string) (codeUIPreferenceFile, error) {
					if stage == "create" {
						return nil, errors.New("create fault")
					}
					f, err := os.CreateTemp(dir, pattern)
					return codeUIFaultFile{f, stage}, err
				},
				rename: func(from, to string) error {
					if stage == "rename" {
						return errors.New("rename fault")
					}
					return os.Rename(from, to)
				},
			}
			if err := saveCodeUIPreferences(path, CodeUIPreferences{ThemeID: "dark-orange"}, ops); err == nil {
				t.Fatal("expected failure")
			}
			got, _ := os.ReadFile(path)
			if string(got) != old {
				t.Fatal("pre-rename failure changed bytes")
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Fatalf("temporary file leaked: %v", entries)
			}
		})
	}
}

func TestCodeUIIOFailures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	path, _ := GetCodeUIPreferencesPath()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCodeUIPreferences(); err == nil {
		t.Fatal("directory accepted")
	}
	if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: "dark-orange"}); err == nil {
		t.Fatal("directory replaced")
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveCodeUIPreferences(filepath.Join(blocked, "prefs"), CodeUIPreferences{}, codeUIPreferenceFileOps{}); err == nil {
		t.Fatal("invalid parent accepted")
	}
}

func TestCodeUIConcurrentAtomicReadersWriters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: "blue-gray"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			for i := 0; i < 40; i++ {
				id := "blue-gray"
				if i%2 == 0 {
					id = "dark-orange"
				}
				if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: id}); err != nil {
					t.Error(err)
					return
				}
				p, err := LoadCodeUIPreferences()
				if err != nil || p.Version != 1 || (p.ThemeID != "blue-gray" && p.ThemeID != "dark-orange") {
					t.Errorf("torn read: %+v %v", p, err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := SaveCodeUIPreferences(CodeUIPreferences{ThemeID: "last"}); err != nil {
		t.Fatal(err)
	}
	p, err := LoadCodeUIPreferences()
	if err != nil || p.ThemeID != "last" {
		t.Fatalf("last rename: %+v %v", p, err)
	}
	path, _ := GetCodeUIPreferencesPath()
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}
