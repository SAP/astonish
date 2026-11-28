package agent

import (
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/tool"
)

func TestBotWorkerPromptOverridesTrustedBase(t *testing.T) {
	base := &PromptOverrides{SkillIndex: "scoped skills", PinnedToolGroups: []string{"a2a"}, AdditionalTools: []tool.Tool{nil}, ApprovedPlanExecution: true, ApprovedPlanCompleted: true, ApprovedPlanExecutionExplicit: true, PlanMode: true}
	po := NewBotWorkerPromptOverridesFromBase(base, bot.RunRequest{})
	if po.Bot == nil || !po.Bot.worker || po.SkillIndex != base.SkillIndex || !po.PlanMode || !po.ApprovedPlanExecution || !po.ApprovedPlanCompleted || !po.ApprovedPlanExecutionExplicit || len(po.AdditionalTools) != 1 {
		t.Fatalf("trusted worker composition lost envelope: %+v", po)
	}
	po.PinnedToolGroups[0] = "changed"
	po.AdditionalTools = append(po.AdditionalTools, nil)
	if base.Bot != nil || base.PinnedToolGroups[0] != "a2a" || len(base.AdditionalTools) != 1 {
		t.Fatal("worker composition mutated trusted base")
	}
	if plain := NewBotWorkerPromptOverrides(bot.RunRequest{}); plain.Bot == nil || !plain.Bot.worker || plain.ApprovedPlanExecution {
		t.Fatal("legacy constructor contract changed")
	}
}
