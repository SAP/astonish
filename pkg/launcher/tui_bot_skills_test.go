package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotSkillBackendScopedReadOnly(t *testing.T) {
	id, skill := uuid.New(), uuid.New()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Query().Get("scope") != "team" || !strings.HasPrefix(r.URL.Path, "/api/studio/bots/"+id.String()+"/skills") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"skills":[],"revisions":[],"runs":[]}`))
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "team")
	ctx := context.Background()
	if _, err := b.BotSkills(ctx, store.BotSkillPage{}); err == nil {
		t.Fatal("unselected read allowed")
	}
	b.selected = &store.Bot{ID: id}
	if _, err := b.BotSkills(ctx, store.BotSkillPage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BotSkillRevisions(ctx, skill, store.BotSkillPage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BotSkillRuns(ctx, skill, store.BotSkillPage{}); err != nil {
		t.Fatal(err)
	}
	b.DetachBot()
	if _, err := b.BotSkillRuns(ctx, skill, store.BotSkillPage{}); err == nil {
		t.Fatal("detached read allowed")
	}
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
}
