//go:build !unix

package tools

import (
	"errors"
	"os"
)

func ownedProcessGroup(*os.Process) int { return 0 }
func processGroupAlive(int) bool        { return false }

// Platforms without Unix process groups retain direct-process termination.
func signalOwnedProcess(p *os.Process, _ int, _ bool) error {
	if p == nil {
		return nil
	}
	err := p.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
