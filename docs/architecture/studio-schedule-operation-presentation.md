# Studio schedule-operation presentation

Studio consumes the optional `BotOperationView.presentation` field without changing operation authority or transcript transport. Its typed allowlist is `internal` and `conversation`; omitted fields retain the legacy operation card, and unknown values are rejected by `isBotOperation`.

- **Internal:** pending/applying schedule operations render no card or assistant bubble. The head and transcript identity remain in the normal reducer so busy indicators, source positioning, independent queued operations, and revision/sequence replay guards still work. Stream connectivity or source-run completion cannot clear pending operation work.
- **Conversation:** `projectBotOperation` projects confirmed schedule summaries into ordinary `agent` messages in the existing StudioChat transcript. `renderAgentMessage` supplies the exact same bubble styling, markdown, Copy and raw-markdown controls as other assistant replies in both themes. `BotOperationResult` only owns legacy and authorization cards, not a second assistant renderer. No operation heading, status badge, or duplicate verified-schedule/next-run line is appended. Failure/cancellation summaries and supplied blockers remain visible.
- **Authorization:** `blocked` plus `require_authorization` keeps the existing action card, saved-action fields, expiration, read-only restrictions, and approve/deny handling. This actionable state takes precedence over presentation hints; the backend normally omits presentation for it.
- **Compatibility:** absence of presentation preserves legacy operation rendering. Report gates, unrelated assistant text, errors, and ordinary Studio chat are untouched. There is no prose-based acknowledgement suppression in Studio; the backend owns acknowledgement policy.

Live events, snapshot refreshes, and replay all use the existing per-operation head merge. A stale pending head cannot replace an applied head. Head equality does **not** skip transcript projection: after a reset or snapshot preload the equal durable row still inserts the missing confirmation in replay order. Publication identity prevents duplicate replay from relocating it after later reports. Publications are hard boundaries for activity grouping and incremental text, while internal pending heads stay hidden and authoritative for busy state.

`connectBotEvents.onState('live')` signals connectivity after each row, not replay completion. Snapshot-only fallback waits for the explicit `onReplayComplete` callback from the existing `replay_complete` SSE frame. Fallback anchors a missing entry after its source when present; durable replay supersedes fallback placement. Session changes reset readiness and scope-filter fallback heads. Backend state-only operation rows and SSE payloads are unchanged; no synthetic backend agent event or second message store is introduced. The TUI's existing `ItemAgent` operation publication path continues consuming the unchanged protocol.

## Focused verification

From `web/`:

```sh
npm test -- src/api/__tests__/botEvents.test.ts src/api/__tests__/botOperationPresentation.test.ts src/api/__tests__/botOperationResult.test.ts src/components/chat/__tests__/botOperationProjection.test.ts src/components/bots/__tests__/BotOperationPresentation.test.tsx src/components/bots/__tests__/BotOperationResult.test.tsx src/components/bots/__tests__/BotOperationExceptionDisplay.test.tsx src/components/__tests__/StudioChat.test.tsx
npm run build
```

The Studio integration test covers live pending/applying → confirmed, unmount/reload with a preloaded final snapshot and chunked replay, two later scheduled reports, duplicate/stale heads, source ordering, session reset, shared bubble styling and Copy/raw controls in both themes. Snapshot-only fallback is separately checked against `replay_complete`, not connectivity. Independent pending work and unrelated text/errors remain covered. These mocked component/transport tests do not establish deployed browser or terminal acceptance. The built-Studio `TestBotScheduleConfirmedOnlyBrowserAcceptance` additionally exercises persisted worker operations through real HTTP, reload and later-question ordering; launch it through the browser environment helper in `tests/e2eboot/employee_http_acceptance.py`. Terminal rendering is owned outside this frontend change.
