package launcher

import (
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotRoutineReadMandateScopedWorkBaseline(t *testing.T) {
	ref := store.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OrgSlug: "org", TeamSlug: "team", OwnerID: uuid.NewString(), Revision: 1}
	p, err := NewBotRoutineReadMandatePolicy(&store.Services{}, config.DefaultBotMandateConfig())(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, c := range p.Constraints {
		if c.Action == execution.CapabilityToolExecute {
			for _, r := range c.Resources {
				allowed[r] = true
			}
		}
	}
	for _, name := range []string{botWorkReadTool, botWorkUpdateTool, botEvidenceReadTool, botCheckWorkTool, "bot_skill_list", "bot_skill_read", "bot_skill_use", "bot_skill_save"} {
		if !allowed[name] {
			t.Fatalf("scoped capability missing: %s", name)
		}
	}
	for _, name := range []string{"schedule_job", "shell_command", "propose_bot_control", "memory_save", "*"} {
		if allowed[name] {
			t.Fatalf("work baseline grants unrelated authority: %s", name)
		}
	}
}
