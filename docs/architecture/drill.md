# Drill (Test Engine)

## Overview

Drill is Astonish's AI-composed, mechanically-replayed test engine. The agent creates test suites by analyzing an application, then the suites are executed deterministically with shell commands, browser interactions, and assertions -- without LLM involvement during execution. This separates the creative work (writing tests) from the mechanical work (running tests), ensuring reproducible results.

Drill suites are stored as YAML flow definitions (type: `drill_suite` and `drill`) and executed by a specialized runner that handles multi-service orchestration, readiness checks, assertion evaluation, visual regression, and failure triage.

## Key Design Decisions

### Why Separate Composition from Execution

Most AI testing approaches have the LLM drive the browser in real-time (reasoning about what to click, what to assert). This is slow, non-deterministic, and expensive. Drill separates the two phases:

1. **Composition** (LLM-powered): The agent analyzes the application, identifies test scenarios, and generates YAML test definitions with explicit steps and assertions. For UI navigation flows, `/drill` can offer **human capture**: `browser_request_human(capture_actions: true)` over KasmVNC → `browser_get_action_log` → `draft_drill_from_action_log` (mode-neutral step skeleton) → chat adds asserts / filters noise → `validate_drill` / `save_drill`. Mode (`tutorial` vs test) is chosen by chat, not by the draft tool.
2. **Execution** (mechanical): The drill runner executes steps exactly as defined, evaluates assertions deterministically, and reports results without any LLM calls.

The only exception is `semantic` assertions and `triage` mode, which use targeted LLM calls for specific judgment tasks.

### Why drills are the preferred live verify for running surfaces

Astonish Code finds files quickly (codegraph). That is not proof a session container exists, an overlay is mounted, or the in-container browser works. `go test ./pkg/sandbox/...` of library code is the wrong harness for those outcomes.

When a plan phase's `verify_kind` is `behavior` and the surface is sandbox, daemon, browser, CLI, or Studio UI, prefer a **drill** (compose with the LLM, replay mechanically via `run_drill`) over package tests:

1. Overlay mounted at `/sandbox/rootfs`
2. Product binary present (CloakBrowser at `/home/browser/.cloakbrowser/*/chrome`, not `which chromium`)
3. `browser_navigate` to a public URL succeeds
4. Session container named `astonish-session-*` (Docker) or the pod is Running (Kubernetes)

Builtin skill `verify-live-with-drill` is the agent protocol. Builtin skill `inspect-live-surface` is the diagnostic protocol when the user reports a live miss. Do not install Debian `chromium` to satisfy a PATH probe.

Example smoke (agent-composed; mechanical replay):

```yaml
type: drill
suite: sandbox-browser
description: Overlay has CloakBrowser and browser_navigate works
nodes:
  - name: overlay-mounted
    type: tool
    args:
      tool: shell_command
      command: "findmnt /sandbox/rootfs | grep -E 'overlay|fuse'"
    assert:
      type: contains
      expected: overlay
  - name: cloakbrowser-binary
    type: tool
    args:
      tool: shell_command
      command: "ls /home/browser/.cloakbrowser/*/chrome"
    assert:
      type: contains
      expected: chrome
  - name: navigate-public
    type: tool
    args:
      tool: browser_navigate
      url: https://example.com
    assert:
      type: contains
      source: snapshot
      expected: Example
```

### Why Multi-Service Ready Checks

Real applications often require multiple services (database, backend, frontend). Drill suites support:

- **HTTP ready checks**: Poll a URL until it returns success (e.g., `http://localhost:3000/health`). Prefer a **functional** readiness URL when the app has setup/onboarding (e.g. setup-complete status), not only process liveness — health ≠ usable for browser E2E.
- **Port ready checks**: Wait for a TCP port to accept connections.
- **Output ready checks**: Wait for a specific string in the service's stdout (e.g., "Server started on port").

Services are started in declaration order and stopped in reverse order. Each service can have its own ready check and environment variables.

### Canonical start script (template bootstrap)

For multi-service apps, prefer a single start script stored on the **sandbox template** as `bootstrap_files` (e.g. `.astonish/start-services.sh`). Every container from that template gets the file injected at session start; it is **not** auto-executed.

**`run_drill` is test-only.** It injects suite credentials (idempotent) and executes drills. It does **not** switch templates, git-pull, run `configure`/`setup`/`ready_check`/`teardown`.

Studio "Run" pastes suite **run instructions** (from `run_instructions` or auto-generated from `template` / `workspace` / `branch` / credentials / `setup` / `ready_check`). The chat agent:

1. `use_sandbox_template` when needed
2. `git fetch && git checkout <branch|main> && git pull --ff-only` when `workspace` is set
3. `inject_drill_credentials(suite_name)` when the suite declares `credentials` / `credential_injection` — **before** start-services (apps that read secrets only at process boot)
4. Run `configure` / `setup` / start-services.sh and wait for readiness
5. Call `run_drill` (also injects before tests; do not write secret files by hand)

Never start services first and inject afterward. Fleet sessions call `run_drill` alone when the stack is already live (fleet wiring injects credentials early).

```yaml
suite_config:
  template: "@myapp"
  workspace: /root/myapp
  branch: main
  credentials:
    providers: myapp-providers
  credential_injection:
    files:
      - credential: providers
        path: /root/myapp/config/providers.yaml
        field: value
        mode: "0600"
  configure:
    - "test -f /root/myapp/config/providers.yaml"
  setup:
    - "bash /root/myapp/.astonish/start-services.sh"
  ready_check:
    type: http
    # Prefer functional readiness over bare /health when the app has a setup gate:
    # url: "http://localhost:3000/api/setup/status"  # then assert output includes is_setup_complete
    url: "http://localhost:3000/"
    timeout: 60
    interval: 2
  teardown:
    - "bash /root/myapp/.astonish/stop-services.sh || true"
```

Put offline / file / env configuration in `configure:` (agent prep, after inject). If an API must run **after** services are up, put it in `start-services.sh` after the ready poll.

**Ready check tip:** `/health` (or similar) is process **liveness**. For apps with onboarding/setup that must complete before UI E2E, poll a **functional** endpoint (setup status, authenticated ping, homepage that only renders when configured) and require the expected payload — e.g. curl + `output_contains`/`is_setup_complete`. Do not assume liveness implies browser-test readiness.

Canonical scripts start each daemon under a **detached restart supervisor** (`setsid` + `nohup` + `while true` restart loop + supervisor PID file), poll until ready and briefly stable, then **exit 0**. Do **not** end with `wait`, and do **not** use bare `npm run dev &` or one-shot `setsid` without a restart loop. Prefer `npx vite --host 0.0.0.0` over `npm run dev`. Always run a newly written start script once before `save_sandbox_template`; use `overwrite: true` to replace an existing named template after fixing bootstrap files. Self-overwrite (session based on the same template name) flattens onto the parent and must not delete-first — see `pkg/sandbox/AGENTS.md`.

### Recovering a deleted template

If a failed overwrite removed the named template from the registry while the chat session still has the app filesystem:

1. Do **not** switch templates or reboot the session.
2. Restart Studio on a binary with the flatten/materialize fix.
3. `save_sandbox_template(template_name: "…", overwrite: true, bootstrap_files: …)` — recreates the template (materializes live rootfs onto `@base` if the source is already gone).
4. Add `workspace` + `branch` on the suite YAML so Studio Run git-pulls next time.


### Why Visual Regression Testing

Drill includes pixel-level visual comparison:

1. First run creates baseline screenshots.
2. Subsequent runs compare against baselines with configurable tolerance (default 1%).
3. Anti-aliasing tolerance ignores pixel differences in border regions.
4. Red-highlighted diff images are generated for visual inspection.
5. Baselines are stored as artifacts and can be manually updated.

### Tutorial mode vs test mode

`drill_config.mode` selects runner semantics:

| Mode | Purpose | Assertions | Recording / pacing |
|---|---|---|---|
| `test` (default / empty) | Deterministic smoke/CI | Required for useful drills; failures fail the test; triage/retries per config | Not auto-managed |
| `tutorial` | Regenerable UI training scripts | Content asserts **fail** the run (broken/empty pages must not film green); tool errors still fail | `narration`, `hold_ms`, `record` (`start`/`stop`/`segment`); auto-segment when narration is set; writes `scene_manifest.json` |

Tutorial defaults: `on_fail: continue`, no triage/retries unless YAML sets them. Always honor `hold_ms > 0` (sleep after a successful step) in any mode.

**Explore-first authoring:** `/tutorial-drill` must explore the live UI (click nav, `browser_snapshot` + `browser_take_screenshot` each must-show beat, note reveal interactions and failures) **before** drafting voiceovers. Snapshot drives selectors/asserts; screenshot gives the creator a visual of what the agent sees. `validate_drill` rejects TODO stubs, navigate-only recorded scenes (except warm-up `open_app`), and recorded scenes without content asserts (`contains` / `element_exists`, preferably `source: snapshot`). Dry-run in chat (same snapshot + screenshot pairing) before `run_drill`.

Example:

```yaml
type: drill
suite: astonish-product
drill_config:
  mode: tutorial
  tags: [tutorial]
nodes:
  - name: open_studio
    narration: "Open Astonish Studio from the home screen."
    hold_ms: 4000
    record: segment
    type: tool
    args:
      tool: browser_click
      ref: "studio-link"
```

After a tutorial `run_drill`, per-scene MP4s and `scene_manifest.json` are returned in `artifact_paths` and registered as **session artifacts** (Studio Files list / download). Studio Chat also renders a **persistent scene slideshow** (`TutorialSceneSlideshowCard`): prev/next navigation through every manifest scene (screen MP4s inline; avatar/b-roll placeholders with narration). The card survives page refresh via the `[tutorial_scene_slideshow]` session marker. Authoring is **blueprint-first**: `/tutorial-drill` interviews the creator (reuse stack vs greenfield), presents a HeyGen-style Scene|Voiceover|Visual table (avatar / b-roll / screen), and only after Approve converts **screen** rows into executable drill nodes while embedding the **full ordered cut list** under `drill_config.scenes`. After the run, `scene_manifest.json` lists every blueprint scene in order; screen rows carry `path` / `duration_seconds`, while avatar/b-roll stay scripted (empty media path) for a later provider step.

**Suite separation:** tutorial drills live only in dedicated tutorial suites. Never append `mode: tutorial` drills into a regular smoke/CI suite. When the product already has a drill suite, `/tutorial-drill` **copies** `suite_config` (template, start script, credentials) into a **new** sibling suite (e.g. `juicytrade-tutorial`) and saves only tutorial drills there. `/tutorial-drill-add <suite>` appends more tutorials **only** if that suite is already a tutorial suite (`IsTutorialSuite`); otherwise it refuses and points back to `/tutorial-drill`. (Aliases `/tutorial` and `/tutorial-add` still work.) **Never** tag tutorial drills into default fleet smoke without filtering `mode != tutorial` (or excluding the `tutorial` tag).

Example `drill_config.scenes` (written by Approve):

```yaml
drill_config:
  mode: tutorial
  blueprint: juicytrade_overview_blueprint
  scenes:
    - id: intro
      voiceover: "Meet JuicyTrade."
      visual_kind: avatar
      visual_description: "Presenter on branded backdrop"
      hold_ms: 5000
    - id: dashboard
      voiceover: "Here's your dashboard."
      visual_kind: screen
      visual_description: "Net Liquidation and account badge"
      drill_node: dashboard_overview
      hold_ms: 9000
```

Authoring: `/tutorial-drill` wizard — **required explore pass** (click through must-show beats, snapshot content) before `draft_tutorial_blueprint`; optional Path B human demo via `browser_request_human(capture_actions: true)` → `draft_drill_from_action_log` (neutral skeleton only; chat adds `mode: tutorial` / narration / `record` after blueprint Approve). Product training videos use `mode: tutorial`; re-run after UI changes.

**Recording order (authoring invariant):** `applyTutorialRecording` starts a segment **before** the step tool runs. Authors must put unrecorded warm-up first (`browser_navigate` to the live app, `browser_fullscreen`) with no `narration` / `record`. Do **not** start the first `record: segment` on a blank tab. Blueprint conversion prepends `open_app` + `enter_fullscreen` for this. Preferred scene pattern: dry-run with content asserts → `browser_highlight` → `browser_click(animate_cursor: true)` → reveal interaction (e.g. click expiration) → `hold_ms`. Prefer in-app nav clicks over cold `browser_navigate` between scenes. Segment timing semantics are unchanged — polish via authoring, not runner changes.

### Why LLM-Powered Triage

When a drill fails in `triage` mode, an LLM analyzes the failure context and classifies it as:

- **Transient**: Timing issue, network flake -- retry likely to succeed.
- **Bug**: Genuine application defect -- needs developer attention.
- **Environment**: Infrastructure problem -- not related to the application.
- **Test issue**: The test itself is wrong -- needs updating.

This automated triage reduces the human effort needed to process test failures.

## Architecture

### Suite Lifecycle

```
0. Prep (agent / Studio run_instructions — not run_drill):
   - use_sandbox_template when needed
   - git sync workspace to branch (default main)
   - inject_drill_credentials (when suite declares credentials)
   - configure + start-services.sh + readiness poll
    |
    v
1. run_drill:
   - Inject credentials (idempotent final pass)
   - Execute drills (assertions, artifacts, optional triage)
   - Generate JSON report
```

### Assertion Types

| Type | Description | Source |
|---|---|---|
| `contains` | Output contains expected string | stdout, snapshot, screenshot |
| `not_contains` | Output does not contain string | stdout, snapshot, screenshot |
| `regex` | Output matches regular expression | stdout, snapshot, screenshot |
| `exit_code` | Command exited with expected code | shell command |
| `element_exists` | CSS selector finds element in DOM | browser page |
| `semantic` | LLM judges if output meets criteria | any (uses LLM call) |
| `visual_match` | Screenshot matches baseline within threshold | browser screenshot |

### Drill YAML Structure

```yaml
# Suite definition (type: drill_suite)
description: "E-commerce smoke tests"
type: drill_suite
suite_config:
  template: "@myapp"
  services:
    - name: database
      setup: "docker compose up -d postgres"
      ready_check:
        type: port
        port: 5432
        timeout: 30
    - name: backend
      setup: "npm start"
      ready_check:
        type: http
        url: "http://localhost:3000/health"
  teardown:
    - "docker compose down"

# Individual drill (type: drill)
description: "User can add item to cart"
type: drill
suite: "E-commerce smoke tests"
drill_config:
  timeout: 120
  on_fail: triage
nodes:
  - name: navigate_to_product
    type: tool
    action: browser_navigate
    args:
      url: "http://localhost:3000/products/1"

  - name: check_product_page
    type: tool
    action: browser_snapshot
    assert:
      type: contains
      expected: "Add to Cart"

  - name: add_to_cart
    type: tool
    action: browser_click
    args:
      ref: "add-to-cart-btn"

  - name: verify_cart
    type: tool
    action: browser_snapshot
    assert:
      type: semantic
      expected: "Cart shows 1 item with the correct product"
```

### Composite Executor

The drill runner uses a composite executor that routes different tool categories to different backends:

- **Container tools** (shell_command, file ops): Routed to the sandbox container via NDJSON.
- **Browser tools**: Routed to Chromium + KasmVNC inside the same session container (same path as Studio chat). Use `localhost` in URLs just like shell curls.
- **Local tools**: Direct in-process execution for tools that don't need sandbox or browser.

This handles the common case where a drill starts services in a container and tests them via an in-container browser.

Authors can write `http://localhost:<port>` in both `shell_command` curls and `browser_navigate` URLs. Shell keeps localhost as written. Browser navigation normalizes `localhost` / `::1` to `127.0.0.1` so Chromium does not fail against IPv4-only listeners (common with Vite `--host 0.0.0.0`). `{{CONTAINER_IP}}` placeholders remain supported; prefer localhost over hard-coded bridge IPs.

### Parameterized Tests

Drills support data-driven testing via the `parameters` field:

```yaml
parameters:
  - username: "admin"
    password: "secret123"
    expected: "Welcome, Admin"
  - username: "guest"
    password: "guest"
    expected: "Welcome, Guest"
```

Each parameter set runs the full drill with those values substituted into `{{variable}}` placeholders in node args.

### Artifact Management

The drill runner collects:

- **Logs**: stdout/stderr from each step.
- **Screenshots**: Captured at assertion points and on failure.
- **Diff images**: Red-highlighted pixel differences for visual regression failures.
- **JSON report**: Structured test results with timings, assertion outcomes, and triage classifications.

## Key Files

| File | Purpose |
|---|---|
| `pkg/drill/runner.go` | Main drill runner: suite lifecycle, test execution, assertion evaluation |
| `pkg/drill/assertions.go` | Assertion evaluation logic for all types |
| `pkg/drill/visual.go` | Visual regression: baseline management, pixel comparison, diff generation |
| `pkg/drill/triage.go` | LLM-powered failure classification |
| `pkg/drill/artifacts.go` | Artifact collection and management |
| `pkg/drill/executor.go` | Composite executor: routes tools to sandbox, browser, or local |
| `pkg/config/yaml_loader.go` | Drill YAML schema: DrillSuiteConfig, DrillConfig, AssertConfig |
| `pkg/tools/drill_tool.go` | Drill management tools (save, validate, delete, list) |
| `pkg/tools/run_drill_tool.go` | Drill execution tool with the 1000-line creation wizard prompt |
| `pkg/tools/inject_drill_credentials_tool.go` | Prep-time credential injection (before start-services) |
| `pkg/drill/run_instructions.go` | Studio Run prep text (Go); mirrored by `web/src/utils/generateRunInstructions.ts` |
| `pkg/drill/scene_manifest.go` | Tutorial `scene_manifest.json` writer |
| `pkg/tools/tutorial_prompt.go` | `/tutorial-drill` and `/tutorial-drill-add` wizard prompts (blueprint-first) |
| `pkg/tools/tutorial_blueprint.go` | HeyGen-style tutorial_blueprint schema + drill conversion |
| `web/src/components/chat/TutorialBlueprintCard.tsx` | In-chat Scene\|Voiceover\|Visual approval table |
| `web/src/components/chat/TutorialSceneSlideshowCard.tsx` | Post-run scene navigator (screen video + avatar/b-roll placeholders) |
| `pkg/browser/action_recorder.go` | DOM action capture for human demo → draft YAML |
| `pkg/tools/draft_drill_from_action_log.go` | Action log → mode-neutral drill YAML skeleton (chat specializes) |

## Interactions

- **Sandbox**: Drill services run inside containers. The composite executor routes container tools through the sandbox node protocol.
- **Browser**: Browser-based drills use Chromium + KasmVNC inside the same sandbox session as shell tools (same path as Studio chat). Use localhost URLs.
- **Flows**: Drills are a specialized flow type (`type: drill`/`drill_suite`) stored in the flow directory.
- **Fleet**: The E2E agent in a fleet can run drills as part of its validation workflow.
- **API/Studio**: Drill view in Studio provides suite management, execution, and result viewing.
