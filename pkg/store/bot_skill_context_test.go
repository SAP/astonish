package store

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

type skillContextRepo struct {
	BotStore
	runs []BotRun
}

func (r skillContextRepo) ListRuns(context.Context, BotID, BotPage) ([]BotRun, error) {
	return r.runs, nil
}

func TestBotPriorSkillTask(t *testing.T) {
	now := time.Now()
	current := BotRun{ID: uuid.New(), BotID: uuid.New(), CreatedAt: now}
	prior := BotRun{ID: uuid.New(), BotID: current.BotID, Trigger: TriggerManual, CreatedAt: now.Add(-time.Minute), Status: RunSucceeded}
	prior.Report.Skill = &BotSkillReceipt{BotID: current.BotID, RunID: prior.ID, SkillID: uuid.New(), Kind: BotSkillCreated}
	for _, tc := range []struct {
		name   string
		change func(*BotRun)
		extra  bool
		want   bool
	}{
		{"committed", func(*BotRun) {}, false, true},
		{"failed", func(r *BotRun) { r.Status = RunFailed }, false, false},
		{"unbound", func(r *BotRun) { r.Report.Skill = nil }, false, false},
		{"foreign", func(r *BotRun) { r.BotID = uuid.New() }, false, false},
		{"foreign-receipt", func(r *BotRun) { r.Report.Skill.BotID = uuid.New() }, false, false},
		{"rejected", func(r *BotRun) { r.Report.Skill.Kind = BotSkillRejected }, false, false},
		{"newer-unbound-blocks-older", func(r *BotRun) { r.Report.Skill = nil; r.CreatedAt = now.Add(-time.Second) }, true, false},
		{"distinct-tasks-ambiguous", func(r *BotRun) {
			r.ID = uuid.New()
			r.Report.Skill.RunID = r.ID
			r.Report.Skill.SkillID = uuid.New()
			r.CreatedAt = now.Add(-time.Second)
		}, true, false},
		{"ambiguous", func(r *BotRun) { r.ID = uuid.New() }, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := prior
			receipt := *prior.Report.Skill
			r.Report.Skill = &receipt
			tc.change(&r)
			runs := []BotRun{r}
			if tc.extra {
				runs = append(runs, prior)
			}
			got, err := BotPriorSkillTask(t.Context(), skillContextRepo{runs: runs}, current)
			if err != nil || (got != nil) != tc.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

// Context availability is independent of consent words; only orientation selects a task.
func TestBotSkillFollowup(t *testing.T) {
	now := time.Now()
	current := BotRun{ID: uuid.New(), BotID: uuid.New(), CreatedAt: now}
	prior := BotRun{ID: uuid.New(), BotID: current.BotID, Trigger: TriggerManual, CreatedAt: now.Add(-time.Minute), Status: RunSucceeded}
	prior.Report.Skill = &BotSkillReceipt{BotID: current.BotID, RunID: prior.ID, SkillID: uuid.New(), Kind: BotSkillCreated}
	for _, text := range []string{"yes", " YES! ", "schedule it", "yes please.", "hi", "AI News", "Report earnings", "schedule weather instead"} {
		current.Invocation = text
		got, err := BotPriorSkillTask(t.Context(), skillContextRepo{runs: []BotRun{prior}}, current)
		if err != nil || got == nil || got.ID != prior.ID {
			t.Errorf("context keyword-gated for %q: %v", text, err)
		}
	}
}
