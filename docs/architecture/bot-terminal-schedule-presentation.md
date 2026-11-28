# Terminal schedule-operation presentation

The terminal consumes the optional `BotOperationView.presentation` through the
existing Bot transcript mapper and shared `events.Transcript` reducer. The wire
mapper must forward internal operations, not drop them: both the visible
transcript and launcher's authoritative operation heads need their state.

- `internal`: merge the validated, scoped operation head without adding a
  transcript item. Pending/applying operations still drive the existing status
  indicator, including after a `done` event.
- `conversation`: upsert exactly one non-provisional assistant item using
  `operation_id`, with the server-projected summary as its body. Do not prefix
  lifecycle status or append a second schedule verification block. This also
  applies to failure, blocked-without-authorization, and cancellation summaries.
- Absent presentation: preserve the legacy system item and authorization
  exception review instructions. Authorization decisions stay in the existing
  bound `/bots exceptions` flow.

Both revision and state sequence must advance before an existing operation head
can be replaced. Deduplication and stale-event rejection happen before
presentation, so reconnect replay, stale pending frames, and stale snapshots
cannot overwrite a final confirmation or restart its pending indicator.
Operation identities remain separate from Bot version/revision authority.

A conversation item is a complete publication boundary, like an autonomous Bot
report: later model text cannot append to or replace it, tool folds cannot move
it, and routing events cannot attribute a model badge to it. This protection
applies to both sticky and linear transcript modes.

## Coverage

- `pkg/tui/events/bot_schedule_presentation_test.go`: internal head/status,
  revision/sequence ordering, final-item identity, streaming boundaries, legacy
  and authorization compatibility.
- `pkg/tui/bot_schedule_presentation_render_test.go`: actual transcript renderer
  for live and replayed pending/applying/final sequences, including unsuccessful
  outcomes, duplicate final events, and stale pending events.
- `pkg/launcher/tui_bots_schedule_presentation_test.go`: production HTTP SSE
  watch/mapping path, repeated watch replay, and stale snapshot reconciliation.

Focused verification:

```sh
go test ./pkg/tui/... ./pkg/launcher -run 'TestBot(Schedule|Operation|Composer|Exception|Scheduled)' -count=1
```

These tests exercise the production mapper, reducer and renderer with controlled
HTTP events; they do not claim a deployed daemon or interactive PTY session was
exercised.
