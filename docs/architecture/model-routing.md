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
    Classify(ctx context.Context, prompt string, cc ClassifierContext) ComplexityScore
}

// ClassifierContext provides additional out-of-band signals.
type ClassifierContext struct {
    ToolNames         []string
    ConversationTurns int
    HasPlanMode       bool  // when true, always returns 0.9 (strong)
}

// ComplexityScore is a float64 in [0, 1] where 0 = trivial, 1 = complex.
type ComplexityScore float64
```

### Production Implementation: `MLPClassifier`

A 3-layer MLP (~25K parameters, ~100 KB weights) that outputs `P(needs_strong | prompt)`:

```
query_emb (384-dim, all-MiniLM-L6-v2, L2-normalized)
    │
    ▼
Linear(384→64) + ReLU
Linear(64→32)  + ReLU
Linear(32→1)   + sigmoid
    │
    ▼
ComplexityScore ∈ [0, 1]
```

### Training Data

The classifier was trained on **general-purpose human preference data** — it is not coding-specific:

- **Primary**: `lmarena-ai/arena-human-preference-55k` (Apache-2.0) — 55k real Chatbot Arena battles covering math, writing, coding, translation, reasoning, creative tasks, and more. The key reframing: when a strong-tier model won the battle, `label=1`; when a weak-tier model won, `label=0`; ties are excluded. This collapses all model identities into a single domain-agnostic prompt complexity signal.
- **Augmentation**: `routellm/gpt4_judge_battles` — additional Arena-style battles (~109k embeddings).
- **Synthetic**: ~80 hand-crafted Astonish coding examples (simple shell commands → `label=0`, complex architecture tasks → `label=1`) to sharpen the low end of the scale for tool-use patterns.

The model learns **which prompts are hard**, not **which models are good**. It generalizes to any domain — the coding augmentation only sharpened the bottom of the scale (ensuring "git status" scores near 0), not the general shape.

### Score Distribution

On Arena prompts the score distribution is roughly uniform across [0, 1], not bimodal. With the default thresholds this produces approximately:
- ~32% weak-zone (score < 0.30): greetings, single-word answers, simple lookups, shell commands
- ~48% medium-zone (0.30–0.70): standard Q&A, code review, debugging, explanation, most coding turns
- ~20% strong-zone (score ≥ 0.70): architecture design, complex refactors, multi-step reasoning

In a real Astonish coding session the medium model typically handles ~70% of calls, strong ~17%, and weak ~11% (simple confirmations and short commands).

### Special Cases

- **Empty prompt** → returns `0.1` (weak)
- **`HasPlanMode = true`** → returns `0.9` (strong) — planning tasks always need the strong model
- **Embed error or dimension mismatch** → returns `0.5` (neutral, routes to medium)
- **nil embed function** → returns `0.5` (neutral)

### Weights

Weights are downloaded on first use and stored locally (`~/.config/astonish/models/router_weights.npz`). SHA-256 checksum verification (`pkg/provider/routing/weights_init.go`) is performed on download to ensure integrity.

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

## Dynamic Pricing & Cost Savings

### Pricing Data Source

Model pricing is fetched from OpenRouter's public API (`https://openrouter.ai/api/v1/models`),
which provides per-token costs (USD) for hundreds of models across all major providers.
The endpoint is public — no API key is required.

OpenRouter returns pricing as decimal strings in **USD-per-token** format:

```
"pricing": { "prompt": "0.000003", "completion": "0.000015" }
```

For example, `"0.000003"` = $0.003/1K tokens = $3.00/M tokens.

### Pricing Cache (`pkg/provider/routing/pricing.go`)

Pricing data is stored at:

```
~/.config/astonish/models/pricing_cache.json
```

Structure:

```json
{
  "version": 1,
  "updated_at": "2025-01-15T10:00:00Z",
  "models": {
    "anthropic/claude-opus-4":   { "PromptCost": 0.000015, "CompletionCost": 0.000075 },
    "anthropic/claude-sonnet-4": { "PromptCost": 0.000003, "CompletionCost": 0.000015 },
    "openai/gpt-4o-mini":        { "PromptCost": 0.00000015, "CompletionCost": 0.0000006 }
  }
}
```

Cache behaviour:

| Property | Value |
|---|---|
| Disk TTL | 24 hours |
| In-memory TTL | 1 hour (via OpenRouter's existing `FetchModelsMetadata` cache) |
| Fetch strategy | On demand when first needed; background goroutine — never blocks startup |
| Graceful degradation | If API unreachable and no disk cache, cost display is silently omitted |

### Fuzzy Model Name Matching

Provider-specific names differ from OpenRouter's catalog. The `normalizeName()` function
strips provider prefixes, collapses separators, and lowercases to produce a canonical form:

| Provider Name | Normalized | OpenRouter Match |
|---|---|---|
| `claude-sonnet-4` | `claudesonnet4` | `anthropic/claude-sonnet-4` |
| `gpt-4o-mini` | `gpt4omini` | `openai/gpt-4o-mini` |
| `anthropic--claude-4.6-opus` | `claude46opus` | `anthropic/claude-4.6-opus` |
| `meta-llama/llama-3.1-70b` | `llama3170b` | `meta-llama/llama-3.1-70b` |

Matching priority (first match wins):
1. **Exact normalized match** — normalized name is identical
2. **Substring containment** — one normalized name contains the other (longest overlap preferred)
3. **Longest common prefix** — if > 60% of the shorter name matches

### Cost Savings Calculation

At end-of-turn, the routing summary computes:

```
actual_cost = (strong_calls × strong_prompt_price) +
              (medium_calls × medium_prompt_price) +
              (weak_calls   × weak_prompt_price)

all_strong  = total_calls × strong_prompt_price

savings_pct = (1 − actual_cost / all_strong) × 100%
```

Prompt cost is used for the ratio (completion cost scales proportionally in typical usage).

Example display:

```
Auto routing — 5 calls (20% strong opus, 40% medium sonnet, 40% weak mini) · Saved ~65% vs all-strong
```

The savings line is only shown when `savings_pct > 0.5%` (to suppress noise when all calls are strong).

### API Surface

```go
// PricingCache — created per backend instance, shared across turns.
pc := routing.NewPricingCache(modelsDir)
cost, ok := pc.LookupCost(ctx, "anthropic", "claude-sonnet-4")

// RoutingLLM — pricing injected post-construction via background goroutine.
rLLM.SetPricing(strongCost, mediumCost, weakCost)
if rLLM.HasPricing() {
    savings := rLLM.CostSavingsPct() // 0-100 float64
}

// Standalone helper (also used internally).
savings := routing.CostSavingsPct(strongCost, mediumCost, weakCost, sCalls, mCalls, wCalls)
```
