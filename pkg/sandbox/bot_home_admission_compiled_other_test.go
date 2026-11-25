//go:build !linux

package sandbox

import (
	"bytes"
	"os"
	"testing"
)

func TestCompiledAdmissionUnsupported(t *testing.T) {
	var out bytes.Buffer
	if err := RunBotHomeAdmission(BotHomeAdmissionWait, os.Stdin, &out); err == nil || out.Len() != 0 {
		t.Fatal("non-Linux admission did not fail closed", err)
	}
}
