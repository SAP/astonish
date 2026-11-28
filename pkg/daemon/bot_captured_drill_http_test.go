package daemon

import (
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestCapturedScheduledDrillDriftHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, variant := range []string{"suite", "tool", "arguments", "restricted-shell"} {
			t.Run(string(scope)+"/"+variant, func(t *testing.T) {
				runConfiguredServiceAdmissionVariant(t, "drill", scope, "scheduled-exact-resume", variant)
			})
		}
	}
}
