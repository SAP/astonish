# web/src/components/chat — AGENTS.md

Chat-specific guidance under the parent [`web/src` guide](../../AGENTS.md). Read it first. The authoritative pipeline is [`docs/architecture/chat-rendering-pipeline.md`](../../../../docs/architecture/chat-rendering-pipeline.md); also consult [`docs/architecture/terminal-app.md`](../../../../docs/architecture/terminal-app.md) and [`docs/architecture/testing-chat-scenarios.md`](../../../../docs/architecture/testing-chat-scenarios.md). The executable scenario catalog is [`web/src/test/scenarios`](../../test/scenarios/), backed by fixtures in [`web/src/test/fixtures/scenarios`](../../test/fixtures/scenarios/).

## Ownership

- `../StudioChat.tsx` owns session/stream orchestration, incremental event reduction, transcript state, and top-level panel selection. Keep leaf rendering and pure derivations here: message types in `chatTypes.ts`, harness selection/layout in `chatHarness.ts`, tool grouping in `toolActivity.ts`, and focused visual components in their named files.
- Do not create a second transcript store or let leaf components interpret wire events. Extract pure reducers/helpers when event logic can be shared by send and reconnect paths.
- Preserve raw transcript semantics. Display grouping is derived: it must not mutate, reorder, or discard source messages needed for citations, history, reconnect, or parity clients.

## Incremental transcript reduction and SSE names

- Text arrives incrementally. Append chunks to the active agent turn; finalize it before hard-boundary events such as tools, artifacts, previews, errors, or `done`. Functional state updates must be safe under batched React rendering and duplicate/replayed reconnect events.
- The primary-send and reconnect reducers must handle the same events. Use the backend spellings exactly—never camel-case or invent aliases. Current chat names include `session`, `session_title`, `new_session`, `text`, `done`, `error`, `error_info`, `usage`, `system`, `thinking`, `retry`, `model_changed`, `tool_call`, `tool_result`, `approval`, `auto_approved`, `network_denial_hint`, `memory_saved`, `artifact`, `report_marker`, `image`, `flow_output`, `distill_preview`, `distill_saved`, `tutorial_blueprint_preview`, `tutorial_blueprint_approved`, `tutorial_scene_slideshow`, `app_preview`, `app_done`, `app_saved`, `subtask_progress`, `fleet_progress`, `fleet_redirect`, `fleet_plan_redirect`, `drill_redirect`, and `drill_add_redirect`.
- Fleet-stream names are likewise exact: `fleet_session`, `fleet_message`, `fleet_state`, `fleet_agent_started`, `fleet_agent_finished`, `fleet_task_posted`, `fleet_task_claimed`, `fleet_task_completed`, `fleet_mailbox_delivered`, `fleet_done`, plus any names currently emitted by the Go handler.
- `pkg/api/chat_runner.go` and `pkg/api/chat_handlers.go` are the wire source of truth. Add/rename an event in backend, SPA send/reconnect handling, terminal/TUI handling, docs, and scenario fixtures in the same change.

## Report gate and harness

A markdown artifact enters the report harness only when all three signals hold:

1. it was emitted after the most recent user message (last turn);
2. `fileType === 'Markdown'`;
3. `isReport === true`, set by the backend/`report_marker` contract when an ``astonish-report`` fence path matches the artifact.

Never widen this gate. A failure of any signal renders the compact `ArtifactCard`; fix missing report metadata in `pkg/api/chat_runner.go`, not with filename/content heuristics.

`deriveLatestHarness` receives already-gated report/video path sets. The latest harness emission auto-opens/focuses the panel; an explicit placeholder click wins until a newer emission appears. Preserve resizable width clamping, the chat-width floor, sidebar auto-collapse, stable focus identity, and compact `HarnessPlaceholder` transcript entries for reports, Apps, flow drafts, tutorial views, browser handoff, and eligible video.

## Artifacts, approvals, and tools

- Last-turn `Video` artifacts may open in `HarnessPanel` independently of the report gate. Slideshow-owned tutorial MP4s remain owned by `TutorialSceneSlideshowCard` and must not also become generic video harness items.
- Fetch media through `fetchArtifactBlob`, create an object URL for `<video>`, and revoke it on replacement/unmount. Never send protected artifact URLs directly to `<video>` (required team/CSRF headers would be absent), and never fetch binary media as text.
- Render `approval` as an actionable hard boundary and `auto_approved` as process narration. Preserve exact option/tool data and prevent duplicate submissions while an action is pending.
- `tool_call` and `tool_result` remain separate transcript records. `toolActivity.ts` may pair/fold contiguous work for `ToolActivityBlock`, but agent text and hard boundaries must not be absorbed. Unknown future message types are hard boundaries by default.

## Cross-client parity

Studio, the Go backend, and terminal/TUI are one user-visible protocol. Before changing event meaning, approvals, report markers, tool status, network denials, or completion/error behavior, inspect the backend emitter and terminal consumer described in the terminal architecture doc. Differences may be presentation-only; event semantics, ordering, and actionable states must remain aligned.

## Focused tests

From `web/`, select the smallest relevant set, then typecheck:

```bash
npm test -- src/components/chat/__tests__/toolActivity.test.ts
npm test -- src/components/chat/__tests__/ToolActivityBlock.test.tsx
npm test -- src/components/__tests__/StudioChat.test.tsx
npm test -- src/test/scenarios/core-chat.test.tsx src/test/scenarios/reconnection.test.tsx
npm test -- src/test/scenarios/harness-panel.test.tsx src/test/scenarios/downloads-artifacts.test.tsx
npm test -- src/test/scenarios/tool-execution.test.tsx src/test/scenarios/tool-interactions.test.tsx
npm run typecheck
```

Add scenario coverage for every wire-visible behavior, including both live and reconnect reduction. For report/harness work, test all three gate signals independently, latest-turn selection, manual focus versus newer emissions, generic versus slideshow video ownership, blob cleanup, and compact fallback rendering.
