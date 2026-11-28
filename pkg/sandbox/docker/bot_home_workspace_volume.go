package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
)

func workspaceVolume(h *sandbox.BotHomeBinding) string {
	if suffix, ok := strings.CutPrefix(h.Ref, botHomeVolume(h.Scope)+"-restore-"); ok && validBotHomeVolume(h) {
		return "astonish-bot-workspace-" + h.Scope.Key() + "-restore-" + suffix
	}
	return "astonish-bot-workspace-" + h.Scope.Key()
}

// Separate backing volume: never passed to runtime session creation or exec.
func (db *DockerBackend) ensureWorkspaceVolume(ctx context.Context, h *sandbox.BotHomeBinding) (string, error) {
	ref := workspaceVolume(h)
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", ref)
	if err != nil {
		if !isDockerNotFoundError(err) && !strings.Contains(strings.ToLower(err.Error()), "no such volume") {
			return "", err
		}
		if _, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "create", "--driver=local", "--label="+botHomeScopeLabel+"="+h.Scope.Key(), "--label="+botHomeVersionLabel+"=workspace-1", ref); err != nil {
			return "", err
		}
		raw, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", ref)
	}
	if err != nil {
		return "", err
	}
	var info []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if json.Unmarshal(raw, &info) != nil || len(info) != 1 || info[0].Name != ref || info[0].Driver != "local" || len(info[0].Options) != 0 || info[0].Labels[botHomeScopeLabel] != h.Scope.Key() || info[0].Labels[botHomeVersionLabel] != "workspace-1" {
		return "", errors.New("workspace private volume ownership mismatch")
	}
	return ref, nil
}
