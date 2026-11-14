package launcher

import "testing"

func TestBotSearchFirstLogicalTarget(t *testing.T) {
	f, _, episode := researchFixture(t)
	binding := &botFactoryBinding{research: episode, req: f.req}
	for _, name := range []string{"web_fetch", "http_request", "browser_navigate", "mcp:public/fetch"} {
		if err := binding.checkResearchDetail(f.ctx, f.req.Run.SessionID.String(), name, map[string]any{"url": "https://example.com/guessed"}); err == nil {
			t.Fatalf("pre-search %s allowed", name)
		}
	}
	if err := binding.checkResearchDetail(f.ctx, "child", "web_fetch", map[string]any{"url": "https://example.com"}); err == nil {
		t.Fatal("child inherited parent search")
	}
	if err := binding.checkResearchDetail(f.ctx, f.req.Run.SessionID.String(), "read_file", map[string]any{"path": "local"}); err != nil {
		t.Fatal(err)
	}
}
