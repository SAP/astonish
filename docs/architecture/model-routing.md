# Model Routing Architecture

## Overview

Astonish supports **3-tier Auto model routing** that intelligently routes LLM
calls to one of three model tiers (strong/medium/weak) based on task complexity.
The medium tier is optional — when unconfigured, routing falls back to 2-tier
(strong/weak) operation.

Routing uses a trained MLP classifier (all-MiniLM-L6-v2 embeddings → 3-layer
MLP, 384→64→32→1 sigmoid) with a heuristic fallback classifier. The
classifier produces a complexity score in [0, 1]; two configurable thresholds
(high/low) select the tier:

- Score ≥ high threshold → **strong** model
- Score ≤ low threshold → **weak** model
- Otherwise → **medium** model (or weak if medium is not configured)

## Configuration

```yaml
model_routing:
  strong_provider: anthropic
  strong_model: claude-sonnet-4
  medium_provider: openai
  medium_model: gpt-4o-mini
  weak_provider: openai
  weak_model: gpt-4o-mini
  high_threshold: 0.7
  low_threshold: 0.3
```

All six model fields (strong/medium/weak provider+model) plus the two
thresholds live at the top level of `model_routing`. The medium tier is
optional and can be left empty for 2-tier routing.

### Legacy Migration

Older config formats are automatically migrated via
`ModelRoutingConfig.Migrate()`:

- **Flat 2-model** (pre-4-tier): `strong_provider`/`weak_provider` → kept as-is
- **4-tier orchestrator/task**: orchestrator.strong → strong, orchestrator.weak → medium, task.weak → weak; task.strong is dropped

## Architecture

```
SwappableLLM
  └─ RoutingLLM
       ├─ strong   → claude-sonnet-4       (score ≥ 0.7)
       ├─ medium   → gpt-4o-mini           (0.3 < score < 0.7)
       └─ weak     → gpt-4o-mini           (score ≤ 0.3)

SubAgentManager.TaskLLM → same RoutingLLM (sub-agents share routing)
```

### Key Types

- **`config.ModelRoutingConfig`** — flat config with strong/medium/weak provider+model, high/low thresholds
- **`backend.AutoRoutingConfig`** — TUI backend struct carrying the full config for the model picker
- **`routing.RoutingLLM`** — wraps strong+medium+weak LLMs with a classifier and two thresholds
- **`routing.MLPClassifier`** — trained 3-layer MLP (384→64→32→1) for complexity scoring
- **`routing.HeuristicClassifier`** — keyword/length/tool/depth heuristic scoring (fallback)
- **`routing.ComplexityClassifier`** — interface for pluggable classifiers
- **`routing.RoutingStats`** — atomic counters for per-tier call/token counts
- **`routing.PricingCache`** — OpenRouter pricing with 24h disk cache and fuzzy model matching

### Prompt Classification

`extractLastUserMessage` extracts the actual user-typed input from the LLM
request, skipping:
1. Framework-injected per-turn context (`[Astonish Per-Turn Context` prefix)
2. Injected context parts (AGENTS.md, session state) — picks the shortest
   text part as the user's actual input

The classifier scores this text on a 0–1 complexity scale. The two thresholds
determine tier selection as described above.

## UX

### Model Picker

The `/model` → Auto config screen shows:
1. Strong provider/model (complex tasks)
2. Medium provider/model (moderate tasks, optional)
3. Weak provider/model (simple tasks)
4. High threshold [← →] (default 0.70)
5. Low threshold [← →] (default 0.30)
6. Confirm [Enter]

### Footer

When Auto routing is active:
```
Auto ✦ strong|medium|weak
```

### Routing Badge

Each agent response **and its tool fold** show a routing badge during the live
turn and after session restore:

- 🧠 = strong
- ⚙️ = medium
- ⚡ = weak

### Turn Summary

End-of-turn summary shows per-tier breakdown plus estimated cost savings when
OpenRouter pricing is available:

```
Auto routing — 5 calls (60% strong sonnet, 40% weak mini) · Saved ~45% vs all-strong
```

The savings clause is omitted when pricing is unknown or every call used the
strong model.

## Wiring

1. **Startup restore**: `RunCodeTUI` / `buildCodeBackend` check
   `appConfig.ModelRouting.IsConfigured()`, create `RoutingLLM`, swap into
   `SwappableLLM`, and wire to `SubAgentManager.TaskLLM`
2. **Model picker**: `SetAutoRouting` creates `RoutingLLM`, wires everything,
   and persists config
3. **Turn execution**: `driveTurn` uses the snapshotted `routingLLM` for
   per-call badge emission and end-of-turn summary
4. **Config persistence**: saved to `config.yaml` under `model_routing`

## MLP Weights

Pre-trained weights are stored in `router_weights.npz` (26,753 parameters,
~99 KB). `EnsureRouterWeights` checks for a local copy, falls back to GitHub
Releases download, and verifies SHA-256 integrity on every load.

The NPZ parser includes security hardening: ZIP entry limits, array size caps,
header size limits, integer overflow checks, negative dimension rejection,
Fortran-order rejection, and cross-layer dimension validation.
