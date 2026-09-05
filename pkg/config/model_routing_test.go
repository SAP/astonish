package config

import "testing"

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

func TestModelRoutingConfig_IsConfigured_3Tier(t *testing.T) {
	tests := []struct {
		name string
		cfg  ModelRoutingConfig
		want bool
	}{
		{
			name: "all set flat 3-tier",
			cfg: ModelRoutingConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakProvider: "openai", WeakModel: "gpt-4o-mini",
			},
			want: true,
		},
		{
			name: "missing weak provider",
			cfg: ModelRoutingConfig{
				StrongProvider: "anthropic", StrongModel: "claude-sonnet",
				WeakModel: "gpt-4o-mini",
			},
			want: false,
		},
		{
			name: "missing strong model",
			cfg: ModelRoutingConfig{
				StrongProvider: "anthropic",
				WeakProvider:   "openai", WeakModel: "gpt-4o-mini",
			},
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

func TestModelRoutingConfig_EffectiveHighThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		want      float64
	}{
		{"zero defaults to 0.7", 0, 0.7},
		{"custom 0.8", 0.8, 0.8},
		{"custom 0.6", 0.6, 0.6},
		{"negative defaults to 0.7", -0.1, 0.7},
		{"above 1 defaults to 0.7", 1.5, 0.7},
		{"exactly 1 defaults to 0.7", 1.0, 0.7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ModelRoutingConfig{HighThreshold: tt.threshold}
			if got := cfg.EffectiveHighThreshold(); got != tt.want {
				t.Errorf("EffectiveHighThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRoutingConfig_EffectiveLowThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		want      float64
	}{
		{"zero defaults to 0.3", 0, 0.3},
		{"custom 0.2", 0.2, 0.2},
		{"custom 0.4", 0.4, 0.4},
		{"negative defaults to 0.3", -0.1, 0.3},
		{"above 1 defaults to 0.3", 1.5, 0.3},
		{"exactly 1 defaults to 0.3", 1.0, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ModelRoutingConfig{LowThreshold: tt.threshold}
			if got := cfg.EffectiveLowThreshold(); got != tt.want {
				t.Errorf("EffectiveLowThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRoutingConfig_HasMedium(t *testing.T) {
	tests := []struct {
		name string
		cfg  ModelRoutingConfig
		want bool
	}{
		{
			name: "medium set",
			cfg:  ModelRoutingConfig{MediumProvider: "anthropic", MediumModel: "claude-haiku"},
			want: true,
		},
		{
			name: "medium provider only",
			cfg:  ModelRoutingConfig{MediumProvider: "anthropic"},
			want: false,
		},
		{
			name: "medium model only",
			cfg:  ModelRoutingConfig{MediumModel: "claude-haiku"},
			want: false,
		},
		{
			name: "neither set",
			cfg:  ModelRoutingConfig{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasMedium(); got != tt.want {
				t.Errorf("HasMedium() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelRoutingConfig_LegacyFlatMigration(t *testing.T) {
	// Pre-4-tier: flat threshold -> HighThreshold
	cfg := ModelRoutingConfig{
		StrongProvider:  "anthropic",
		StrongModel:     "claude-sonnet",
		WeakProvider:    "openai",
		WeakModel:       "gpt-4o-mini",
		LegacyThreshold: 0.6,
	}

	cfg.Migrate()

	if cfg.HighThreshold != 0.6 {
		t.Errorf("HighThreshold = %v, want 0.6", cfg.HighThreshold)
	}
	if cfg.LegacyThreshold != 0 {
		t.Errorf("LegacyThreshold should be cleared, got %v", cfg.LegacyThreshold)
	}
	// Flat fields should remain (they are the canonical fields now)
	if cfg.StrongProvider != "anthropic" {
		t.Errorf("StrongProvider = %q, want %q", cfg.StrongProvider, "anthropic")
	}
	if cfg.WeakModel != "gpt-4o-mini" {
		t.Errorf("WeakModel = %q, want %q", cfg.WeakModel, "gpt-4o-mini")
	}
}

func TestModelRoutingConfig_Legacy4TierMigration(t *testing.T) {
	// 4-tier: Orchestrator + Task -> 3-tier flat
	cfg := ModelRoutingConfig{
		Orchestrator: &legacyTierConfig{
			StrongProvider: "anthropic", StrongModel: "claude-opus",
			WeakProvider: "anthropic", WeakModel: "claude-haiku",
			Threshold: 0.8,
		},
		Task: &legacyTierConfig{
			StrongProvider: "openai", StrongModel: "gpt-4o",
			WeakProvider: "openai", WeakModel: "gpt-4o-mini",
			Threshold: 0.35,
		},
	}

	cfg.Migrate()

	if cfg.StrongProvider != "anthropic" {
		t.Errorf("StrongProvider = %q, want %q", cfg.StrongProvider, "anthropic")
	}
	if cfg.StrongModel != "claude-opus" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "claude-opus")
	}
	if cfg.MediumProvider != "anthropic" {
		t.Errorf("MediumProvider = %q, want %q", cfg.MediumProvider, "anthropic")
	}
	if cfg.MediumModel != "claude-haiku" {
		t.Errorf("MediumModel = %q, want %q", cfg.MediumModel, "claude-haiku")
	}
	if cfg.WeakProvider != "openai" {
		t.Errorf("WeakProvider = %q, want %q", cfg.WeakProvider, "openai")
	}
	if cfg.WeakModel != "gpt-4o-mini" {
		t.Errorf("WeakModel = %q, want %q", cfg.WeakModel, "gpt-4o-mini")
	}
	if cfg.HighThreshold != 0.8 {
		t.Errorf("HighThreshold = %v, want 0.8", cfg.HighThreshold)
	}
	if cfg.LowThreshold != 0.35 {
		t.Errorf("LowThreshold = %v, want 0.35", cfg.LowThreshold)
	}
	if cfg.Orchestrator != nil {
		t.Errorf("Orchestrator should be nil after migration")
	}
	if cfg.Task != nil {
		t.Errorf("Task should be nil after migration")
	}
}

func TestModelRoutingConfig_Legacy4TierNoTask(t *testing.T) {
	// 4-tier with Orchestrator only (no Task)
	cfg := ModelRoutingConfig{
		Orchestrator: &legacyTierConfig{
			StrongProvider: "anthropic", StrongModel: "claude-opus",
			WeakProvider: "anthropic", WeakModel: "claude-haiku",
			Threshold: 0.75,
		},
	}

	cfg.Migrate()

	if cfg.StrongProvider != "anthropic" {
		t.Errorf("StrongProvider = %q, want %q", cfg.StrongProvider, "anthropic")
	}
	if cfg.StrongModel != "claude-opus" {
		t.Errorf("StrongModel = %q, want %q", cfg.StrongModel, "claude-opus")
	}
	// No task tier: weak gets Orchestrator.WeakProvider/WeakModel
	if cfg.WeakProvider != "anthropic" {
		t.Errorf("WeakProvider = %q, want %q", cfg.WeakProvider, "anthropic")
	}
	if cfg.WeakModel != "claude-haiku" {
		t.Errorf("WeakModel = %q, want %q", cfg.WeakModel, "claude-haiku")
	}
	// No medium since there was no Task tier
	if cfg.HasMedium() {
		t.Errorf("HasMedium() should be false when only Orchestrator was present")
	}
	if cfg.HighThreshold != 0.75 {
		t.Errorf("HighThreshold = %v, want 0.75", cfg.HighThreshold)
	}
	if cfg.Orchestrator != nil {
		t.Errorf("Orchestrator should be nil after migration")
	}
}
