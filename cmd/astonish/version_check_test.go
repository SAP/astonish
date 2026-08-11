package astonish

import "testing"

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{"same version", "3.4.0", "3.4.0", false},
		{"latest is newer patch", "3.4.1", "3.4.0", true},
		{"latest is newer minor", "3.5.0", "3.4.0", true},
		{"latest is newer major", "4.0.0", "3.4.0", true},
		{"latest is older patch", "3.3.2", "3.4.0", false},
		{"latest is older minor", "3.3.0", "3.4.0", false},
		{"latest is older major", "2.9.9", "3.4.0", false},
		{"current is beta, latest is older stable", "3.4.0", "3.5.0-beta.1", false},
		{"current is beta, latest is same base stable", "3.5.0", "3.5.0-beta.1", false},
		{"current is beta, latest is newer stable", "3.6.0", "3.5.0-beta.1", false},
		{"both same with prerelease", "3.5.0-beta.1", "3.5.0-beta.2", false},
		{"dev version", "3.4.0", "dev", false},
		{"empty current", "3.4.0", "", false},
		{"v prefix in latest", "v3.4.1", "3.4.0", true},
		{"v prefix in current", "3.4.1", "v3.4.0", true},
		{"both have v prefix", "v3.4.1", "v3.4.0", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isNewerVersion(c.latest, c.current)
			if got != c.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
			}
		})
	}
}
