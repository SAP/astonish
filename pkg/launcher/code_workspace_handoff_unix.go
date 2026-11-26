//go:build !windows

package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/tui/backend"
	"os"
	"syscall"
)

// RunCodeWorkspaceHandoff replaces the closed TUI with a fresh local Code process.
func RunCodeWorkspaceHandoff(ctx context.Context, cfg *CodeConfig, request backend.WorkspaceLaunchRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := finishCodeSessionDeletion(ctx, request); err != nil {
		return err
	}
	request.DeleteSessionID = ""
	args, err := codeWorkspaceHandoffArgs(cfg, request)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err = syscall.Exec(exe, append([]string{exe}, args...), os.Environ()); err != nil {
		return fmt.Errorf("workspace retained at %s; recover with astonish code --dir %q: %w", request.WorkingDir, request.WorkingDir, err)
	}
	return nil
}
