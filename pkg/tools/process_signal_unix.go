//go:build unix

package tools

import (
	"errors"
	"os"
	"syscall"
)

// creack/pty starts a new session with the child as its group leader. Verify
// that fact while it is still our child; never infer ownership from a PID later.
func ownedProcessGroup(p *os.Process) int {
	if p == nil || p.Pid <= 1 {
		return 0
	}
	pgid, err := syscall.Getpgid(p.Pid)
	parent, parentErr := syscall.Getpgid(0)
	if err != nil || parentErr != nil || pgid != p.Pid || pgid == parent {
		return 0
	}
	return pgid
}

func processGroupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalOwnedProcess(p *os.Process, pgid int, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if pgid > 1 {
		parent, err := syscall.Getpgid(0)
		if err != nil {
			return err
		}
		if pgid == parent || p == nil || pgid != p.Pid {
			return errors.New("refusing to signal unowned process group")
		}
		err = syscall.Kill(-pgid, sig)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if p == nil {
		return nil
	}
	err := p.Signal(sig)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
