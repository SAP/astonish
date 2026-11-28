package docker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotHomeCleanupMissingHomeStillDeletesWorkspace(t *testing.T) {
	b := testBotHome()
	log := filepath.Join(t.TempDir(), "calls")
	script := `printf '%s\n' "$*" >> '` + log + `'
if [ "$1 $2 $3" = 'volume inspect ` + b.Ref + `' ]; then echo 'No such volume' >&2; exit 1; fi
if [ "$1 $2" = 'volume inspect' ]; then printf '%s' '[{"Name":"` + workspaceVolume(b) + `","Driver":"local","Labels":{"astonish.bot-home.scope":"` + b.Scope.Key() + `","astonish.bot-home.version":"workspace-1"}}]'; fi
`
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: writeDockerTestScript(t, log, script)}}
	if err := db.DeleteBotHome(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "volume rm "+workspaceVolume(b)) {
		t.Fatalf("workspace retained after missing home: %s", data)
	}
	if strings.Contains(string(data), "volume create") {
		t.Fatal("cleanup recreated Home")
	}
}

type botCleanupDeleteFailureStore struct {
	store.SandboxSessionStore
	commit bool
}

func (s *botCleanupDeleteFailureStore) Delete(ctx context.Context, id string) error {
	if s.commit {
		if err := s.SandboxSessionStore.Delete(ctx, id); err != nil {
			return err
		}
	}
	return errors.New("injected interruption at registry deletion")
}

func TestBotHomeCleanupExactOwnershipAndRetry(t *testing.T) {
	for _, mode := range []string{"owned", "owned-private", "owned-restored-private", "foreign-private", "wrong-generation-private", "bind-private", "readonly-private", "duplicate-private", "unexpected-mount", "unexpected-private-mount", "missing-container", "foreign-owner", "foreign-label", "foreign-name", "foreign-mount", "revoked", "overlay-failure", "registry-failure", "registry-committed-crash", "unregistered"} {
		t.Run(mode, func(t *testing.T) {
			b := testBotHome()
			if mode == "owned-restored-private" || mode == "wrong-generation-private" {
				b.Ref += "-restore-" + uuid.NewString()
			}
			id := uuid.NewString()
			root := t.TempDir()
			log := filepath.Join(root, "calls")
			gone := filepath.Join(root, "gone")
			st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
			if err != nil {
				t.Fatal(err)
			}
			reg := sandbox.NewSessionRegistryFromStore(st)
			registryFailure := mode == "registry-failure" || mode == "registry-committed-crash"
			if registryFailure {
				reg = sandbox.NewSessionRegistryFromStore(&botCleanupDeleteFailureStore{SandboxSessionStore: st, commit: mode == "registry-committed-crash"})
			}
			db := &DockerBackend{cfg: Config{Sessions: reg, UppersDir: filepath.Join(root, "uppers"), LayersDir: filepath.Join(root, "layers")}}
			spec := sandbox.SessionSpec{SessionID: id, UserID: b.Scope.UserID, OrgSlug: b.Scope.OrgID, TeamSlug: b.Scope.TeamID, BotHome: b, TemplateID: "base", LayerChain: []string{"base"}, Image: "image"}
			if err := db.writeSessionMetadata(newDockerSessionMetadata(spec)); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(db.upperPath(id), "private-payload")
			if err := os.WriteFile(payload, []byte("owned data"), 0600); err != nil {
				t.Fatal(err)
			}
			owner := b.Scope.UserID
			if mode == "foreign-owner" {
				owner = "foreign"
			}
			if mode != "unregistered" {
				if err := reg.PutSession(&store.SandboxSession{SessionID: id, CreatedBy: owner, Backend: "docker", ContainerName: containerName(id)}); err != nil {
					t.Fatal(err)
				}
			}
			label := b.Scope.Key()
			if mode == "foreign-label" {
				label = "foreign"
			}
			homeRef := b.Ref
			if mode == "foreign-mount" {
				homeRef = "foreign"
			}
			name := "/" + containerName(id)
			if mode == "foreign-name" {
				name = "/foreign-forged-session-label"
			}
			mounts := []any{
				map[string]any{"Type": "volume", "Name": homeRef, "Destination": botHomeMount, "RW": true},
				map[string]any{"Type": "volume", "Name": overlayVolumeName(id), "Destination": mountOverlay, "RW": true},
				map[string]any{"Type": "bind", "Source": db.upperPath(id), "Destination": mountUppers, "RW": true},
				map[string]any{"Type": "bind", "Source": db.cfg.LayersDir, "Destination": mountLayers, "RW": false},
			}
			if strings.Contains(mode, "private") {
				private := map[string]any{"Type": "volume", "Name": workspaceVolume(b), "Destination": botPrivateMount, "RW": true}
				switch mode {
				case "foreign-private":
					private["Name"] = "foreign"
				case "wrong-generation-private":
					private["Name"] = "astonish-bot-workspace-" + b.Scope.Key()
				case "bind-private":
					private["Type"] = "bind"
				case "readonly-private":
					private["RW"] = false
				case "duplicate-private":
					mounts = append(mounts, private)
				}
				mounts = append(mounts, private)
			}
			if mode == "unexpected-mount" || mode == "unexpected-private-mount" {
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/foreign", "Destination": "/external", "RW": true})
			}
			raw, err := json.Marshal(map[string]any{"ID": "immutable-id", "Name": name, "Config": map[string]any{"Labels": map[string]string{"astonish.session_id": id, botHomeScopeLabel: label, "astonish.org": b.Scope.OrgID, "astonish.team": b.Scope.TeamID}}, "Mounts": mounts})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing-container" {
				if err := os.WriteFile(gone, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			failure := ""
			if mode == "overlay-failure" {
				failure = "echo 'volume busy' >&2; exit 1"
			}
			script := `printf '%s\n' "$*" >> '` + log + `'
case "$1" in
ps) if [ ! -f '` + gone + `' ]; then echo immutable-id; fi;;
inspect) if [ -f '` + gone + `' ]; then echo 'No such object' >&2; exit 1; fi; printf '%s' '` + string(raw) + `';;
rm) touch '` + gone + `';;
volume) if [ "$2 $3" = 'inspect ` + overlayVolumeName(id) + `' ]; then echo 'No such volume' >&2; exit 1; fi; if [ "$2" = inspect ]; then printf '%s' '` + homeVolumeJSON(b, "local", nil, b.Scope.Key()) + `'; else ` + failure + `; fi;;
esac`
			// Empty shell branches require a command.
			script = strings.ReplaceAll(script, "else ; fi", "else :; fi")
			db.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, script)
			calls := 0
			authorize := func(context.Context) error {
				calls++
				if mode == "revoked" && calls > 1 {
					return errors.New("revoked")
				}
				return nil
			}
			err = db.ReconcileBotHomeSessions(t.Context(), b, authorize)
			owned := mode == "owned" || mode == "owned-private" || mode == "owned-restored-private"
			success := owned || mode == "missing-container"
			if (err == nil) != success {
				t.Fatalf("reconcile %v", err)
			}
			data, e := os.ReadFile(log)
			if e != nil {
				t.Fatal(e)
			}
			removed := strings.Contains(string(data), "rm -f -v immutable-id")
			if removed != (owned || mode == "overlay-failure" || registryFailure) {
				t.Fatalf("unsafe/missing destruction: %s", data)
			}
			if strings.Contains(string(data), "volume rm -f") {
				t.Fatal("forced volume removal")
			}
			rec, e := reg.GetSession(id)
			if e != nil {
				t.Fatal(e)
			}
			if success && rec != nil {
				t.Fatal("registry retained after success")
			}
			if mode == "overlay-failure" && rec == nil {
				t.Fatal("failure discarded retry authority")
			}
			if registryFailure {
				if _, err := os.Stat(payload); !os.IsNotExist(err) {
					t.Fatalf("private payload survived registry deletion attempt: %v", err)
				}
				m, err := db.readSessionMetadata(id)
				if err != nil || m == nil || !m.CleanupPending {
					t.Fatalf("registry interruption discarded discoverable recovery evidence: %+v %v", m, err)
				}
				if _, err := db.recoverySpec(m, spec, true); err == nil {
					t.Fatal("cleanup receipt permitted session recreation")
				}
			}
			if mode == "overlay-failure" || registryFailure {
				// Resume after process restart with no in-memory registry/backend state.
				st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
				if err != nil {
					t.Fatal(err)
				}
				cfg := db.cfg
				cfg.Sessions = sandbox.NewSessionRegistryFromStore(st)
				if failure != "" {
					script = strings.ReplaceAll(script, failure, ":")
				}
				if registryFailure {
					script = "if [ \"$1 $2\" = 'volume rm' ]; then echo 'No such volume' >&2; exit 1; fi\n" + script
				}
				cfg.ContainerRuntimePath = writeDockerTestScript(t, log, script)
				restarted := &DockerBackend{cfg: cfg}
				if mode == "registry-committed-crash" {
					// An unregistered receipt must never authorize deleting a new
					// overlay or container that appeared under the same name.
					for _, replacement := range []string{"container", "overlay"} {
						replacementScript := script
						if replacement == "container" {
							if err := os.Remove(gone); err != nil {
								t.Fatal(err)
							}
						} else {
							replacementScript = strings.ReplaceAll(script, "echo 'No such volume' >&2; exit 1", "echo '[]'; exit 0")
						}
						restarted.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, replacementScript)
						before, _ := os.ReadFile(log)
						if err := restarted.ReconcileBotHomeSessions(t.Context(), b, authorize); err == nil {
							t.Fatalf("accepted replacement %s", replacement)
						}
						after, _ := os.ReadFile(log)
						if strings.Contains(string(after[len(before):]), "rm ") {
							t.Fatalf("removed replacement %s", replacement)
						}
						if replacement == "container" {
							if err := os.WriteFile(gone, nil, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
					restarted.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, script)
				}
				if err := restarted.ReconcileBotHomeSessions(t.Context(), b, authorize); err != nil {
					t.Fatalf("restart retry: %v", err)
				}
				if rec, err := cfg.Sessions.GetSession(id); err != nil || rec != nil {
					t.Fatalf("restart registry: %+v %v", rec, err)
				}
				if _, err := os.Stat(db.upperPath(id)); !os.IsNotExist(err) {
					t.Fatalf("restart upper cleanup: %v", err)
				}
			}
			if success {
				if err := db.ReconcileBotHomeSessions(t.Context(), b, authorize); err != nil {
					t.Fatalf("idempotent retry: %v", err)
				}
			}
		})
	}
}
