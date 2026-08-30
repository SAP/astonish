package config

import (
	"testing"
	"time"
)

func TestRetrievalTimeoutDefaultsAndOverrides(t *testing.T) {
	var embedding EmbeddingConfig
	if got := embedding.Timeout(); got != 30*time.Second {
		t.Fatalf("default embedding timeout = %s, want 30s", got)
	}
	embedding.TimeoutSeconds = 7
	if got := embedding.Timeout(); got != 7*time.Second {
		t.Fatalf("embedding timeout = %s, want 7s", got)
	}

	var chat ChatConfig
	if got := chat.PreProviderRetrievalTimeout(); got != 10*time.Second {
		t.Fatalf("default pre-provider retrieval timeout = %s, want 10s", got)
	}
	chat.PreProviderRetrievalTimeoutSeconds = 3
	if got := chat.PreProviderRetrievalTimeout(); got != 3*time.Second {
		t.Fatalf("pre-provider retrieval timeout = %s, want 3s", got)
	}
}

func TestGetProviderType(t *testing.T) {
	tests := []struct {
		name         string
		instanceName string
		instance     ProviderConfig
		expectedType string
	}{
		{
			name:         "explicit type field takes precedence",
			instanceName: "my-openai",
			instance: ProviderConfig{
				"type":    "openai",
				"api_key": "sk-...",
			},
			expectedType: "openai",
		},
		{
			name:         "old format - instance name matches known type",
			instanceName: "openai",
			instance: ProviderConfig{
				"api_key": "sk-...",
			},
			expectedType: "openai",
		},
		{
			name:         "old format - anthropic instance name",
			instanceName: "anthropic",
			instance: ProviderConfig{
				"api_key": "sk-ant-...",
			},
			expectedType: "anthropic",
		},
		{
			name:         "old format - gemini instance name",
			instanceName: "gemini",
			instance: ProviderConfig{
				"api_key": "AIza...",
			},
			expectedType: "gemini",
		},
		{
			name:         "new format - custom instance name with type",
			instanceName: "litellm-prod",
			instance: ProviderConfig{
				"type":     "litellm",
				"api_key":  "sk-...",
				"base_url": "http://localhost:4000/v1",
			},
			expectedType: "litellm",
		},
		{
			name:         "new format - anthropic dev instance",
			instanceName: "anthropic-dev",
			instance: ProviderConfig{
				"type":    "anthropic",
				"api_key": "sk-ant-...",
			},
			expectedType: "anthropic",
		},
		{
			name:         "nil instance returns empty",
			instanceName: "openai",
			instance:     nil,
			expectedType: "",
		},
		{
			name:         "empty type and unknown instance name",
			instanceName: "my-custom-name",
			instance: ProviderConfig{
				"api_key": "sk-...",
			},
			expectedType: "",
		},
		{
			name:         "empty type field returns empty",
			instanceName: "my-openai",
			instance: ProviderConfig{
				"type":    "",
				"api_key": "sk-...",
			},
			expectedType: "",
		},
		{
			name:         "xai provider type",
			instanceName: "xai-prod",
			instance: ProviderConfig{
				"type":    "xai",
				"api_key": "xai-...",
			},
			expectedType: "xai",
		},
		{
			name:         "ollama local provider",
			instanceName: "ollama-local",
			instance: ProviderConfig{
				"type":     "ollama",
				"base_url": "http://localhost:11434",
			},
			expectedType: "ollama",
		},
		{
			name:         "sap_ai_core provider",
			instanceName: "sap-prod",
			instance: ProviderConfig{
				"type":          "sap_ai_core",
				"client_id":     "sb-xxx",
				"client_secret": "secret",
			},
			expectedType: "sap_ai_core",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetProviderType(tt.instanceName, tt.instance)
			if result != tt.expectedType {
				t.Errorf("GetProviderType(%q, %v) = %q, expected %q",
					tt.instanceName, tt.instance, result, tt.expectedType)
			}
		})
	}
}

func TestGetProviderType_KnownProviderTypes(t *testing.T) {
	knownTypes := []string{
		"anthropic", "gemini", "groq", "litellm", "lm_studio",
		"ollama", "openai", "openrouter", "poe", "sap_ai_core", "xai",
	}

	for _, providerType := range knownTypes {
		t.Run("old format "+providerType, func(t *testing.T) {
			instance := ProviderConfig{"api_key": "test-key"}
			result := GetProviderType(providerType, instance)
			if result != providerType {
				t.Errorf("GetProviderType(%q, instance) = %q, expected %q",
					providerType, result, providerType)
			}
		})
	}
}

func TestGetDaemonMode(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{"unset returns default", "", DaemonModeDefault},
		{"explicit default", "default", DaemonModeDefault},
		{"api mode", "api", DaemonModeAPI},
		{"worker mode", "worker", DaemonModeWorker},
		{"invalid returns default", "invalid", DaemonModeDefault},
		{"empty string returns default", "", DaemonModeDefault},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("ASTONISH_MODE", tt.envValue)
			} else {
				t.Setenv("ASTONISH_MODE", "")
			}
			result := GetDaemonMode()
			if result != tt.expected {
				t.Errorf("GetDaemonMode() = %q, expected %q (env=%q)", result, tt.expected, tt.envValue)
			}
		})
	}
}

func TestIsDaemonModeAPI(t *testing.T) {
	t.Setenv("ASTONISH_MODE", "api")
	if !IsDaemonModeAPI() {
		t.Error("IsDaemonModeAPI() should return true when ASTONISH_MODE=api")
	}

	t.Setenv("ASTONISH_MODE", "worker")
	if IsDaemonModeAPI() {
		t.Error("IsDaemonModeAPI() should return false when ASTONISH_MODE=worker")
	}
}

func TestIsDaemonModeWorker(t *testing.T) {
	t.Setenv("ASTONISH_MODE", "worker")
	if !IsDaemonModeWorker() {
		t.Error("IsDaemonModeWorker() should return true when ASTONISH_MODE=worker")
	}

	t.Setenv("ASTONISH_MODE", "api")
	if IsDaemonModeWorker() {
		t.Error("IsDaemonModeWorker() should return false when ASTONISH_MODE=api")
	}
}

func TestGetPlatformDSN_FromConfig(t *testing.T) {
	cfg := &PostgresConfig{PlatformDSN: "postgres://config-host:5432/db"}
	got := cfg.GetPlatformDSN()
	if got != "postgres://config-host:5432/db" {
		t.Errorf("GetPlatformDSN() = %q, want config value", got)
	}
}

func TestGetPlatformDSN_FallbackToEnv(t *testing.T) {
	cfg := &PostgresConfig{PlatformDSN: ""}
	t.Setenv("ASTONISH_PLATFORM_DSN", "postgres://env-host:5432/db")
	got := cfg.GetPlatformDSN()
	if got != "postgres://env-host:5432/db" {
		t.Errorf("GetPlatformDSN() = %q, want env value", got)
	}
}

func TestGetPlatformDSN_ConfigPrecedence(t *testing.T) {
	cfg := &PostgresConfig{PlatformDSN: "postgres://config-host:5432/db"}
	t.Setenv("ASTONISH_PLATFORM_DSN", "postgres://env-host:5432/db")
	got := cfg.GetPlatformDSN()
	if got != "postgres://config-host:5432/db" {
		t.Errorf("GetPlatformDSN() = %q, want config value (should take precedence over env)", got)
	}
}

func TestGetPlatformDSN_Empty(t *testing.T) {
	t.Setenv("ASTONISH_PLATFORM_DSN", "")
	cfg := &PostgresConfig{PlatformDSN: ""}
	got := cfg.GetPlatformDSN()
	if got != "" {
		t.Errorf("GetPlatformDSN() = %q, want empty", got)
	}
}

// --- SandboxConfig.BackendKind / IsK8sBackend ---

func TestSandboxConfig_BackendKind(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "incus"},
		{"incus", "incus"},
		{"INCUS", "incus"},
		{"k8s", "k8s"},
		{"K8S", "k8s"},
		{"kubernetes", "k8s"},
		{"Kubernetes", "k8s"},
		{"KUBERNETES", "k8s"},
		{"mock", "mock"},
		{"bogus", "bogus"},
	}
	for _, tt := range tests {
		c := SandboxConfig{Backend: tt.input}
		if got := c.BackendKind(); got != tt.want {
			t.Errorf("BackendKind(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSandboxConfig_IsK8sBackend(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"incus", false},
		{"k8s", true},
		{"kubernetes", true},
		{"K8S", true},
		{"KUBERNETES", true},
		{"mock", false},
	}
	for _, tt := range tests {
		c := SandboxConfig{Backend: tt.input}
		if got := c.IsK8sBackend(); got != tt.want {
			t.Errorf("IsK8sBackend(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSandboxKubernetesConfig_GCDefaults(t *testing.T) {
	cfg := SandboxKubernetesConfig{}
	if got := cfg.K8sGCInterval(); got != time.Hour {
		t.Fatalf("K8sGCInterval() = %s, want 1h", got)
	}
	if got := cfg.K8sLayerGracePeriod(); got != 24*time.Hour {
		t.Fatalf("K8sLayerGracePeriod() = %s, want 24h", got)
	}
	if got := cfg.K8sOrphanUpperGracePeriod(); got != time.Hour {
		t.Fatalf("K8sOrphanUpperGracePeriod() = %s, want 1h", got)
	}
	retention, enabled := cfg.K8sEvictedUpperRetention()
	if !enabled {
		t.Fatalf("K8sEvictedUpperRetention() enabled = false, want true")
	}
	if retention != 14*24*time.Hour {
		t.Fatalf("K8sEvictedUpperRetention() = %s, want 336h", retention)
	}
	if got := cfg.K8sMaxUpperReclaimsPerCycle(); got != 500 {
		t.Fatalf("K8sMaxUpperReclaimsPerCycle() = %d, want 500", got)
	}
}

func TestSandboxKubernetesConfig_GCOverrides(t *testing.T) {
	cfg := SandboxKubernetesConfig{GC: KubernetesGCConfig{
		IntervalMinutes:            15,
		LayerGraceHours:            48,
		OrphanUpperGraceMinutes:    30,
		EvictedUpperRetentionHours: 24,
		MaxUpperReclaimsPerCycle:   25,
	}}
	if got := cfg.K8sGCInterval(); got != 15*time.Minute {
		t.Fatalf("K8sGCInterval() = %s, want 15m", got)
	}
	if got := cfg.K8sLayerGracePeriod(); got != 48*time.Hour {
		t.Fatalf("K8sLayerGracePeriod() = %s, want 48h", got)
	}
	if got := cfg.K8sOrphanUpperGracePeriod(); got != 30*time.Minute {
		t.Fatalf("K8sOrphanUpperGracePeriod() = %s, want 30m", got)
	}
	retention, enabled := cfg.K8sEvictedUpperRetention()
	if !enabled || retention != 24*time.Hour {
		t.Fatalf("K8sEvictedUpperRetention() = %s, %v; want 24h, true", retention, enabled)
	}
	if got := cfg.K8sMaxUpperReclaimsPerCycle(); got != 25 {
		t.Fatalf("K8sMaxUpperReclaimsPerCycle() = %d, want 25", got)
	}
}

func TestSandboxKubernetesConfig_GCDisableEvictedUpperRetention(t *testing.T) {
	cfg := SandboxKubernetesConfig{GC: KubernetesGCConfig{EvictedUpperRetentionDisabled: true}}
	retention, enabled := cfg.K8sEvictedUpperRetention()
	if enabled || retention != 0 {
		t.Fatalf("K8sEvictedUpperRetention() = %s, %v; want 0, false", retention, enabled)
	}
}

func TestSecretScanner_DisabledByDefault(t *testing.T) {
	// Default config (nil Enabled pointer) should mean scanner is disabled.
	cfg := SecurityConfig{}
	if cfg.IsSecretScannerEnabled() {
		t.Fatal("IsSecretScannerEnabled() = true for nil Enabled; want false (opt-in)")
	}

	// Explicitly enabled
	enabled := true
	cfg.SecretScanner.Enabled = &enabled
	if !cfg.IsSecretScannerEnabled() {
		t.Fatal("IsSecretScannerEnabled() = false when Enabled=true; want true")
	}

	// Explicitly disabled
	disabled := false
	cfg.SecretScanner.Enabled = &disabled
	if cfg.IsSecretScannerEnabled() {
		t.Fatal("IsSecretScannerEnabled() = true when Enabled=false; want false")
	}
}
