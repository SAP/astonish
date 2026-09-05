# Model Routing Architecture

## Overview

Astonish supports **3-tier Auto routing** with a single shared model pool used by both the main agent (orchestrator) and all sub-agents (spawned via `delegate_tasks`). The MLP classifier's continuous sigmoid score (0.0–1.0) is split into three ranges using two configurable thresholds to select among strong, medium, and weak models.

```
SwappableLLM
  └─ RoutingLLM (shared by orchestrator + sub-agents)
       ├─ strong → e.g. claude-opus-4-5     (score ≥ high_threshold, default 0.70)
       ├─ medium → e.g. claude-sonnet-4     (low_threshold ≤ score < high_threshold)
       └─ weak   → e.g. gpt-4o-mini         (score < low_threshold, default 0.30)
```

Score ranges (default thresholds):
- `[0.70, 1.0]` → **strong** (complex reasoning, architecture, refactoring)
- `[0.30, 0.70)` → **medium** (standard tasks, code generation, explanations)
- `[0.00, 0.30)` → **weak** (simple queries, greetings, clarifications)

## Key Types

### `ModelRoutingConfig` (`pkg/config/app_config.go`)

Flat configuration struct persisted in `config.yaml` under `model_routing:`:

```go
type ModelRoutingConfig struct {
    StrongProvider string  `yaml:"strong_provider,omitempty"`
    StrongModel    string  `yaml:"strong_model,omitempty"`
    MediumProvider string  `yaml:"medium_provider,omitempty"`
    MediumModel    string  `yaml:"medium_model,omitempty"`
    WeakProvider   string  `yaml:"weak_provider,omitempty"`
    WeakModel      string  `yaml:"weak_model,omitempty"`
    HighThreshold  float64 `yaml:"high_threshold,omitempty"`
    LowThreshold   float64 `yaml:"low_threshold,omitempty"`
    // Legacy fields (auto-migrated on load)
    Orchestrator *legacyTierConfig `yaml:"orchestrator,omitempty"`
    Task         *legacyTierConfig `yaml:"task,omitempty"`
    LegacyThreshold float64        `yaml:"threshold,omitempty"`
}
```

Key methods:
- `IsConfigured()` — returns true when strong+weak providers are set (medium is optional)
- `HasMedium()` — returns true when medium provider+model are both set
- `EffectiveHighThreshold()` — returns `HighThreshold` or default 0.70
- `EffectiveLowThreshold()` — returns `LowThreshold` or default 0.30
- `Migrate()` — converts legacy 4-tier or pre-4-tier configs to the flat 3-model format

### `RoutingLLM` (`pkg/provider/routing/routing_llm.go`)

Wraps three `model.LLM` instances and selects among them at call time:

```go
type RoutingLLM struct {
    strong     model.LLM
    medium     model.LLM  // nil → medium-range scores fall back to weak
    weak       model.LLM
    classifier ComplexityClassifier
    highThreshold float64
    lowThreshold  float64
    StrongName string
    MediumName string
    WeakName   string
    Stats      RoutingStats
    Last       LastRouting
}
```

Constructor:
```go
func NewRoutingLLM(strong, medium, weak model.LLM, classifier ComplexityClassifier,
    highThreshold, lowThreshold float64) *RoutingLLM
```

- `medium` may be nil — when nil, medium-range scores (between `lowThreshold` and `highThreshold`) route to `weak`
- Both thresholds are clamped to `(0, 1)` and must satisfy `lowThreshold < highThreshold`

Selection logic in `GenerateContent`:
```go
switch {
case score >= highThreshold:
    // use strong
case medium != nil && score >= lowThreshold:
    // use medium
default:
    // use weak
}
```

### `RoutingStats` (`pkg/provider/routing/routing_llm.go`)

Thread-safe call counters:
- `RecordStrong()`, `RecordMedium()`, `RecordWeak()`
- `StrongCount()`, `MediumCount()`, `WeakCount()`, `Total()`
- `StrongPct()`, `MediumPct()`, `WeakPct()` — percentages of total
- `Reset()` — zeroes all counters

### `LastRouting` (`pkg/provider/routing/routing_llm.go`)

Thread-safe record of the most recent routing decision:
- `Set(name string, tier string)` — stores model name and tier ("strong"/"medium"/"weak")
- `Get() (name string, tier string)` — retrieves the last decision

### `AutoRoutingConfig` (`pkg/tui/backend/backend.go`)

Carries 3-model routing choices from the TUI into `SetAutoRouting`:

```go
type AutoRoutingConfig struct {
    StrongProvider string
    StrongModel    string
    MediumProvider string  // optional
    MediumModel    string  // optional
    WeakProvider   string
    WeakModel      string
    HighThreshold  float64
    LowThreshold   float64
}
```

`HasMedium()` returns true when both medium fields are non-empty.

## Classifier

The `ComplexityClassifier` interface (`pkg/provider/routing/classifier.go`):

```go
type ComplexityClassifier interface {
    Classify(ctx context.Context, prompt string, context []string) float32
}
```

The production implementation is an MLP (multi-layer perceptron) trained on prompt features. The score is a sigmoid output in `[0.0, 1.0]`:
- Values close to 1.0 → high complexity (prefer strong model)
- Values close to 0.0 → low complexity (prefer weak model)

Weights are downloaded on first use and stored locally. SHA-256 checksum verification (`pkg/provider/routing/weights_init.go`) is performed on download to ensure integrity.

## Launcher Wiring (`pkg/launcher/tui_code.go`)

One `RoutingLLM` is created and shared:

1. **Startup restore**: when `config.yaml` contains `model_routing` with a configured strong+weak pair, `NewBackend` creates a single `RoutingLLM` and:
   - Swaps it into `result.SwappableLLM` (used by the main agent loop)
   - Assigns it to `b.result.ChatAgent.SubAgentManager.TaskLLM` (used by sub-agents)

2. **Interactive configuration** (`SetAutoRouting`): same wiring as above, triggered by the user confirming the model picker auto-config screen.

3. **Routing summary**: after each turn with more than one routing call, a system message is emitted showing the percentage breakdown (e.g., `Auto routing — 5 calls (20% strong opus, 40% medium sonnet, 40% weak mini)`).

## UX

### Model Picker Auto-Config Screen

When the user selects `✦ Auto (smart routing)` from the `/model` provider list, a 7-line configuration screen appears:

```
✦ Auto Model Routing  ↑↓ move  enter select  ← → threshold  esc cancel

› Strong (complex tasks): anthropic / claude-opus-4-5  [Enter]
  Medium (standard tasks): anthropic / claude-sonnet-4  [Enter]
  Weak (simple tasks): openai / gpt-4o-mini  [Enter]
  High Threshold: 0.70  [← →]
  Low Threshold: 0.30  [← →]

  Confirm  [Enter]
```

- Lines 0–2: model slots (Enter opens provider→model picker sub-flow)
- Lines 3–4: threshold adjustments (left/right in 0.05 steps)
- Line 5: blank separator (skipped by navigation)
- Line 6: confirm

Medium is optional. If medium fields are left empty, a 2-model fallback is used (medium-range scores go to weak).

### Routing Badges

Each assistant message shows a routing badge indicating which model tier was used:
- `🧠` — strong model
- `⚙️` — medium model
- `⚡` — weak model

### Footer

When auto routing is active, the footer shows `auto / auto`.

### Routing Info Events

`KindRoutingInfo` events carry:
- `RoutingModel` — the model name used for this turn
- `RoutingTier` — "strong", "medium", or "weak"
- `RoutingStrongName`, `RoutingMediumName`, `RoutingWeakName` — display names for each tier
- `RoutingStrongPct`, `RoutingMediumPct`, `RoutingWeakPct` — cumulative percentage breakdown
- `RoutingTotal` — total call count so far

## Legacy Migration

`ModelRoutingConfig.Migrate()` handles two legacy formats transparently:

### Pre-4-tier flat format
Old configs with flat `strong_provider`/`strong_model`/`weak_provider`/`weak_model` keys deserialize directly into the new struct (same YAML keys). Only the `threshold` key needs migration: it maps to `HighThreshold`, and `LowThreshold` defaults to 0.30.

### 4-tier Orchestrator/Task format
Old configs with nested `orchestrator:` and `task:` sections are migrated as follows:
- `Orchestrator.StrongProvider/Model` → `StrongProvider/Model`
- `Task.StrongProvider/Model` → `MediumProvider/Model` (task-strong becomes medium)
- `Task.WeakProvider/Model` → `WeakProvider/Model` (task-weak becomes the new weak)
- `Orchestrator.Threshold` → `HighThreshold`
- `Task.Threshold` → `LowThreshold`

After migration the `Orchestrator` and `Task` fields are cleared so they are not persisted on the next save.

## Context Propagation

`ComplexityClassifier.Classify(ctx context.Context, ...)` accepts a context to support cancellation. The caller's context (from `GenerateContent`) is passed through so a cancelled turn cancels the classifier call as well.

## Checksum Verification

When downloading MLP weights for the first time, `pkg/provider/routing/weights_init.go` verifies the SHA-256 hash of the downloaded file against a hardcoded expected value before writing it to disk. A checksum mismatch causes the download to fail with an error, preventing use of corrupted or tampered weights.
