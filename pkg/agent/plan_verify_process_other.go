//go:build !unix

package agent

import (
	"os"
	"os/exec"
)

func configurePlanVerifyProcess(cmd *exec.Cmd) {}

// On platforms without Unix process groups (including Windows), keep the
// CommandContext process-kill fallback; WaitDelay bounds inherited pipe waits.
func killPlanVerifyProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
