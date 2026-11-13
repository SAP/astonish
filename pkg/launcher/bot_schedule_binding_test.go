package launcher

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
)

type implicitScheduleRepo struct {
	store.BotStore
	store.BotSkillStore
	store.BotWorkStore
	work  *store.BotWorkState
	prior []store.BotRun
	skill *store.BotSkill
}

func (s implicitScheduleRepo) LoadWork(context.Context, store.BotLease) (*store.BotWorkState, error) {
	return s.work, nil
}
func (s implicitScheduleRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return s.prior, nil
}
func (s implicitScheduleRepo) GetSkill(_ context.Context, b, id uuid.UUID) (*store.BotSkill, error) {
	if s.skill == nil || s.skill.BotID != b || s.skill.ID != id {
		return nil, store.ErrBotNotFound
	}
	return s.skill, nil
}

func TestBotScheduleImplicitSkillBinding(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			resources, c := scheduleFactory(t, f, repo)
			defer resources.Cleanup()
			id := uuid.New()
			skill := &store.BotSkill{ID: id, BotID: f.req.Bot.ID, Status: store.BotSkillDraft}
			wrapped := implicitScheduleRepo{BotStore: repo, skill: skill, work: &store.BotWorkState{Contract: store.BotWorkContract{Skill: &store.BotSkillInvocation{SkillID: id}}}}
			s := botScheduleTools{request: f.req, repo: wrapped}
			args := botScheduleJobArgs{ScheduleJobArgs: tools.ScheduleJobArgs{Name: "AI News", Mode: "adaptive", Schedule: "0 9 * * *", Instructions: "Fetch articles and copy a long procedure"}, Invocation: "Daily AI & tech news briefing"}
			out, err := s.addSkill(c, args)
			if err != nil {
				t.Fatal(err)
			}
			tr := out.Review.Body.Definition
			if tr == nil || tr.SkillID == nil || *tr.SkillID != id || tr.Instructions != args.Invocation {
				t.Fatalf("not thin binding: %+v", out)
			}
			wrapped.work = nil
			s.repo = wrapped
			out, err = s.addSkill(c, args)
			tr = out.Review.Body.Definition
			if err != nil || tr == nil || tr.SkillID != nil || tr.Invocation != "" || tr.Instructions != args.Instructions {
				t.Fatalf("explicit task requires no saved method: %+v %v", out, err)
			}
			// An invocation-only request still requires a persisted selection.
			args.Instructions = ""
			out, err = s.addSkill(c, args)
			if err != nil || out.Status != "clarification_required" || !strings.Contains(out.Message, "task") {
				t.Fatalf("missing context not actionable: %+v %v", out, err)
			}
			// Neither consent-like text nor the first recent task is a binding.
			// Orientation must persist an unambiguous selection before scheduling.
			for _, text := range []string{"yes", "yes please", "schedule it", "make it daily", "schedule weather instead"} {
				s.request.Instructions = text
				wrapped.prior = []store.BotRun{
					{ID: uuid.New(), BotID: f.req.Bot.ID, Status: store.RunSucceeded, Report: store.BotReport{Skill: &store.BotSkillReceipt{SkillID: id}}},
					{ID: uuid.New(), BotID: f.req.Bot.ID, Status: store.RunSucceeded, Report: store.BotReport{Skill: &store.BotSkillReceipt{SkillID: uuid.New()}}},
				}
				s.repo = wrapped
				out, err = s.addSkill(c, args)
				if err != nil || out.Status != "clarification_required" || out.Review.Body.Definition != nil {
					t.Fatalf("ambiguous %q selected a historical task: %+v %v", text, out, err)
				}
			}
			foreign := uuid.New()
			args.SkillID = &foreign
			args.Instructions = args.Invocation
			if _, err = s.addSkill(c, args); err == nil {
				t.Fatal("foreign explicit ID accepted")
			}
		})
	}
}
