package docker

import (
	"context"
	"slices"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

// Capture only structural launch metadata in memory, never full CLI/env values.
type scopeCaptureBackend struct {
	sandbox.Backend
	spec sandbox.SessionSpec
}

func (b *scopeCaptureBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *scopeCaptureBackend) EnsureBotHome(_ context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return &sandbox.BotHomeBinding{Scope: s, Backend: sandbox.BackendKindDocker, Ref: "owned-home-volume"}, nil
}
func (b *scopeCaptureBackend) CreateSession(_ context.Context, s sandbox.SessionSpec) (*sandbox.Session, error) {
	b.spec = s
	return &sandbox.Session{SessionID: s.SessionID}, nil
}
func (b *scopeCaptureBackend) StartSession(context.Context, string) error        { return nil }
func (b *scopeCaptureBackend) WaitForSessionReady(context.Context, string) error { return nil }
func (b *scopeCaptureBackend) DestroySession(context.Context, string) error      { return nil }

func TestBotPoolDockerTrustedScope(t *testing.T) {
	for _, org := range []string{"owned-org", ""} {
		t.Run("org="+org, func(t *testing.T) {
			scope := sandbox.BotHomeScope{UserID: "owner", BotID: "bot"}
			if org != "" {
				scope.OrgID = org
				scope.TeamID = "owned-team"
			}
			b := &scopeCaptureBackend{}
			p := sandbox.NewBackendPool(b, sandbox.ResourceLimits{})
			defer p.Cleanup()
			home, _ := b.EnsureBotHome(t.Context(), scope)
			original := home.Clone()
			if err := sandbox.BindBotHome(p, "physical-owner", home); err != nil {
				t.Fatal(err)
			}
			home.Scope.UserID = "client-claimed-owner"
			c := p.GetOrCreate("physical-owner")
			if err := c.EnsureReady("physical-owner"); err != nil {
				t.Fatal(err)
			}
			db := &DockerBackend{}
			args := db.dockerRunArgs(b.spec, "owned-container", "base", "/owned/upper")
			for _, want := range []string{
				"--label=astonish.org=" + scope.OrgID,
				"--label=astonish.team=" + scope.TeamID,
				"--label=" + botHomeScopeLabel + "=" + scope.Key(),
				"--mount=type=volume,source=" + original.Ref + ",target=" + botHomeMount + ",volume-nocopy",
			} {
				if !slices.Contains(args, want) {
					t.Errorf("missing structural ownership argument %q", want)
				}
			}
			if b.spec.UserID != scope.UserID || !b.spec.BotHome.Equal(original) {
				t.Fatal("retained home identity changed")
			}
			if org != "" && !slices.Contains(args, "--network="+orgNetworkName(org)) {
				t.Fatal("missing tenant network")
			}
			if org == "" {
				for _, arg := range args {
					if len(arg) >= 10 && arg[:10] == "--network=" {
						t.Fatal("personal session acquired tenant network")
					}
				}
			}
		})
	}
}
