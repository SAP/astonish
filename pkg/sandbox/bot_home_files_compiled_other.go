//go:build !linux

package sandbox

import (
	"io"
	"os"
)

// OpenBotHomeFileRoots is available only inside the Linux sandbox runtime.
func OpenBotHomeFileRoots(mount *os.File, owner string, needPrivate, createPrivate bool) (*os.File, *os.File, error) {
	return nil, nil, ErrBotHomeUnsupported
}

// RunBotHomeFiles fails closed outside the Linux sandbox runtime.
func RunBotHomeFiles(home, private *os.File, input io.Reader, output io.Writer) error {
	return ErrBotHomeUnsupported
}
