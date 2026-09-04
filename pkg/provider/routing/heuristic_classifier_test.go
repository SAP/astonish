package routing

import (
	"strings"
	"testing"
)

func TestHeuristicClassifier(t *testing.T) {
	c := &HeuristicClassifier{}

	tests := []struct {
		name    string
		prompt  string
		ctx     ClassifierContext
		minimum float64
		maximum float64
	}{
		{
			name:    "simple greeting",
			prompt:  "hello",
			ctx:     ClassifierContext{},
			minimum: 0.0,
			maximum: 0.35,
		},
		{
			name:    "complex refactoring request",
			prompt:  "Refactor the authentication system with security audit and migration plan for the distributed services",
			ctx:     ClassifierContext{ConversationTurns: 5},
			minimum: 0.55,
			maximum: 1.0,
		},
		{
			name:    "plan mode boosts score",
			prompt:  "ok",
			ctx:     ClassifierContext{HasPlanMode: true},
			minimum: 0.30,
			maximum: 1.0,
		},
		{
			name:    "empty prompt",
			prompt:  "",
			ctx:     ClassifierContext{},
			minimum: 0.0,
			maximum: 0.35,
		},
		{
			name:    "long prompt without keywords",
			prompt:  strings.Repeat("lorem ipsum dolor sit amet ", 100),
			ctx:     ClassifierContext{},
			minimum: 0.25,
			maximum: 0.55,
		},
		{
			name:    "simple prompt with complex tools",
			prompt:  "show me the files",
			ctx:     ClassifierContext{ToolNames: []string{"announce_plan", "read_file"}},
			minimum: 0.20,
			maximum: 0.50,
		},
		{
			name:    "simple read request",
			prompt:  "list the files in this directory",
			ctx:     ClassifierContext{ToolNames: []string{"read_file", "find_files"}},
			minimum: 0.0,
			maximum: 0.30,
		},
		{
			name:    "deep conversation with medium keywords",
			prompt:  "implement the new feature based on our discussion",
			ctx:     ClassifierContext{ConversationTurns: 12},
			minimum: 0.44,
			maximum: 0.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := float64(c.Classify(tt.prompt, tt.ctx))
			if score < tt.minimum || score > tt.maximum {
				t.Errorf("Classify() = %.3f, want [%.3f, %.3f]", score, tt.minimum, tt.maximum)
			}
		})
	}
}

func TestLengthScore(t *testing.T) {
	if s := lengthScore(""); s != 0.0 {
		t.Errorf("empty = %f, want 0.0", s)
	}
	if s := lengthScore(strings.Repeat("x", 50)); s != 0.0 {
		t.Errorf("50 chars = %f, want 0.0", s)
	}
	if s := lengthScore(strings.Repeat("x", 200)); s != 0.0 {
		t.Errorf("200 chars = %f, want 0.0", s)
	}
	if s := lengthScore(strings.Repeat("x", 5000)); s != 1.0 {
		t.Errorf("5000 chars = %f, want 1.0", s)
	}
	mid := lengthScore(strings.Repeat("x", 2100))
	if mid < 0.4 || mid > 0.6 {
		t.Errorf("2100 chars = %f, want ~0.5", mid)
	}
}

func TestKeywordScore(t *testing.T) {
	if s := keywordScore("refactor the code"); s != 1.0 {
		t.Errorf("high keyword = %f, want 1.0", s)
	}
	if s := keywordScore("implement the feature"); s != 0.6 {
		t.Errorf("medium keyword = %f, want 0.6", s)
	}
	if s := keywordScore("show me the output"); s != 0.2 {
		t.Errorf("low keyword = %f, want 0.2", s)
	}
	if s := keywordScore("foobar bazqux"); s != 0.4 {
		t.Errorf("no keyword = %f, want 0.4", s)
	}
}

func TestToolScore(t *testing.T) {
	if s := toolScore(nil); s != 0.3 {
		t.Errorf("nil tools = %f, want 0.3", s)
	}
	if s := toolScore([]string{"announce_plan"}); s != 0.8 {
		t.Errorf("complex tool = %f, want 0.8", s)
	}
	if s := toolScore([]string{"read_file", "grep_search"}); s != 0.2 {
		t.Errorf("read-only tools = %f, want 0.2", s)
	}
	if s := toolScore([]string{"write_file"}); s != 0.5 {
		t.Errorf("mixed tools = %f, want 0.5", s)
	}
}

func TestDepthScore(t *testing.T) {
	if s := depthScore(ClassifierContext{ConversationTurns: 0}); s != 0.1 {
		t.Errorf("0 turns = %f, want 0.1", s)
	}
	if s := depthScore(ClassifierContext{ConversationTurns: 2}); s != 0.3 {
		t.Errorf("2 turns = %f, want 0.3", s)
	}
	if s := depthScore(ClassifierContext{ConversationTurns: 7}); s != 0.6 {
		t.Errorf("7 turns = %f, want 0.6", s)
	}
	if s := depthScore(ClassifierContext{ConversationTurns: 15}); s != 0.8 {
		t.Errorf("15 turns = %f, want 0.8", s)
	}
	if s := depthScore(ClassifierContext{HasPlanMode: true}); s != 0.9 {
		t.Errorf("plan mode = %f, want 0.9", s)
	}
}
