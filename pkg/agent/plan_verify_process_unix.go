//go:build unix

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configurePlanVerifyProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killPlanVerifyProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	// The shell is the group leader; kill its descendants, including those
	// still holding stdout/stderr after the shell has exited.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
