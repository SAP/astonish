//go:build !linux

package sandbox

import (
	"errors"
	"io"
	"os"
)

func RunBotHomeAdmission(mode BotHomeAdmissionMode, input *os.File, output io.Writer) error {
	return errors.New("compiled Bot Home admission requires Linux")
}
