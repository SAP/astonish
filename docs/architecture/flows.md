# Flow System

## Overview

Flows are YAML-defined agent workflows that execute as deterministic node graphs. Unlike the open-ended ChatAgent where the LLM decides what to do next, a flow specifies a sequence of nodes (LLM calls, tool invocations, conditionals) with explicit transitions between them. Flows are used for repeatable tasks, automated workflows, and as the output format of the distillation system that converts chat traces into reusable procedures.

Flows are executed by the `AstonishAgent` and can also be invoked from ChatAgent via the `run_flow` tool.

## Key Design Decisions

### Why YAML for Flow Definitions

YAML provides a human-readable, version-controllable format that non-programmers can edit. The structure maps naturally to a directed graph: nodes define the processing steps, and a separate `flow` section defines the transitions (including conditional edges). This was chosen over:

- **Visual-only editors**: The Studio provides a visual flow canvas, but YAML is the source of truth. Flows can be created, edited, and shared without the UI.
- **Code-based definitions**: Would limit accessibility and make LLM-powered distillation harder.
- **JSON**: Less readable for the complex nested structures flows require.

### Why Starlark for Conditions

Flow conditions (determining which edge to follow after a node) use **Starlark** -- a Python-like language designed for configuration evaluation. Starlark was chosen because:

- Python-like syntax is familiar to most developers and natural for LLMs to generate.
- Starlark is sandboxed by design -- no file I/O, no network, no imports. This prevents condition expressions from having side effects.
- Conditions access session state via `x["variable_name"]`, providing a clean data-passing interface between nodes.

### Why Distillation From Chat Traces

The ChatAgent records execution traces (tool calls, args, results) during every turn. The distillation system converts these into YAML flows using an LLM:

1. The trace captures what actually happened -- which tools were called, in what order, with what arguments.
2. The FlowDistiller sends the trace to an LLM with the flow schema and available tools list.
3. The LLM generates a YAML flow that reproduces the same behavior deterministically.
4. The flow is validated against the schema and available tools before saving.

This "record and generalize" approach means users can accomplish a task conversationally once, then distill it into a reusable flow. The LLM can parameterize the flow (replacing hardcoded values with state variables) making it applicable beyond the original context.

### Why Intelligent Error Recovery

Flow LLM nodes use a two-tier retry system:

1. **Simple retry**: Re-execute the node with the same input (for transient failures).
2. **Intelligent retry**: An `ErrorRecoveryNode` uses a separate LLM call to analyze the error, decide whether to retry, and suggest modifications. The error context (previous attempts, error messages, tool args) helps the recovery LLM propose a different approach.

This prevents flows from silently failing on the same error repeatedly.

## Architecture

### Flow Definition Structure

```yaml
description: "Deploy application to staging"
nodes:
  - name: check_status
    type: llm
    prompt: "Check the current deployment status using kubectl"
    tools: true
    output_model:
      current_version: "The currently deployed version"
      healthy: "Whether the deployment is healthy (true/false)"

  - name: decide_action
    type: llm
    prompt: "Based on status: {{current_version}}, healthy: {{healthy}}, decide next step"

  - name: deploy
    type: tool
    action: shell_command
    args:
      command: "kubectl apply -f deployment.yaml"

flow:
  - from: START
    to: check_status
  - from: check_status
    to: decide_action
  - from: decide_action
    edges:
      - to: deploy
        condition: "x['healthy'] == True"
      - to: END
        condition: "x['healthy'] == False"
  - from: deploy
    to: END
```

### Node Types

- **`llm`**: Sends a prompt (with `{{variable}}` interpolation from session state) to the LLM. Can optionally enable tools. Output model extracts structured data from the response into state variables.
- **`tool`**: Directly invokes a specific tool with provided args. Supports `raw_tool_output` mapping for extracting specific fields from the tool result into state.
- **`input`**: Pauses execution to collect user input (used in interactive flows). Options can constrain the response.

### Execution State Machine

```
AstonishAgent.Run()
    |
    v
Parse flow: build adjacency map from flow items
    |
    v
Start at "START" node
    |
    v
For each node:
  |
  +-- LLM node: executeLLMNode()
  |     - Interpolate {{variables}} in prompt from session state
  |     - Create llmagent with tools (if enabled) + callbacks
  |     - Execute with retry loop (simple or intelligent)
  |     - Extract output_model fields from response into state
  |     - Handle approval pauses (return, resume on next user message)
  |
  +-- Tool node: executeToolNode()
  |     - Resolve args with {{variable}} interpolation
  |     - Call tool directly
  |     - Map results to state via raw_tool_output
  |
  +-- After node: resolve next node
        - Unconditional: follow `to` edge
        - Conditional: evaluate edges in order, first true wins
        - "END" terminates the flow
```

### Parallel Execution

Nodes can define a `parallel` configuration for data-parallel processing:

```yaml
- name: process_items
  type: llm
  prompt: "Process item: {{item}}"
  parallel:
    forEach: "items"        # State variable containing the list
    as: "item"              # Variable name for each element
    index_as: "item_index"  # Optional index variable
    maxConcurrency: 3       # Limit parallel goroutines
  output_action: "append"   # Aggregate results
```

Each iteration runs independently with its own copy of the state variables. Results are aggregated back into the parent state.

### Flow Registry

The `FlowRegistry` indexes saved flows for lookup by description:

- Stored as `flow_registry.json` in the config directory.
- Each entry records: file path, description, tags, creation time, usage count, last used timestamp.
- The `search_flows` tool queries the registry by natural language description.
- The `run_flow` tool loads and executes a flow by name.

### Distillation Pipeline

```
Chat Execution
    |
    v
ExecutionTrace: records each tool call (name, args, result, success)
    |
    v
/distill command or auto-distill trigger
    |
    v
FlowDistiller.Distill():
  1. Format trace as structured input for the LLM
  2. Include: flow schema, available tools list, trace steps
  3. LLM generates YAML flow (parameterized, with state passing)
  4. Validate against schema (up to 3 retries on validation failure)
  5. Generate knowledge document (markdown description of the flow)
    |
    v
Save flow YAML + register in FlowRegistry + index knowledge doc
```

Auto-distillation triggers after turns where the agent performed substantial work (multiple tool calls producing a reusable pattern). The ChatAgent shows a preview and asks for confirmation before saving.

### MCP Dependencies

Flows can declare required MCP servers:

```yaml
mcp_dependencies:
  - server: github
    tools: [create_pull_request, list_issues]
    source: store
    store_id: github-mcp
```

Before execution, the system checks that required MCP servers are available and the specified tools are registered. Sources: `store` (official MCP store), `tap` (flow repository), or `inline` (embedded configuration).

## Latency vs Studio Chat

Flows often feel slower than Chat for the same `shell_command` / OpenStack curl even though the graph is “streamlined.” Main reasons:

1. **Per-run sandbox isolation** — each flow run gets a fresh session (`flow-run-…` or ADK session ID) and destroys it on cleanup. Chat reuses a warm sandbox across turns of one conversation. **Sandboxes are not shared across flow runs** (would leak `write_file` / shell state).
2. **Distilled shell steps are usually `llm` + `tools: true`** — not deterministic `type: tool` nodes — so each step still pays LLM round-trips (see `flow_distiller.go` shell guidance).
3. **Cold start used to serialize on the first tool** — Chat overlaps `BindSession` with the LLM via `NodeTool.ProcessRequest`. Flows now call `sandbox.WarmFlowSession` after ADK session create so provisioning overlaps the first LLM node **within the same run** only; cleanup still destroys the sandbox.

`type: tool` nodes honor global `AutoApprove` (same as LLM nodes) so headless / `run_flow` do not pause for “Yes” when `AstonishAgent.AutoApprove` is set.

## Key Files

| File | Purpose |
|---|---|
| `pkg/config/yaml_loader.go` | Flow YAML schema: AgentConfig, Node, FlowItem, Edge, ParallelConfig |
| `pkg/agent/astonish_agent.go` | AstonishAgent: flow state machine, node dispatch, approval handling |
| `pkg/agent/node_llm.go` | LLM node execution: retry logic, callback wiring, variable interpolation |
| `pkg/agent/node_tool.go` | Deterministic tool nodes; AutoApprove parity with LLM nodes |
| `pkg/sandbox/flow_warm.go` | Same-run eager BindSession / EnsureReady / PreSeed |
| `pkg/agent/condition_evaluator.go` | Starlark-based condition evaluation for flow edges |
| `pkg/agent/error_recovery.go` | Intelligent error analysis and retry decisions |
| `pkg/agent/flow_distiller.go` | LLM-powered trace-to-YAML flow conversion |
| `pkg/agent/chat_distill.go` | Distill command: trace reconstruction, preview, confirm |
| `pkg/agent/flow_registry.go` | Flow registry: indexing, lookup, usage tracking |
| `pkg/agent/execution_trace.go` | Execution trace recording for distillation |
| `pkg/flowstore/` | Flow YAML storage with GitHub integration |

## Interactions

- **Agent Engine**: ChatAgent invokes flows via `run_flow` tool. AstonishAgent IS the flow executor.
- **Sandbox**: Tool nodes and LLM nodes with tools enabled execute inside sandbox containers via the same node protocol.
- **Credentials**: Flow tool args containing `{{CREDENTIAL:...}}` placeholders are substituted by the same BeforeToolCallback used in ChatAgent. Flow `http_request(credential: ...)` resolves against the request-scoped merged credential store: personal credentials are checked first, then team credentials. Credential names are exact identifiers, not display labels, so `openstack_keystone` and `openstack-keystone` are distinct.
- **Drill**: Drill test suites are a specialized flow type (`type: drill_suite`) with assertions, ready checks, and visual regression.
- **Studio**: The flow canvas provides a visual editor that reads/writes the same YAML format.
- **Memory**: Distilled flows generate knowledge documents indexed in the vector store for future retrieval.
