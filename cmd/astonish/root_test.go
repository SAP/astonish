package astonish

import "testing"

func TestShouldLaunchBareChatWith(t *testing.T) {
	tests := []struct {
		name      string
		remote    bool
		stdinTTY  bool
		stdoutTTY bool
		want      bool
	}{
		{name: "remote interactive", remote: true, stdinTTY: true, stdoutTTY: true, want: true},
		{name: "not logged in", remote: false, stdinTTY: true, stdoutTTY: true, want: false},
		{name: "piped stdin", remote: true, stdinTTY: false, stdoutTTY: true, want: false},
		{name: "redirected stdout", remote: true, stdinTTY: true, stdoutTTY: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldLaunchBareChatWith(tt.remote, tt.stdinTTY, tt.stdoutTTY)
			if got != tt.want {
				t.Fatalf("shouldLaunchBareChatWith(%v, %v, %v) = %v, want %v", tt.remote, tt.stdinTTY, tt.stdoutTTY, got, tt.want)
			}
		})
	}
}
