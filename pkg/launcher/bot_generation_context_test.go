package launcher

import (
	"context"
	"testing"

	"google.golang.org/adk/model"
)

func TestBotFactoryResolvedContextWindow(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		explicit, resolved, want int
	}{
		{"model metadata", 0, 272000, 272000},
		{"explicit override", 32768, 272000, 32768},
		{"unknown fallback", 0, 200000, 200000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.cfg.ContextWindow = tt.explicit
			called := false
			f.cfg.ResolveContextWindow = func(_ context.Context, llm model.LLM) int {
				if llm == nil {
					t.Fatal("missing scoped model")
				}
				called = true
				return tt.resolved
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			if resources.Agent.Compactor.ContextWindow != tt.want {
				t.Fatalf("context=%d want %d", resources.Agent.Compactor.ContextWindow, tt.want)
			}
			if called != (tt.explicit == 0) {
				t.Fatal("explicit override did not take precedence")
			}
		})
	}
}
