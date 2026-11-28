//go:build !linux

package sandbox

import (
	"io"
	"os"
)

// RunBotHomeWorkspace fails closed outside the Linux sandbox runtime.
func RunBotHomeWorkspace(root *os.File, input io.Reader, output io.Writer) error {
	return ErrBotHomeUnsupported
}
