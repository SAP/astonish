package agent

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlanVerifyExecutionOutput(t *testing.T) {
	for _, tc := range []struct {
		name, command, output string
		code                  int
	}{
		{"success", "echo stdout", "stdout", 0},
		{"failure", "echo stderr >&2; exit 7", "stderr", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := tc.command
			if runtime.GOOS == "windows" {
				command = strings.ReplaceAll(command, ";", " &")
			}
			code, output, err := defaultPlanVerify(t.TempDir(), command, 5*time.Second)
			if err != nil || code != tc.code || output != tc.output {
				t.Fatalf("got (%d, %q, %v), want (%d, %q, nil)", code, output, err, tc.code, tc.output)
			}
		})
	}
}

func TestPlanVerifyEmptyCommand(t *testing.T) {
	code, output, err := DefaultPlanVerify(t.TempDir(), " \n\t")
	if code != -1 || output != "" || err == nil || err.Error() != "empty verify command" {
		t.Fatalf("got (%d, %q, %v)", code, output, err)
	}
}

func TestPlanVerifyOutputWriterBounded(t *testing.T) {
	var w planVerifyOutputWriter
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < 1024; i++ {
		n, err := w.Write(chunk)
		if n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if w.used != planVerifyMaxOutput || string(w.data[:]) != strings.Repeat("x", planVerifyMaxOutput) {
		t.Fatal("writer did not retain exactly the bounded prefix")
	}
}

func TestPlanVerifyOutputWriterUTF8(t *testing.T) {
	var w planVerifyOutputWriter
	_, _ = w.Write([]byte(strings.Repeat("x", planVerifyMaxOutput-1) + "€"))
	output := truncateVerifyOutput(w.data[:w.used])
	if !utf8.ValidString(output) || !strings.HasSuffix(output, "\uFFFD") {
		t.Fatalf("invalid truncated UTF-8 suffix: %q", output[len(output)-5:])
	}
}
