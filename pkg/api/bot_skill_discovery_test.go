package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type agentDiscoverySkills struct {
	store.BotSkillStore
	list func(context.Context, store.BotID, store.BotSkillPage) (*store.BotSkillList, error)
}

func (s agentDiscoverySkills) ListSkills(ctx context.Context, id store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
	return s.list(ctx, id, p)
}

type agentDiscoveryPriorWork struct {
	*agentExecutionWorkStore
	store.BotStore
	runs []store.BotRun
}

func (s agentDiscoveryPriorWork) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return s.runs, nil
}

func TestBotAgentSkillDiscoveryPriorAssociation(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Run.BotID = req.Bot.ID
	req.Run.CreatedAt = time.Now()
	id, priorID := uuid.New(), uuid.New()
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	repo := agentDiscoveryPriorWork{agentExecutionWorkStore: &agentExecutionWorkStore{state: w}, runs: []store.BotRun{{ID: priorID, BotID: req.Bot.ID, Trigger: store.TriggerManual, Status: store.RunSucceeded, CreatedAt: req.Run.CreatedAt.Add(-time.Hour), Report: store.BotReport{Skill: &store.BotSkillReceipt{BotID: req.Bot.ID, RunID: priorID, SkillID: id, Kind: store.BotSkillSelected}}}}}
	svc := &botAgentWorkService{req: req, resources: &BotChatResources{Work: repo, Skills: agentDiscoverySkills{list: func(context.Context, store.BotID, store.BotSkillPage) (*store.BotSkillList, error) {
		return &store.BotSkillList{}, nil
	}}}}
	got, err := svc.Read(ctx, bot.BotWorkReadRequest{})
	if err != nil || got.PriorSkillID == nil || *got.PriorSkillID != id {
		t.Fatalf("prior association lost: %+v %v", got, err)
	}
	if repo.state.Contract.Skill != nil {
		t.Fatal("hint silently selected method")
	}
}

func TestBotAgentSkillDiscoveryReadOptionalBounded(t *testing.T) {
	for _, scenario := range []string{"heads", "empty", "error", "timeout", "oversized", "foreign", "too-many"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			w.AgentWork.Notes = "useful retained work"
			saved := &agentExecutionWorkStore{state: w}
			skills := agentDiscoverySkills{list: func(c context.Context, id store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
				if id != req.Bot.ID || p.Limit != bot.BotWorkSkillIndexLimit || p.After != "" || p.IncludeArchived {
					t.Fatal("discovery escaped bounded Bot scope")
				}
				if _, ok := c.Deadline(); !ok {
					t.Fatal("missing optional read timeout")
				}
				row := store.BotSkill{ID: uuid.New(), BotID: id, Title: "Weather method", Status: store.BotSkillActive, HeadRevision: 2}
				switch scenario {
				case "empty":
					return &store.BotSkillList{}, nil
				case "timeout":
					<-c.Done()
					return nil, c.Err()
				case "error":
					return nil, errors.New("private storage error")
				case "oversized":
					row.Title = strings.Repeat("x", bot.BotWorkSkillIndexMaxBytes)
				case "foreign":
					row.BotID = uuid.New()
				case "too-many":
					return &store.BotSkillList{Skills: make([]store.BotSkill, bot.BotWorkSkillIndexLimit+1)}, nil
				}
				return &store.BotSkillList{Skills: []store.BotSkill{row}, Next: "next-page"}, nil
			}}
			svc := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved, Skills: skills}}
			got, err := svc.Read(ctx, bot.BotWorkReadRequest{})
			if err != nil || got == nil || got.Work == nil || got.Notes != "useful retained work" {
				t.Fatalf("optional index blocked work: %+v %v", got, err)
			}
			if scenario == "heads" {
				if len(got.SkillIndex) != 1 || got.SkillIndex[0].Title != "Weather method" || got.SkillNext != "next-page" {
					t.Fatalf("missing discovery: %+v", got)
				}
			} else if scenario == "empty" {
				if got.SkillIndexUnavailable || len(got.SkillIndex) != 0 {
					t.Fatalf("empty index: %+v", got)
				}
			} else if !got.SkillIndexUnavailable || len(got.SkillIndex) != 0 || got.SkillNext != "" {
				t.Fatalf("unsafe optional index: %+v", got)
			}
			if saved.state.Contract.Skill != nil || saved.state.Revision != w.Revision {
				t.Fatal("discovery pinned or mutated work")
			}
		})
	}
}
