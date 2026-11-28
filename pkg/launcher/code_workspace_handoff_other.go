//go:build windows

package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/tui/backend"
	"os"
	"os/exec"
)

func RunCodeWorkspaceHandoff(ctx context.Context, cfg *CodeConfig, r backend.WorkspaceLaunchRequest) error {
	if e := finishCodeSessionDeletion(ctx, r); e != nil {
		return e
	}
	r.DeleteSessionID = ""
	args, e := codeWorkspaceHandoffArgs(cfg, r)
	if e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("workspace retained at %s; recover with astonish code --dir %q: %w", r.WorkingDir, r.WorkingDir, e)
	}
	return nil
}
