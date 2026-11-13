package launcher

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type discoveryListStore struct {
	store.BotStore
	store.BotSkillStore
	calls []store.BotSkillPage
	botID store.BotID
}

func (s *discoveryListStore) ListSkills(_ context.Context, id store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
	if id != s.botID || p.IncludeArchived {
		return nil, store.ErrBotForbidden
	}
	s.calls = append(s.calls, p)
	start := 0
	if p.After != "" {
		var err error
		start, err = strconv.Atoi(p.After)
		if err != nil {
			return nil, store.ErrBotInvalid
		}
	}
	out := &store.BotSkillList{}
	for i := start; i < min(25, start+p.Limit); i++ {
		out.Skills = append(out.Skills, store.BotSkill{ID: uuid.New(), BotID: id, Title: fmt.Sprintf("method-%d", i), HeadRevision: 1, Status: store.BotSkillActive})
	}
	if start+p.Limit < 25 {
		out.Next = strconv.Itoa(start + p.Limit)
	}
	return out, nil
}

type discoveryWorkService struct{ bot.BotWorkServices }

func TestBotSkillListPagination(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &discoveryListStore{BotStore: f.services.Bots, botID: f.req.Bot.ID}
	if f.req.Bot.Scope == store.BotScopePersonal {
		repo.BotStore = f.services.PersonalBots
		f.services.PersonalBots = repo
	} else {
		f.services.Bots = repo
	}
	owner := bot.WithBotWorkServices(store.WithServices(f.ctx, f.services), discoveryWorkService{})
	binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	ts, err := newBotSkillTools(binding, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	var list botRunnableTool
	for _, tool := range ts {
		if tool.Name() == "bot_skill_list" {
			list = tool.(botRunnableTool)
		}
	}
	if list == nil {
		t.Fatal("missing list tool")
	}
	ctx := optionalKnowledgeContext{knowledgeCtx(f)}
	first, err := list.Run(ctx, map[string]any{})
	if err != nil || first["status"] != "ok" {
		t.Fatalf("first: %v %v", first, err)
	}
	page := first["result"].(map[string]any)
	if len(page["skills"].([]any)) != 20 || page["next"] != "20" {
		t.Fatalf("first page: %v", page)
	}
	second, err := list.Run(ctx, map[string]any{"after": page["next"], "limit": 10})
	if err != nil || second["status"] != "ok" {
		t.Fatalf("second: %v %v", second, err)
	}
	page = second["result"].(map[string]any)
	if len(page["skills"].([]any)) != 5 || page["next"] != nil || page["skills"].([]any)[4].(map[string]any)["title"] != "method-24" {
		t.Fatalf("later methods unavailable: %v", page)
	}
	for _, limit := range []int{-1, 101} {
		got, err := list.Run(ctx, map[string]any{"limit": limit})
		if err != nil || got["status"] != "invalid_request" {
			t.Fatalf("bad limit: %v %v", got, err)
		}
	}
	if len(repo.calls) != 2 || repo.calls[0].Limit != 20 || repo.calls[1].After != "20" || repo.calls[1].Limit != 10 {
		t.Fatalf("pagination not forwarded: %+v", repo.calls)
	}
}
