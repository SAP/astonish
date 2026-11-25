package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/sandbox"
)

const botPrivateMount = "/mnt/astonish-bot-private"

// resolveBotFileTarget pins mounts and execution to one physical container.
// It deliberately never consults a session store or performs lifecycle work.
func (db *DockerBackend) resolveBotFileTarget(ctx context.Context, id string, b *sandbox.BotHomeBinding) (string, error) {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", containerName(id))
	if err != nil {
		return "", err
	}
	var c struct {
		ID     string `json:"Id"`
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	if c.ID == "" || !c.State.Running || c.Config.Labels["astonish.session_id"] != id {
		return "", errors.New("sandbox/docker: file target identity mismatch")
	}
	home, private := false, false
	for _, m := range c.Mounts {
		switch m.Destination {
		case botHomeMount:
			if home || m.Type != "volume" || m.Name != b.Ref || !m.RW {
				return "", errors.New("sandbox/docker: Home mount mismatch")
			}
			home = true
		case botPrivateMount:
			if private || m.Type != "volume" || m.Name != workspaceVolume(b) || !m.RW {
				return "", errors.New("sandbox/docker: private mount mismatch")
			}
			private = true
		}
	}
	if !home || !private {
		return "", errors.New("sandbox/docker: retained mounts missing")
	}
	return c.ID, nil
}

// BindBotHomeSession deliberately performs no SQL or Docker IO. Codec callers
// can bind a transaction-derived record without re-entering their repository.
func (db *DockerBackend) BindBotHomeSession(b *sandbox.BotHomeBinding, id string) error {
	if err := validateBotHomeReference(b); err != nil {
		return err
	}
	if id == "" {
		return errors.New("sandbox/docker: missing Bot session")
	}
	if old, loaded := db.botFileSessions.LoadOrStore(*b, id); loaded && old.(string) != id {
		return errors.New("sandbox/docker: Bot file session binding is immutable")
	}
	return nil
}

// PrepareBotHomeSession is the pre-transaction lifecycle boundary. StartSession
// serializes concurrent callers and reuses the persisted authorized recipe.
func (db *DockerBackend) PrepareBotHomeSession(ctx context.Context, b *sandbox.BotHomeBinding, id string) error {
	if err := db.BindBotHomeSession(b, id); err != nil {
		return err
	}
	meta, err := db.readSessionMetadata(id)
	if err != nil {
		return err
	}
	if meta == nil || !meta.BotHome.Equal(b) {
		return errors.New("sandbox/docker: missing or mismatched authorized Bot session recipe")
	}
	if state, err := db.SessionState(ctx, id); err != nil {
		return err
	} else if state == sandbox.SessionStateRunning {
		return nil
	}
	if err := db.StartSession(ctx, id); err != nil {
		return err
	}
	return db.WaitForSessionReady(ctx, id)
}

func (db *DockerBackend) botFileSession(ctx context.Context, b *sandbox.BotHomeBinding) (string, error) {
	value, ok := db.botFileSessions.Load(*b)
	if !ok {
		return "", errors.New("sandbox/docker: Bot file access requires a trusted session binding")
	}
	id := value.(string)
	meta, err := db.readSessionMetadata(id)
	if err != nil {
		return "", err
	}
	if meta == nil {
		return "", errors.New("sandbox/docker: Bot file session has no authorized launch recipe; create the session with its configured tool template first")
	}
	if !meta.BotHome.Equal(b) {
		return "", errors.New("sandbox/docker: Bot session Home mismatch")
	}
	state, stateErr := db.SessionState(ctx, id)
	if stateErr == nil && state == sandbox.SessionStateRunning {
		// File codecs may run inside a store transaction. Do not re-enter the
		// session repository via Exec/WaitForSessionReady/TouchActivity here.
		if err := db.validateBotFileMount(ctx, id, b); err != nil {
			return "", err
		}
		return id, nil
	}
	if stateErr != nil {
		return "", stateErr
	}
	// Codec calls can hold a store transaction. Lifecycle recovery re-enters that
	// repository, and reads must not unpause or provision compute as a side effect.
	// The trusted launcher must prepare the existing authorized recipe first.
	return "", fmt.Errorf("sandbox/docker: Bot file session is %s; trusted lifecycle preparation required before file IO", state)
}

func (db *DockerBackend) validateBotFileMount(ctx context.Context, id string, b *sandbox.BotHomeBinding) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Mounts}}", containerName(id))
	if err != nil {
		return err
	}
	var mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
	if err = json.Unmarshal(raw, &mounts); err != nil {
		return err
	}
	found, foundHome := false, false
	for _, m := range mounts {
		if m.Destination == botHomeMount {
			if foundHome || m.Type != "volume" || m.Name != b.Ref || !m.RW {
				return errors.New("sandbox/docker: Home session mount mismatch")
			}
			foundHome = true
		}
		if m.Destination == botPrivateMount {
			if found || m.Type != "volume" || m.Name != workspaceVolume(b) || !m.RW {
				return errors.New("sandbox/docker: private session mount mismatch")
			}
			found = true
		}
	}
	if !foundHome {
		return errors.New("sandbox/docker: Home session mount missing")
	}
	if !found {
		return errors.New("sandbox/docker: session predates private content mount; explicit compute recreation required, storage retained")
	}
	return nil
}

// Directory descriptors select retained content, not a security boundary: the
// interpreter belongs to the writable session overlay, just like agent tools.
func botSessionPythonArgs(id, program string) []string {
	script := fmt.Sprintf("exec 8<%s; exec 9<%s; exec chroot /sandbox/rootfs /usr/bin/python3 -I -c %s", botPrivateMount, botHomeMount, quoteShell(program))
	return []string{"exec", "--interactive", containerName(id), "/bin/sh", "-ec", script}
}

// Cancellation addresses only this exec's PID in the container PID namespace.
// A cancellation marker closes the race with a docker exec that starts late.
func (db *DockerBackend) botSessionCommand(ctx context.Context, id, program string) *exec.Cmd {
	return db.botSessionExecCommand(ctx, containerName(id), botSessionPythonArgs(id, program))
}

// botCompiledFilesCommand uses the shipped outer executable, never overlay PATH.
// The target is a resolved physical container ID, not a reusable session name.
func (db *DockerBackend) botCompiledFilesCommand(ctx context.Context, target string) *exec.Cmd {
	script := fmt.Sprintf("exec 8<%s; exec 9<%s; exec /usr/local/bin/astonish-host node --internal-home-files", botPrivateMount, botHomeMount)
	return db.botSessionExecCommand(ctx, target, []string{"exec", "--interactive", target, "/bin/sh", "-ec", script})
}

func (db *DockerBackend) botSessionExecCommand(ctx context.Context, target string, args []string) *exec.Cmd {
	token := "/tmp/astonish-file-exec-" + uuid.NewString()
	inner := args[len(args)-1]
	args[len(args)-1] = fmt.Sprintf(`mkdir -p %[1]s; test ! -e %[1]s/cancelled || exit 125; exec 7<&0; ( %[2]s %[1]s ) <&7 & p=$!; echo "$p" > %[1]s/pid; if test -e %[1]s/cancelled; then kill -KILL "$p" 2>/dev/null || true; fi; rc=0; wait "$p" || rc=$?; rm -rf %[1]s; exit "$rc"`, token, inner)
	args = append(args, token) // shell $0 and Python argv both identify this exact exec.
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, args...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := runDocker(c, db.cfg.ContainerRuntimePath, "exec", target, "/bin/sh", "-ec", fmt.Sprintf(`mkdir -p %s; touch %s/cancelled; if test -f %s/pid; then read p < %s/pid; if test -r /proc/$p/cmdline && tr '\000' '\n' < /proc/$p/cmdline | grep -Fxq %s; then kill -KILL "$p"; fi; fi`, token, token, token, token, token))
		if cmd.Process != nil {
			err = errors.Join(err, cmd.Process.Kill())
		}
		return err
	}
	return cmd
}
