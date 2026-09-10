package api

import (
	"context"
	"sync"
	"testing"
	"time"
)

// memoryTitleStore is an in-memory SessionTitleChecker for unit tests.
type memoryTitleStore struct {
	mu     sync.Mutex
	titles map[string]string
}

func newMemoryTitleStore() *memoryTitleStore {
	return &memoryTitleStore{titles: make(map[string]string)}
}

func (m *memoryTitleStore) SetSessionTitle(_ context.Context, sessionID, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.titles[sessionID] = title
	return nil
}

func (m *memoryTitleStore) GetSessionTitle(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.titles[sessionID], nil
}

func TestFallbackTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "short", msg: "Hello world", want: "Hello world"},
		{name: "strips timestamp", msg: "[2026-03-20 14:30:05 UTC]\nFix the login bug", want: "Fix the login bug"},
		{name: "empty", msg: "   ", want: ""},
		{
			name: "truncates at word boundary",
			msg:  "This is a fairly long message that definitely exceeds fifty characters easily",
			want: "This is a fairly long message that definitely...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fallbackTitle(tt.msg)
			if got != tt.want {
				t.Fatalf("fallbackTitle(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

func TestGenerateStudioSessionTitle_LLMErrorUsesFallbackWhenNoProvisional(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	llm := NewMockLLM(ErrorTurn("500", "boom"))

	var gotTitle string
	generateStudioSessionTitle(llm, store, "sess-1", "Debug flaky CI", "", func(title string) {
		gotTitle = title
	})

	if gotTitle != "Debug flaky CI" {
		t.Fatalf("onTitle = %q, want fallback from user message", gotTitle)
	}
	stored, err := store.GetSessionTitle(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "Debug flaky CI" {
		t.Fatalf("stored title = %q, want fallback", stored)
	}
}

func TestGenerateStudioSessionTitle_LLMErrorKeepsProvisional(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	_ = store.SetSessionTitle(context.Background(), "sess-2", "Debug flaky CI")
	llm := NewMockLLM(ErrorTurn("500", "boom"))

	var called bool
	generateStudioSessionTitle(llm, store, "sess-2", "Debug flaky CI", "Debug flaky CI", func(string) {
		called = true
	})

	if called {
		t.Fatal("onTitle should not be called when provisional already covers the failure")
	}
	stored, err := store.GetSessionTitle(context.Background(), "sess-2")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "Debug flaky CI" {
		t.Fatalf("stored title = %q, want provisional unchanged", stored)
	}
}

func TestGenerateStudioSessionTitle_SkipsRedundantUpdate(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	_ = store.SetSessionTitle(context.Background(), "sess-3", "Same Title")
	llm := NewMockLLM(TextTurn("Same Title"))

	var called bool
	generateStudioSessionTitle(llm, store, "sess-3", "Same Title please", "Same Title", func(string) {
		called = true
	})

	if called {
		t.Fatal("onTitle should not fire when refined title equals provisional")
	}
}

func TestGenerateStudioSessionTitle_UpgradesProvisional(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	_ = store.SetSessionTitle(context.Background(), "sess-4", "Tell me about Go testing tools")
	llm := NewMockLLM(TextTurn("Go Testing Tips"))

	var gotTitle string
	generateStudioSessionTitle(llm, store, "sess-4", "Tell me about Go testing tools", "Tell me about Go testing tools", func(title string) {
		gotTitle = title
	})

	if gotTitle != "Go Testing Tips" {
		t.Fatalf("onTitle = %q, want refined title", gotTitle)
	}
	stored, err := store.GetSessionTitle(context.Background(), "sess-4")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "Go Testing Tips" {
		t.Fatalf("stored title = %q, want refined title", stored)
	}
}

func TestSessionNeedsTitle(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	ctx := context.Background()

	if !sessionNeedsTitle(ctx, "s1", true, "hi", store) {
		t.Fatal("new session with message should need title")
	}
	if sessionNeedsTitle(ctx, "s1", true, "hi", nil) {
		t.Fatal("nil setter should not need title")
	}
	if sessionNeedsTitle(ctx, "s1", true, "", store) {
		t.Fatal("empty message should not need title")
	}
	// GetSessionTitle returns "" for missing keys, so existing untitled needs title.
	if !sessionNeedsTitle(ctx, "s1", false, "hi", store) {
		t.Fatal("existing untitled session should need title")
	}
	_ = store.SetSessionTitle(ctx, "s1", "Already Named")
	if sessionNeedsTitle(ctx, "s1", false, "hi", store) {
		t.Fatal("existing titled session should not need title")
	}
}

func TestStartSessionTitle_EmitsProvisionalThenRefine(t *testing.T) {
	t.Parallel()
	store := newMemoryTitleStore()
	cr := newChatRunner("sess-start", "user-1", studioChatAppName, true)
	_ = cr.Subscribe("sub-1")

	llm := NewMockLLM(TextTurn("Polished Title"))
	cr.startSessionTitle(llm, store, "Hello from the very first user message")

	select {
	case <-cr.titleDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for titleDone")
	}

	events := cr.GetHistory()
	var titles []string
	for _, ev := range events {
		if ev.Type != "session_title" {
			continue
		}
		if title, _ := ev.Data["title"].(string); title != "" {
			titles = append(titles, title)
		}
	}
	if len(titles) < 1 {
		t.Fatal("expected at least the provisional session_title")
	}
	if titles[0] != "Hello from the very first user message" {
		t.Fatalf("provisional title = %q, want user message", titles[0])
	}
	if len(titles) < 2 {
		t.Fatalf("expected refine session_title after provisional, got %v", titles)
	}
	if titles[1] != "Polished Title" {
		t.Fatalf("refined title = %q, want Polished Title", titles[1])
	}
	stored, err := store.GetSessionTitle(context.Background(), "sess-start")
	if err != nil {
		t.Fatal(err)
	}
	if stored != "Polished Title" {
		t.Fatalf("stored title = %q, want Polished Title", stored)
	}
}
