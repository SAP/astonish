package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodeWorkspacePreferencesReadOnlyAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	p, err := loadCodeWorkspacePreferences(path)
	if err != nil || p.NewSessionMode != "" {
		t.Fatal(p, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("load created file")
	}
	if err = saveCodeWorkspacePreferences(path, CodeWorkspacePreferences{NewSessionMode: "independent"}); err != nil {
		t.Fatal(err)
	}
	p, err = loadCodeWorkspacePreferences(path)
	if err != nil || p.Version != 1 || p.NewSessionMode != "independent" {
		t.Fatal(p, err)
	}
	s, _ := os.Stat(path)
	if s.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", s.Mode().Perm())
	}
}
func TestCodeWorkspacePreferencesRefuseFutureAndMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	for _, s := range []string{`{"version":99,"new_session_mode":"current"}`, `not-json-secret`} {
		if err := os.WriteFile(path, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCodeWorkspacePreferences(path); err == nil {
			t.Fatal("accepted invalid record")
		}
	}
	future := []byte(`{"version":2,"new_session_mode":"current"}`)
	os.WriteFile(path, future, 0600)
	if err := saveCodeWorkspacePreferences(path, CodeWorkspacePreferences{NewSessionMode: "current"}); err == nil {
		t.Fatal("downgraded")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(future) {
		t.Fatal("rewrote future")
	}
	if err := saveCodeWorkspacePreferences(path, CodeWorkspacePreferences{NewSessionMode: "secret"}); err == nil {
		t.Fatal("invalid mode")
	}
}
