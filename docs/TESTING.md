# Testing Guide

A comprehensive guide to Astonish's test suite — what exists, why it exists, and how to run it.

---

## Quick Reference

```bash
# Run everything
make test                         # All Go tests
cd web && npm run test            # All frontend tests

# Run specific areas
go test ./pkg/agent/...           # Agent package only
go test ./pkg/api/... -v          # API + integration tests (verbose)
go test ./pkg/tools -run TestFileTree  # Single test
cd web && npx vitest run src/test/scenarios/  # SSE scenario tests only

# Regenerate golden files after intentional prompt changes
go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update

# With race detector (CI)
go test -race ./...

# Frontend with coverage
cd web && npm run test:coverage
```

---

## Test Architecture

The test suite is organized in **four layers**, each catching a different class of bug. The taxonomy below describes **what kind of bug each test catches**; for the orthogonal **build-tag taxonomy** (`unit` / `integration` / `e2e`, run via `make test-unit/integration/e2e`) and for the e2e harness, see [`docs/architecture/testing.md`](architecture/testing.md). When deciding *which* test to add for a new feature or bug, start at the [Choosing the Right Test Type](architecture/testing.md#choosing-the-right-test-type) decision tree.

```
┌─────────────────────────────────────────────────────────────────────┐
│  Layer 4: E2E (Playwright) — future                                 │
│  Real browser + real server + mock LLM. CSS/layout/browser bugs.    │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 3: Prompt Contract Tests (Go)                                │
│  Golden snapshots + structural assertions on system prompt output.  │
│  Catches prompt regressions that break frontend/backend contracts.  │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 2: Backend Integration Tests (Go)                            │
│  Real ChatRunner + mock LLM (no HTTP). SSE event sequences,        │
│  state deltas, tool calls, truncation retry, multi-turn.            │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 1: SSE Scenario Tests (Vitest + React Testing Library)       │
│  Simulated SSE streams from JSON fixtures → real component tree.    │
│  UI regressions, state management, event rendering.                 │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 0: Unit Tests (Go + Vitest)                                  │
│  Individual functions, tools, utilities, API handlers, components.  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Layer 0: Unit Tests

### Go Unit Tests

**102 test files, ~1,345 test functions, 6 fuzz tests, 7 benchmarks**

These test individual packages in isolation. Each package directory contains `*_test.go` files.

#### Key packages and what they test

| Package | Tests | Focus |
|---------|-------|-------|
| `pkg/agent` | 155+ | Agent execution, prompt building, sub-agents, tool index |
| `pkg/api` | 95+ | HTTP handlers, auth, rate limiting, SSE replay, sandbox proxy |
| `pkg/tools` | 170+ | All 58+ built-in tools (shell, file ops, HTTP, credentials, etc.) |
| `pkg/memory` | 100+ | Vector search, BM25, chunking, MEMORY.md, SELF.md |
| `pkg/credentials` | 92+ | Encrypt/decrypt, secret scanning, redaction, credential store |
| `pkg/fleet` | 76 | Fleet YAML parsing, validation, agent roles |
| `pkg/session` | 73 | Session CRUD, compaction, transcript, thread indexing |
| `pkg/drill` | 86 | Test runner, assertions, artifacts, reports |
| `pkg/provider` | 80+ | Provider protocol tests (Anthropic, OpenAI, Vertex, Bedrock, SAP) |
| `pkg/config` | 32 | YAML loading, config merging, fuzz testing |
| `pkg/browser` | 60 | Account store, CAPTCHA detection, VNC handoff, containers |
| `pkg/sandbox` | 67 | Container management, overlay fs, session registry |
| `pkg/scheduler` | 14 | Cron jobs, execution tracking |
| `pkg/channels` | 32 | Telegram, email, command parsing, message routing |

#### Running Go unit tests

```bash
# All tests
go test ./...

# Single package
go test ./pkg/tools

# Single test function
go test ./pkg/tools -run TestFileTree

# Verbose output
go test -v ./pkg/agent -run TestChatAgent

# With race detector
go test -race ./pkg/api/...

# Fuzz tests (run for 30 seconds)
go test ./pkg/config -fuzz FuzzAgentConfig -fuzztime=30s
go test ./pkg/credentials -fuzz FuzzKeyFileParser -fuzztime=30s

# Benchmarks
go test -bench=. ./pkg/agent
go test -bench=. ./pkg/credentials
```

#### Writing Go tests

- Place tests in `*_test.go` in the same package
- Use table-driven tests for multiple cases
- Use `os.MkdirTemp` with `t.Cleanup` for file system tests
- Mock external dependencies (LLM calls, file system, network)
- Follow the pattern in `pkg/tools/shell_command_test.go` for tool tests

### Frontend Unit Tests

**15 test files, ~160 tests across API, components, hooks, and utilities**

Located in `__tests__/` directories alongside their source:

| Location | Tests | Focus |
|----------|-------|-------|
| `web/src/api/__tests__/` | 44 | API client functions (agents, drill, settings, studioChat) |
| `web/src/components/__tests__/` | 33 | Component rendering (App, FlowCanvas, Settings, SetupWizard, StudioChat) |
| `web/src/hooks/__tests__/` | 13 | Custom hooks (useHashRouter) |
| `web/src/utils/__tests__/` | 91 | Utilities (conditionGenerator, flowToYaml, yamlToFlow, formatters) |

#### Running frontend unit tests

```bash
cd web

# All tests (single run)
npm run test

# Watch mode (re-runs on file change)
npm run test:watch

# With V8 coverage report
npm run test:coverage

# Single file
npx vitest run src/api/__tests__/agents.test.ts

# Pattern match
npx vitest run --testNamePattern "should parse YAML"
```

---

## Layer 1: SSE Scenario Tests

**20 test files, 79 scenario tests, 32 JSON fixture files**

These tests simulate real SSE event streams and verify that StudioChat renders correctly. They catch UI regressions, state management bugs, and event handling errors without a running backend.

### Why they exist

StudioChat handles 28 SSE event types, manages 35 state variables, and orchestrates 19 sub-components. A change to how one event is processed can silently break rendering for another feature. These tests protect the full SSE → state → render pipeline.

### Architecture

```
JSON fixture (events) → sseSimulator → ReadableStream → fetch mock
                                                            ↓
                            Real StudioChat component ← connectChat()
                                                            ↓
                            React Testing Library assertions (DOM)
```

The key insight: tests mock at the `fetch()` level, so the real `connectChat()` SSE parsing code runs. This catches bugs in event deserialization and state updates that mocking at a higher level would miss.

### Infrastructure files

| File | Purpose |
|------|---------|
| `web/src/test/helpers/sseSimulator.ts` | Converts JSON fixture arrays into SSE wire format (`ReadableStream`) |
| `web/src/test/helpers/mockFetch.ts` | Routes 15+ URL patterns (chat, sessions, artifacts, fleet, etc.) to mocked responses |
| `web/src/test/helpers/renderChat.tsx` | Wraps StudioChat with mocked APIs; provides `sendMessage()`, `waitForEvent()`, `getMessages()` |
| `web/src/test/setup.ts` | Global setup: jest-dom matchers, matchMedia mock |

### Test categories and files

| Category | File | Tests | What it covers |
|----------|------|-------|----------------|
| Core chat | `core-chat.test.tsx` | 7 | Simple Q&A, multi-chunk streaming, session title updates |
| Core interactions | `core-interactions.test.tsx` | 5 | Session creation, stream abort, connection errors |
| Tool execution | `tool-execution.test.tsx` | 6 | Single/parallel tool calls, artifacts, approval flows |
| Tool interactions | `tool-interactions.test.tsx` | 2 | Expand/collapse tool cards, approval buttons |
| Task delegation | `task-delegation.test.tsx` | 7 | Simple delegation, parallel tasks, task failure |
| Plan tracking | `plan-tracking.test.tsx` | 5 | Announce plan, step transitions, auto-complete |
| Plan interactions | `plan-interactions.test.tsx` | 3 | Step status rendering, partial completion |
| App preview | `app-preview.test.tsx` | 6 | Generated apps, version navigation, save confirmation |
| Error handling | `error-handling.test.tsx` | 4 | Simple errors, structured error info, retry events |
| Distill flow | `distill-flow.test.tsx` | 5 | Flow preview, save actions |
| Downloads | `downloads-artifacts.test.tsx` | 3 | Artifact cards, agent text after artifacts |
| Fleet mode | `fleet-mode.test.tsx` | 2 | Fleet execution progress, redirect events |
| Browser handoff | `browser-handoff.test.tsx` | 1 | VNC iframe, page title, reason text |
| Slash commands | `slash-commands.test.tsx` | 4 | Popup activation, filtering, command execution |
| Session mgmt | `session-management.test.tsx` | 4 | Session list, search, sidebar, new session |
| Session interactions | `session-interactions.test.tsx` | 2 | Delete sessions, new conversations |
| Clipboard | `clipboard-copy.test.tsx` | 1 | Copy agent messages |
| Misc messages | `misc-messages.test.tsx` | 7 | Thinking, system messages, images, usage, mermaid |
| Panel interactions | `panel-interactions.test.tsx` | 4 | Files button, panel toggling |
| Panel management | `panel-management.test.tsx` | 1 | Toolbar panel buttons |

### JSON fixture format

Fixtures live in `web/src/test/fixtures/scenarios/<category>/<name>.json`:

```json
[
  { "event": "session_id", "data": { "session_id": "sess-123" } },
  { "event": "stream_start", "data": {} },
  { "event": "text_delta", "data": { "content": "Hello " } },
  { "event": "text_delta", "data": { "content": "world!" } },
  { "event": "text_done", "data": { "content": "Hello world!" } },
  { "event": "done", "data": {} }
]
```

### Running scenario tests

```bash
cd web

# All scenario tests
npx vitest run src/test/scenarios/

# Single scenario file
npx vitest run src/test/scenarios/tool-execution.test.tsx

# Watch mode on scenarios
npx vitest src/test/scenarios/

# With pattern
npx vitest run --testNamePattern "parallel tool"
```

### Adding a new scenario test

1. Create a JSON fixture in `web/src/test/fixtures/scenarios/<category>/`
2. Create or extend a test file in `web/src/test/scenarios/`
3. Use `renderChat()` + `loadFixture()` + `waitForEvent()` pattern
4. Assert DOM state with React Testing Library queries

---

## Layer 2: Backend Integration Tests

**4 test files, 43 integration tests**

These tests exercise the real `ChatRunner.Run()` pipeline with a mock LLM — no HTTP layer, no network. They validate that the backend produces correct SSE events for every scenario.

### Why they exist

The backend transforms LLM responses into SSE events through several processing stages: ADK iteration → state delta processing → app preview detection → event emission. Integration tests catch bugs in this pipeline that unit tests miss (event ordering, state machine transitions, multi-turn context handling).

### Architecture

```
MockLLM (turn queue) → ChatRunner.Run() → SSE Event Channel → Test assertions
```

The mock LLM (`MockLLM`) implements the `model.LLM` interface with a queue of pre-programmed turns. Each turn specifies whether to stream text, call tools, return errors, etc. Tests collect emitted events and assert on the sequence.

### Infrastructure files

| File | Purpose |
|------|---------|
| `pkg/api/mock_llm_test.go` | `MockLLM`, `TruncationMockLLM`, `BlockingLLM`, turn builders (`TextTurn`, `ToolCallTurn`, `StreamChunk`, etc.) |
| `pkg/api/integration_test.go` | `setupIntegrationTest()`, `runAndCollect()`, `collectEvents()`, assertion helpers (`assertEventSequence`, `assertHasEvent`, etc.) |

### Test files and scenarios

| File | Tests | Scenarios |
|------|-------|-----------|
| `integration_scenarios_test.go` | 16 | Simple text, streaming, tool calls (single/multi-chain), LLM errors, usage metadata, session title, plan suppression, app preview, tool args, event IDs |
| `integration_expanded_test.go` | 22 | State deltas (approval/auto_approved/retry/failure/thinking), stream truncation retry, multi-turn conversation, context cancellation, thought parts, mixed text+tool, GetHistory, subscriber management, app refinement model |

### Key patterns

```go
// Create test environment with mock LLM
env := setupIntegrationTest(t, &MockLLM{
    Turns: []MockTurn{
        TextTurn("Hello from the LLM"),
    },
})

// Run and collect events
events := runAndCollect(t, env, "user prompt", 5*time.Second)

// Assert event sequence
assertEventSequence(t, events, []string{
    "session_id", "stream_start", "text_delta", "text_done", "done",
})
```

### Running integration tests

```bash
# All integration tests
go test ./pkg/api -run 'Integration' -v

# Specific scenario
go test ./pkg/api -run TestIntegration_X03_SingleToolCall -v

# With race detector
go test -race ./pkg/api -run 'Integration'

# Expanded tests (state deltas, truncation, cancellation)
go test ./pkg/api -run 'TestIntegration_X1' -v
```

---

## Layer 3: Prompt Contract Tests

**1 test file, 31 tests, 1 golden file**

These tests ensure the system prompt contains strings that the frontend and backend code depend on via regex matching and parsing.

### Why they exist

The system prompt is a contract between three parties:
1. **The prompt** (tells the LLM what syntax to produce)
2. **The backend** (parses LLM output with regex, e.g. `appPreviewFenceRe` matches `` ```astonish-app ``)
3. **The frontend** (renders specific patterns, e.g. mermaid fences, useAppData calls)

If someone edits the prompt and accidentally removes or renames a string, the backend/frontend regex breaks silently. These tests catch that.

### Golden file snapshot

The golden file captures the **entire** system prompt output with all features enabled:

```
pkg/agent/testdata/system_prompt_golden.txt
```

Any change to the prompt output causes the test to fail with a diff showing exactly what changed. If the change is intentional, regenerate:

```bash
go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update
```

### Structural contract assertions

Beyond the golden file, targeted assertions verify ~60 specific strings that other code depends on:

| Test Group | Contracts Verified |
|------------|-------------------|
| `TestSystemPromptContracts_GenerativeUI` | `astonish-app` fence, `useAppData`/`useAppAction`/`useAppAI`/`useAppState`, dark theme, transparent root, fetch/XHR/axios blocked, credential `@`-syntax, mermaid, React 19, Tailwind v4, Recharts, Lucide |
| `TestSystemPromptContracts_Delegation` | `delegate_tasks`, `announce_plan`, `plan_step`, planning strategy, tool group listing |
| `TestSystemPromptContracts_ToolUse` | `memory_search`/`memory_save`, `http_request` RFC1918 restriction, `search_tools`, `skill_lookup` |
| `TestSystemPromptContracts_Identity` | `browser_request_human`, `email_wait`, `credential store` |
| `TestSystemPromptContracts_Knowledge` | Knowledge Context, Knowledge For This Task, Relevant Tools |
| `TestSystemPromptContracts_Environment` | Workspace dir, timezone, OS |
| `TestSystemPromptContracts_Capabilities` | All 13 capability names, named tool references |
| `TestSystemPromptContracts_DynamicSections` | Channel/scheduler/session hints, custom prompt, behavior instructions |

### Multi-configuration tests

9 tests verify conditional sections appear/disappear correctly:
- Minimal config → no optional sections
- With Catalog → delegation section appears
- With Identity → identity section appears
- With SkillIndex → skill-first rule appears
- With search_tools tool → search_tools guidance appears
- With WebSearch/WebExtract → named tool hints appear
- With FleetSection → fleet section appears
- With MemorySearch → persistent memory capability listed
- With MCPFirstClass (code mode) → MCP tools listed as directly callable (`## MCP Tools (available directly)`); without it (platform) → MCP stays gated behind `search_tools` (`TestSystemPromptBuilder_MCPFirstClass`)
- With BrowserAvailable → browser automation capability listed

### Size regression guards

- **Minimal prompt**: must stay under 5,100 bytes
- **Maximal prompt** (all features): must stay under 10,700 bytes

### Running contract tests

```bash
# All contract tests
go test ./pkg/agent -run 'Golden|Contracts|Conditional|Size' -v

# Just the golden file comparison
go test ./pkg/agent -run TestSystemPromptBuilder_Golden

# Regenerate golden file
go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update
```

---

## Layer 4: E2E Tests

E2E tests bootstrap a complete Astonish platform (fresh DB, real `StudioServer`, real auth, real LLM provider) and run real user journeys against it. They live under `tests/e2e/<feature>/` and run via `make test-e2e`. The current suite has ~35 passing scenarios across chat, sandbox layer chains, MCP install, fleet, and the flow assistant.

This layer is **operational, not future**. For the harness contract (`e2eboot.Bootstrap(t)`), the K8s sandbox infra (`make e2e-k8s-up`), the inspector mode for browsing failed runs (`make test-e2e-inspect` on port 9394), and the package-scoped seeding model, see [`docs/architecture/testing.md`](architecture/testing.md). For the user-journey scenario catalog and `// COVERS:` contract, see [`tests/scenarios/`](../tests/scenarios/).

A future Playwright layer for browser-specific concerns (CSS, layout, real `<iframe>` rendering) is still on the table but not yet implemented.

---

## Specialized Test Types

### Fuzz Tests

Located in `pkg/config/` and `pkg/credentials/`. These find panics and crashes in parsing code by feeding random input:

```bash
# Run for 30 seconds
go test ./pkg/config -fuzz FuzzAgentConfig -fuzztime=30s
go test ./pkg/credentials -fuzz FuzzKeyFileParser -fuzztime=30s
go test ./pkg/credentials -fuzz FuzzStoreOperations -fuzztime=30s

# Run until failure
go test ./pkg/config -fuzz FuzzAgentConfig
```

Crash-reproducing inputs are saved in `testdata/fuzz/` for regression.

### Benchmarks

Located in `pkg/agent/` and `pkg/credentials/`:

```bash
# Agent benchmarks (tokenizer, BM25 indexing)
go test -bench=. ./pkg/agent

# Credential benchmarks (secret redaction with varying text sizes)
go test -bench=. ./pkg/credentials

# With memory allocation stats
go test -bench=. -benchmem ./pkg/agent
```

### Docker/Sandbox Tests

Tests for Docker+Incus sandbox functionality (see `docs/testing-docker-incus.md`):

```bash
# Start E2E test environment
make e2e-up

# Run tests that require containers
go test ./pkg/sandbox/... -tags=integration

# Teardown
make e2e-down
```

---

## Frontend Test Configuration

### `web/vitest.config.ts`

```typescript
{
  test: {
    environment: 'jsdom',
    testTimeout: 15000,          // 15s for SSE streaming scenarios
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      exclude: ['src/test/**', 'src/vite-env.d.ts', 'src/main.tsx']
    }
  }
}
```

### Mocking patterns

Several libraries require mocking in jsdom:

- **`react-markdown` + `remark-gfm`**: ESM-only modules that don't work in jsdom. Mocked with `vi.mock()` to render children as `<div>`.
- **Heavy sub-components** (`HomePage`, `FleetStartDialog`, `FleetTemplatePicker`, `MermaidBlock`): Mocked in scenario test files to reduce complexity and speed up tests.
- **`window.matchMedia`**: Mocked globally in `setup.ts` for components using media queries.
- **`navigator.clipboard`**: Mocked in clipboard test file.

### Test timeouts

The global timeout is 15,000ms (15 seconds) to accommodate SSE streaming scenarios where events arrive over time. Most tests complete in <1 second.

---

## CI Integration

### Pre-commit hook

Runs `golangci-lint` on staged Go files. Tests are NOT run pre-commit (too slow).

### GitHub Actions

| Workflow | What it runs |
|----------|-------------|
| `lint.yml` | `golangci-lint run` |
| `build.yml` | `make build-all` (compiles UI + Go binary) |

### Running the full CI equivalent locally

```bash
# Lint
golangci-lint run
cd web && npm run lint

# Build
make build-all

# Tests
go test ./...
cd web && npm run test
```

---

## Test Counts Summary

| Layer | Files | Tests | Runtime |
|-------|-------|-------|---------|
| Go unit tests | 102 | ~1,345 | ~8s |
| Frontend unit tests | 15 | ~160 | ~3s |
| SSE scenario tests (L1) | 20 | 79 | ~5s |
| Backend integration (L2) | 4 | 43 | ~2.5s |
| Prompt contracts (L3) | 1 | 31 | <0.1s |
| **Total** | **142** | **~1,658** | **~19s** |

---

## Common Tasks

### "I changed a tool — what tests do I run?"

```bash
go test ./pkg/tools -run TestMyToolName -v
go test ./pkg/api -run Integration -v   # If it emits SSE events
```

### "I changed the system prompt — what breaks?"

```bash
go test ./pkg/agent -run 'Golden|Contracts' -v
# If golden fails intentionally:
go test ./pkg/agent -run TestSystemPromptBuilder_Golden -update
```

### "I changed StudioChat event handling"

```bash
cd web && npx vitest run src/test/scenarios/
```

### "I added a new SSE event type"

1. Add a JSON fixture in `web/src/test/fixtures/scenarios/<category>/`
2. Add a scenario test in `web/src/test/scenarios/`
3. Add a backend integration test in `pkg/api/integration_scenarios_test.go`

### "I need to test a new provider"

Look at existing provider tests for the pattern:
- `pkg/provider/anthropic/anthropic_test.go`
- `pkg/provider/openai/openai_test.go`
- `pkg/provider/vertex/protocol_test.go`

### "Tests are flaky with act() warnings"

The SSE scenario tests use `waitFor()` and event-driven assertions to avoid act() warnings. If you see them:
1. Wrap state-changing operations in `act()`
2. Use `await waitFor(() => ...)` instead of synchronous assertions after async operations
3. Add cleanup in `useEffect` return functions

---

## Architecture Decisions

### Why mock at fetch() level, not at the API module level?

SSE scenario tests mock `global.fetch` rather than the `connectChat()` function. This means the real SSE parsing code runs — the same `EventSource`-like parser that processes `text/event-stream` responses in production. This catches bugs in event deserialization that mocking at a higher level would miss.

### Why mock LLM at the interface level, not HTTP?

Backend integration tests inject a `MockLLM` implementing the `model.LLM` interface rather than an HTTP mock server. This is more precise (no serialization overhead), faster (no network), and easier to debug (the turn queue is inspectable).

### Why golden file snapshots?

The system prompt is a contract that three systems depend on (prompt text, backend regex, frontend parsing). A golden file captures the entire output, so ANY change — even a typo fix — shows up as a diff. This forces developers to acknowledge prompt changes explicitly with `-update`, preventing accidental regressions.

A canonical example of *why this matters* is the **Inline Report Rendering Contract** in `AGENTS.md` (search for "Inline Report Rendering Contract"). Two prior commits (`b5310ae`, `ee2d47d`) tried to "fix" the report-fence gate by widening it; both broke other features. The defense is a prompt-contract test plus an SSE scenario test — not a relaxation of the gate. When tempted to widen a rendering gate, add a contract test instead.

### Why JSON fixtures instead of inline event arrays?

Fixtures are the single source of truth for event sequences. They can be:
- Reviewed independently of test logic
- Shared between tests that need the same scenario
- Generated from real server output during development
- Used as documentation of the SSE protocol

### Why 15s test timeout?

SSE scenario tests simulate streaming events with realistic timing. Complex scenarios (multi-tool chains, plan tracking with delegation) involve 20+ events arriving over time with `waitFor()` polling. The 15s timeout prevents false failures while still catching genuine hangs.
