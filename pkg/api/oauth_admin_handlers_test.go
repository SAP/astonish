package api

import "testing"

func TestCanAdministerOAuthClients(t *testing.T) {
	for role, want := range map[string]bool{
		"owner":  true,
		"admin":  true,
		"member": false,
		"":       false,
	} {
		if got := canAdministerOAuthClients(role); got != want {
			t.Errorf("canAdministerOAuthClients(%q) = %t, want %t", role, got, want)
		}
	}
}
