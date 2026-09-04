package config

import "testing"

func TestModelRoutingConfig_IsConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  ModelRoutingConfig
		want bool
	}{
		{
			name: "all set via orchestrator tier",
			cfg: ModelRoutingConfig{Orchestrator: RoutingTierConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakProvider: "openai", WeakModel: "gpt-4o-mini",
			}},
			want: true,
		},
		{
			name: "missing weak provider via orchestrator tier",
			cfg: ModelRoutingConfig{Orchestrator: RoutingTierConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakModel: "gpt-4o-mini",
			}},
			want: false,
		},
		{
			name: "missing strong model via orchestrator tier",
			cfg: ModelRoutingConfig{Orchestrator: RoutingTierConfig{
				StrongProvider: "anthropic",
				WeakProvider:   "openai", WeakModel: "gpt-4o-mini",
			}},
			want: false,
		},
		{
			name: "empty",
			cfg:  ModelRoutingConfig{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsConfigured(); got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRoutingConfig_EffectiveThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		want      float64
	}{
		{"zero defaults to 0.5", 0, 0.5},
		{"custom 0.3", 0.3, 0.3},
		{"custom 0.7", 0.7, 0.7},
		{"negative defaults to 0.5", -0.1, 0.5},
		{"above 1 defaults to 0.5", 1.5, 0.5},
		{"exactly 1 defaults to 0.5", 1.0, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ModelRoutingConfig{Orchestrator: RoutingTierConfig{Threshold: tt.threshold}}
			if got := cfg.EffectiveThreshold(); got != tt.want {
				t.Errorf("EffectiveThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsAutoModel(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"auto", true},
		{"Auto", true},
		{"AUTO", true},
		{" auto ", true},
		{"gpt-4o", false},
		{"", false},
		{"automatic", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsAutoModel(tt.input); got != tt.want {
				t.Errorf("IsAutoModel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRoutingTierConfig_IsConfigured(t *testing.T) {
	tests := []struct {
		name string
		tier RoutingTierConfig
		want bool
	}{
		{
			name: "all set",
			tier: RoutingTierConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakProvider: "openai", WeakModel: "gpt-4o-mini",
			},
			want: true,
		},
		{
			name: "missing weak provider",
			tier: RoutingTierConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakModel: "gpt-4o-mini",
			},
			want: false,
		},
		{
			name: "empty",
			tier: RoutingTierConfig{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tier.IsConfigured(); got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRoutingTierConfig_EffectiveThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		want      float64
	}{
		{"zero defaults to 0.5", 0, 0.5},
		{"custom 0.3", 0.3, 0.3},
		{"negative defaults to 0.5", -0.1, 0.5},
		{"above 1 defaults to 0.5", 1.5, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := RoutingTierConfig{Threshold: tt.threshold}
			if got := tier.EffectiveThreshold(); got != tt.want {
				t.Errorf("EffectiveThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRoutingConfig_LegacyMigration(t *testing.T) {
	cfg := ModelRoutingConfig{
		StrongProvider: "anthropic",
		StrongModel:    "claude-sonnet",
		WeakProvider:   "openai",
		WeakModel:      "gpt-4o-mini",
		Threshold:      0.3,
	}

	cfg.Migrate()

	// Orchestrator tier should be populated.
	if cfg.Orchestrator.StrongProvider != "anthropic" {
		t.Errorf("Orchestrator.StrongProvider = %q, want %q", cfg.Orchestrator.StrongProvider, "anthropic")
	}
	if cfg.Orchestrator.StrongModel != "claude-sonnet" {
		t.Errorf("Orchestrator.StrongModel = %q, want %q", cfg.Orchestrator.StrongModel, "claude-sonnet")
	}
	if cfg.Orchestrator.WeakProvider != "openai" {
		t.Errorf("Orchestrator.WeakProvider = %q, want %q", cfg.Orchestrator.WeakProvider, "openai")
	}
	if cfg.Orchestrator.WeakModel != "gpt-4o-mini" {
		t.Errorf("Orchestrator.WeakModel = %q, want %q", cfg.Orchestrator.WeakModel, "gpt-4o-mini")
	}
	if cfg.Orchestrator.Threshold != 0.3 {
		t.Errorf("Orchestrator.Threshold = %v, want %v", cfg.Orchestrator.Threshold, 0.3)
	}

	// Legacy flat fields should be cleared.
	if cfg.StrongProvider != "" {
		t.Errorf("legacy StrongProvider should be cleared, got %q", cfg.StrongProvider)
	}
	if cfg.StrongModel != "" {
		t.Errorf("legacy StrongModel should be cleared, got %q", cfg.StrongModel)
	}
	if cfg.WeakProvider != "" {
		t.Errorf("legacy WeakProvider should be cleared, got %q", cfg.WeakProvider)
	}
	if cfg.WeakModel != "" {
		t.Errorf("legacy WeakModel should be cleared, got %q", cfg.WeakModel)
	}
	if cfg.Threshold != 0 {
		t.Errorf("legacy Threshold should be cleared, got %v", cfg.Threshold)
	}
}

func TestModelRoutingConfig_4Tier(t *testing.T) {
	cfg := ModelRoutingConfig{
		Orchestrator: RoutingTierConfig{
			StrongProvider: "anthropic", StrongModel: "claude-opus",
			WeakProvider: "anthropic", WeakModel: "claude-haiku",
		},
		Task: RoutingTierConfig{
			StrongProvider: "openai", StrongModel: "gpt-4o",
			WeakProvider: "openai", WeakModel: "gpt-4o-mini",
		},
	}

	if !cfg.IsConfigured() {
		t.Error("IsConfigured() = false, want true when Orchestrator tier is fully set")
	}
	if !cfg.TaskConfigured() {
		t.Error("TaskConfigured() = false, want true when Task tier is fully set")
	}
}

func TestModelRoutingConfig_OrchestratorOnly(t *testing.T) {
	cfg := ModelRoutingConfig{
		Orchestrator: RoutingTierConfig{
			StrongProvider: "anthropic", StrongModel: "claude-opus",
			WeakProvider: "anthropic", WeakModel: "claude-haiku",
		},
	}

	if !cfg.IsConfigured() {
		t.Error("IsConfigured() = false, want true when Orchestrator tier is fully set")
	}
	if cfg.TaskConfigured() {
		t.Error("TaskConfigured() = true, want false when Task tier is empty")
	}
}
