package astonish

import "testing"

func TestShouldLaunchBareCodeWith(t *testing.T) {
	tests := []struct {
		name      string
		stdinTTY  bool
		stdoutTTY bool
		want      bool
	}{
		{name: "interactive terminal", stdinTTY: true, stdoutTTY: true, want: true},
		{name: "piped stdin", stdinTTY: false, stdoutTTY: true, want: false},
		{name: "redirected stdout", stdinTTY: true, stdoutTTY: false, want: false},
		{name: "no TTY", stdinTTY: false, stdoutTTY: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldLaunchBareCodeWith(tt.stdinTTY, tt.stdoutTTY)
			if got != tt.want {
				t.Fatalf("shouldLaunchBareCodeWith(%v, %v) = %v, want %v", tt.stdinTTY, tt.stdoutTTY, got, tt.want)
			}
		})
	}
}

func TestBareCodeUnavailableHint(t *testing.T) {
	tests := []struct {
		name      string
		stdinTTY  bool
		stdoutTTY bool
		wantHint  bool
	}{
		{name: "piped stdin", stdinTTY: false, stdoutTTY: true, wantHint: true},
		{name: "redirected stdout", stdinTTY: true, stdoutTTY: false, wantHint: true},
		{name: "interactive terminal", stdinTTY: true, stdoutTTY: true, wantHint: false},
		{name: "no TTY", stdinTTY: false, stdoutTTY: false, wantHint: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bareCodeUnavailableHint(tt.stdinTTY, tt.stdoutTTY)
			if (got != "") != tt.wantHint {
				t.Fatalf("bareCodeUnavailableHint() = %q, wantHint=%v", got, tt.wantHint)
			}
		})
	}
}
