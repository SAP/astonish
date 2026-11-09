# Studio durable Goals and tracked commitments

`BotPanels` exports `BotGoalsPanel` without changing Workspace or Schedule. The panel uses typed wrappers in `web/src/api/botGoals.ts` and the existing routes in `pkg/api/bot_goal_handlers.go`; there is no new persistence schema or backend endpoint.

## Goals

Owners can create or edit title, description, observable completion criteria, and an optional due time. Browser-local date/time controls serialize to RFC3339 UTC; clearing a deadline sends `due_at: null`. Creation uses revision zero; edits use the selected saved goal revision. Completed goals are read-only.

Saved evidence is displayed as owner confirmation, worker observation, or inference, with summary, source, timestamp and actor. Goal completion requires an explicit evidence review, summary, source, and non-future observed time. The browser sends only the confirmation DTO: the server supplies authenticated actor and evidence kind. Evidence timestamps remain stable on retries.

## Tracked commitments

Selecting a goal loads its durable commitments separately from run-local to-dos. Creation and edits use PUT with a stable item UUID and the parent goal's reviewed revision. Supported states match the backend: open, blocked, completed, cancelled. Blocked/cancelled require a reason; completed requires fresh reviewed owner confirmation. The existing endpoint replaces evidence, rather than appending or preserving an impersonated worker observation, and the UI explicitly explains this before saving.

Goal completion is blocked while a loaded commitment remains open or blocked. The server remains authoritative for concurrent commitments and permission checks. A successful commitment write reloads the saved goal because the backend increments its revision.

## Failure and permission handling

Read-only users can inspect goals and evidence but cannot mutate fields. Loading failures remain visible and disable unsafe mutations. Edits and evidence drafts are not overwritten by background Workspace refreshes. A revision conflict preserves the draft and blocks writes until an explicit reload; saved server fields are displayed alongside the preserved draft for comparison. The reload resets evidence review and requires the user to submit again rather than automatically retrying a mutation. A post-save refresh failure is distinguished from a failed write. Changing Bot identity/scope resets panel-local state and aborts pending commitment reads.

## Focused verification

From `web/`:

```sh
npm test -- src/api/__tests__/botGoals.test.ts src/components/bots/__tests__/BotGoalsPanel.test.tsx src/components/bots/__tests__/BotWorkspace.test.tsx src/components/bots/__tests__/BotSchedulePanel.test.tsx
npm run typecheck
```

These tests cover exact endpoint DTOs, local deadlines and clearing, CAS conflict recovery, evidence review and retry stability, durable item identity, reason requirements, open-commitment completion guards, read-only states, and load/post-save refresh failures. They mock transport; live server/browser persistence requires a separate acceptance drill.
