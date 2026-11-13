package launcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// BotMemoryTools retains Chat's argument/result types while binding all access
// to the Bot's session-prefiltered service. No owner-wide merger/search fallback.
func BotMemoryTools(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, error) {
	if scope == nil || scope.Services == nil {
		return nil, store.ErrBotUnsupported
	}
	if scope.Services.Memory == nil {
		return nil, nil
	} // no owner-wide fallback
	repo := scope.Services.Bots
	if r.Bot.Scope == store.BotScopePersonal {
		repo = scope.Services.PersonalBots
	}
	memory, err := store.NewBotRunMemoryService(repo, scope.Services.Memory, r.Run.Lease, r.Bot.Scope)
	if err != nil {
		return nil, err
	}
	save, err := functiontool.New(functiontool.Config{Name: "memory_save", Description: "Save durable facts as inferred knowledge in this Bot's memory, not verified evidence. overwrite only changes unattributed legacy sections in this run; attributed claims require an owner correction."}, func(ctx tool.Context, a tools.MemorySaveArgs) (tools.MemorySaveResult, error) {
		if strings.TrimSpace(a.Category) == "" || strings.TrimSpace(a.Content) == "" {
			return tools.MemorySaveResult{}, store.ErrBotInvalid
		}
		content := a.Content
		if redactor := credentials.RedactorFromContext(ctx); redactor != nil {
			content, _ = redactor.Placeholderize(content)
		}
		category := a.Category
		if kind := strings.ToLower(strings.TrimSpace(a.Kind)); kind != "" {
			category = kind + "/" + category
		}
		if a.Overwrite {
			rows, e := memory.ListBySession(ctx, r.Run.SessionID.String())
			if e != nil {
				return tools.MemorySaveResult{}, e
			}
			for _, row := range rows {
				if row.Category == category {
					if row.Attribution != nil && row.Snippet != content {
						return tools.MemorySaveResult{}, fmt.Errorf("%w: attributed memory requires an owner correction; the original claim was not changed", store.ErrBotConflict)
					}
					if e = memory.Update(ctx, row.ID, content, category); e != nil {
						return tools.MemorySaveResult{}, e
					}
					return tools.MemorySaveResult{Saved: true, Message: "Updated Bot memory"}, nil
				}
			}
		}
		e := memory.Add(ctx, store.MemoryEntry{Content: content, Category: category, SessionID: r.Run.SessionID.String(), CreatedBy: r.Bot.OwnerID.String(), Attribution: &store.MemoryAttribution{BotID: r.Bot.ID, RunID: r.Run.ID, Source: "agent-tool:" + ctx.FunctionCallID(), ObservedAt: time.Now().UTC(), Status: store.MemoryKnowledgeInferred}})
		return tools.MemorySaveResult{Saved: e == nil, Message: "Saved to Bot memory"}, e
	})
	if err != nil {
		return nil, err
	}
	search, err := functiontool.New(functiontool.Config{Name: "memory_search", Description: "Search this Bot's memory. Use the returned path with memory_get."}, func(ctx tool.Context, a tools.MemorySearchArgs) (tools.PlatformMemorySearchResult, error) {
		if strings.TrimSpace(a.Query) == "" {
			return tools.PlatformMemorySearchResult{}, store.ErrBotInvalid
		}
		limit := a.MaxResults
		if limit <= 0 {
			limit = 6
		}
		if limit > 100 {
			limit = 100
		}
		rows, e := memory.Search(ctx, a.Query, limit, 0)
		if e != nil {
			return tools.PlatformMemorySearchResult{}, e
		}
		if rows == nil {
			rows = []store.MemorySearchResult{}
		}
		for i := range rows {
			rows[i].Path = rows[i].ID
		}
		return tools.PlatformMemorySearchResult{Results: rows, Count: len(rows)}, nil
	})
	if err != nil {
		return nil, err
	}
	get, err := functiontool.New(functiontool.Config{Name: "memory_get", Description: "Read lines of a Bot memory using its search result path (memory ID)."}, func(ctx tool.Context, a tools.MemoryGetArgs) (tools.MemoryGetResult, error) {
		row, e := memory.Get(ctx, a.Path)
		if e != nil {
			return tools.MemoryGetResult{}, e
		}
		if row == nil || !store.IsCurrentBotMemory(*row) {
			return tools.MemoryGetResult{}, store.ErrBotNotFound
		}
		lines := strings.Split(row.Snippet, "\n")
		from := a.From
		if from <= 0 {
			from = 1
		}
		count := a.Lines
		if count <= 0 {
			count = 50
		}
		start := min(from-1, len(lines))
		end := start + min(count, len(lines)-start)
		return tools.MemoryGetResult{Path: a.Path, Content: strings.Join(lines[start:end], "\n"), From: from, To: end, TotalLines: len(lines)}, nil
	})
	if err != nil {
		return nil, err
	}
	del, err := functiontool.New(functiontool.Config{Name: "memory_delete", Description: "Delete a memory from the current Bot run by exact ID. Prior-run evidence is protected."}, func(ctx tool.Context, a tools.MemoryDeleteArgs) (tools.MemoryDeleteResult, error) {
		if a.Scope != "" && a.Scope != string(r.Bot.Scope) {
			return tools.MemoryDeleteResult{}, store.ErrBotForbidden
		}
		e := memory.Delete(ctx, a.ID)
		return tools.MemoryDeleteResult{Deleted: e == nil, ID: a.ID, Scope: string(r.Bot.Scope), Message: fmt.Sprintf("Delete Bot memory %s", a.ID)}, e
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{save, search, get, del}, nil
}

// Deliberately shadow generic memory context slots even if a caller supplied an
// owner-wide service container. Only BotMemoryTools may touch memory in a Bot run.
func withoutGenericBotMemory(ctx context.Context) context.Context {
	ctx = store.WithMemoryStore(ctx, nil)
	return store.WithThreeTierSearcher(ctx, nil)
}
