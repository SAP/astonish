package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// finishBotHomeHelper samples terminal status and cleanup authority together.
// No cached inspection authorizes removal, and no other request shares a helper.
func (db *DockerBackend) finishBotHomeHelper(ctx context.Context, name, scope string, checkExit bool) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", `{"labels":{{json .Config.Labels}},"state":{{json .State}}}`, name)
	if err != nil {
		if !checkExit && isDockerNotFoundError(err) {
			return nil
		}
		return err
	}
	var info struct {
		Labels map[string]string
		State  *struct {
			Running  *bool
			ExitCode *int
		}
	}
	if json.Unmarshal(raw, &info) != nil || info.Labels[botHomeReaderLabel] != name || info.Labels[botHomeScopeLabel] != scope {
		return errors.New("file helper cleanup ownership mismatch")
	}
	if checkExit {
		if info.State == nil || info.State.Running == nil || info.State.ExitCode == nil {
			err = errors.New("file helper terminal state missing")
		} else if *info.State.Running || *info.State.ExitCode != 0 {
			err = fmt.Errorf("file helper did not exit successfully: running=%t exit=%d", *info.State.Running, *info.State.ExitCode)
		}
	}
	_, removeErr := runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", "-fv", name)
	return errors.Join(err, removeErr)
}
