//go:build !linux

package sandbox

import (
	"io"
	"os"
)

// RunBotHomeArchive fails closed outside the Linux sandbox runtime.
func RunBotHomeArchive(home, private *os.File, input io.Reader, output io.Writer, admissionHeld bool) error {
	return ErrBotHomeUnsupported
}
