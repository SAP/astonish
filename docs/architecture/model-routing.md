# Model Routing Architecture

## Overview

Astonish supports **4-tier Auto routing** with two independent routing levels, each with a strong/weak model pair and a separate complexity threshold:

- **Orchestrator tier** (main agent loop): routes between a premium model (e.g., Claude Opus) and a mid-tier model (e.g., Claude Sonnet) based on prompt complexity
- **Task tier** (sub-agents via `delegate_tasks`): routes between a mid-tier model and a cheap model (e.g., GPT-4o-mini) for parallelized sub-tasks

The Task tier is optional. When unconfigured, sub-agents inherit the orchestrator-level LLM.

## Configuration

```yaml
model_routing:
  orchestrator:
    strong_provider: anthropic
    strong_model: claude-opus-4-5
    weak_provider: anthropic
    weak_model: claude-sonnet-4
    threshold: 0.5
  task:
    strong_provider: anthropic
    strong_model: claude-sonnet-4
    weak_provider: openai
    weak_model: gpt-4o-mini
    threshold: 0.4
```

### Legacy Migration

Flat 2-model configs (pre-4-tier) are automatically migrated to the Orchestrator tier via `ModelRoutingConfig.Migrate()`:

```yaml
# Legacy format (auto-migrated)
model_routing:
  strong_provider: anthropic
  strong_model: claude-opus-4-5
  weak_provider: anthropic
  weak_model: claude-sonnet-4
  threshold: 0.5
```

## Architecture

```
SwappableLLM
  └─ RoutingLLM (tier="orchestrator")
       ├─ strong → claude-opus-4-5
       └─ weak   → claude-sonnet-4

SubAgentManager.TaskLLM
  └─ RoutingLLM (tier="task")
       ├─ strong → claude-sonnet-4
       └─ weak   → gpt-4o-mini
```

### Key Types

- **`config.RoutingTierConfig`** — per-tier config (strong/weak provider+model, threshold)
- **`config.ModelRoutingConfig`** — top-level config with Orchestrator and Task tiers, plus legacy flat fields
- **`backend.AutoRoutingConfig`** — TUI backend struct carrying both tiers for the model picker
- **`routing.RoutingLLM`** — wraps strong+weak LLMs with a classifier; `.Tier` field identifies orchestrator vs task
- **`routing.HeuristicClassifier`** — keyword/length-based complexity scoring (shared by both tiers)
- **`routing.ComplexityClassifier`** — interface for pluggable classifiers (heuristic, MLP, etc.)

### Prompt Classification

`extractLastUserMessage` extracts the actual user-typed input from the LLM request, skipping:
1. Framework-injected per-turn context (Content entries starting with `[Astonish Per-Turn Context`)
2. Injected context parts (AGENTS.md, session state) — picks the shortest text part as the user's actual input

The classifier scores this text on a 0–1 complexity scale. Scores ≥ threshold route to the strong model; below threshold routes to weak.

## UX

### Model Picker

The `/model` → Auto config screen shows 8 lines:
1. Orchestrator Strong (main - complex)
2. Orchestrator Weak (main - simple)
3. Orchestrator Threshold [← →]
4. Task Strong (sub-tasks - complex)
5. Task Weak (sub-tasks - simple)
6. Task Threshold [← →]
7. (blank separator)
8. Confirm [Enter]

### Footer

When both tiers are configured:
```
Auto / orch: opus|sonnet · task: sonnet|luna
```

### Routing Badge

Each agent response shows a routing badge:
- 🧠 = orchestrator strong
- ⚡ = orchestrator weak
- 🔮 = task strong
- 💨 = task weak

### Turn Summary

End-of-turn summary shows per-tier breakdown:
```
Auto routing — Orchestrator: 5 calls (60% strong opus, 40% weak sonnet) · Task: 12 calls (25% strong sonnet, 75% weak luna)
```

## Wiring

1. **Startup restore**: `RunCodeTUI` / `buildCodeBackend` check `appConfig.ModelRouting.IsConfigured()`, create orchestrator RoutingLLM, swap into SwappableLLM, and optionally create task RoutingLLM
2. **Model picker**: `SetAutoRouting` creates both RoutingLLMs, wires task LLM to `SubAgentManager.TaskLLM`
3. **Turn execution**: `driveTurn` wires `taskRoutingLLM` to `chatAgent.SubAgentManager.TaskLLM`
4. **Config persistence**: both tiers saved to `config.yaml` under `model_routing.orchestrator` and `model_routing.task`
5. **Events**: `emitRoutingInfo` sends `routing_tier` field; transcript tracks `LastRoutingTier`
