# Unfinished Bot intake

`GET`, `PUT`, `DELETE /api/studio/bots/intake/{id}?scope=personal|team` use the authenticated scoped services. Scope defaults to personal. UUIDs are client-generated and must not be recycled after discard. Intake content is private to its exact owner even in team scope (including against team admins). Membership is rechecked by the routed repository on every request. Missing or non-owned reads/deletes return 404; conflicting writes return 409 without exposing content.

## Wire contract

PUT accepts `store.BotUnfinishedPut`:

```json
{"expected_revision":0,"idempotency_key":"unique-request-key","content":{"mode":"blank","name":"","charter":"","language":"","timezone":"","identity":"","working_style":"","owner_preferences":"","carryover":{"summary":"","memories":[],"goals":[],"artifacts":[]}}}
```

Content modes: `blank`, `chat`, `template`, `adaptive`. Optional `source_id` is a UUID; optional `template_revision` is a nonnegative integer. Carryover memory entries have `content`, `category`; goals have `title`, `completion_criteria`; artifacts have `title`, `content`. These are inert source selections, never runtime bindings. Empty prose is valid; arbitrary extra fields are rejected.

GET and successful PUT return HTTP 200 with `store.BotUnfinishedIntake`:

```json
{"id":"uuid","owner_id":"uuid","scope":"personal","revision":1,"content":{"mode":"blank","name":"..."},"updated_at":"RFC3339 timestamp"}
```

DELETE accepts `{"expected_revision":1,"idempotency_key":"discard-key"}` and returns 204 with no body. GET after discard returns 404. Unknown fields, invalid JSON, multiple JSON values, invalid modes/revisions and bodies over 64 KiB return 400. Unauthenticated requests return 401; invalid tenant context or membership returns 403. Unavailable storage returns 503. Responses use `Cache-Control: no-store`.

## Persistence and concurrency

Separate `bot_intakes` Ent tables exist in personal and team databases. The scoped Bot repository exposes the optional `BotUnfinishedIntakeProvider` capability; no shared BotStore interface extension is required. The router's normal additive Ent reconciliation installs the new table on open/provision; generated migration metadata is the active migration source (see migrations.md).

Creation requires expected revision 0 and uses a unique INSERT, never UPSERT. Updates increment revisions using database predicates on ID, exact owner, previous revision and `discarded=false`. Only the immediately previous identical typed request can replay; a key alone cannot authorize a changed payload. Losing concurrent writes reread state to recognize exact retries or reject stale revisions.

Discard advances the revision, clears the persisted content column and retains a payload-free tombstone with mutation hash and timestamp. Replaying the exact discard is idempotent. Delayed saves and revision-0 retries cannot resurrect the ID, including after database restart. Intake creates no Bot, session, scheduler job or execution authority; reviewed creation remains a separate existing operation.

## Studio client

`web/src/api/botIntake.ts` owns typed transport and a per-dialog ordered writer. Identical save retries reuse the same key and revision; successful identical snapshots coalesce. Discard seals the writer synchronously, skips queued saves, waits for an in-flight PUT, then deletes at its returned revision. API failures retain local text. CAS errors offer an explicit server-copy comparison; loading the comparison does not replace local text without consent.

`BotDraftDialog` saves unfinished text on Close, Escape, or **Save intake and close**, without creating a Bot. It blocks editing while a save is pending, retains the dialog on failure, and warns before unloading unsaved edits. There is no best-effort unload write. `BotSidebar` offers **Resume intake** from the latest successful-save pointer after reload. Browser storage contains only `{id,scope}`, keyed by authenticated owner, active team and scope; no intake text or review consent is stored there. Resume fetches the server copy and always returns to unreviewed edit mode. Template revision mismatch preserves text but blocks reviewed creation until the owner selects a template again or explicitly changes to blank mode. Chat carryover and adaptive source metadata survive resumption.

Explicit dirty cancellation confirms API discard. Reviewed create/fork/promotion remains a separate operation. After success the resume pointer is removed immediately and cleanup is retried independently, with no creation button while cleanup is pending; cleanup failure never resends creation. Closing/reloading after a failed cleanup can leave an unreferenced server intake (no execution authority), rather than offering it for duplicate creation.

Frontend evidence: `cd web && npm test -- src/components/bots/__tests__ && npm run typecheck`. `BotIntake.test.tsx` uses the real transport with fake HTTP to cover Escape/save, sidebar remount/resume, renewed consent, CAS comparison, failed discard, independent cleanup, queued and in-flight save/discard ordering, exact retries, byte limits, and carryover/mode preservation. Live browser verification is separate.

## Evidence

`go test ./pkg/api -run '^TestBotUnfinishedIntakeHTTPAcceptance$' -count=1 -v` starts a real HTTP server with production routes and tenant middleware against temporary SQLite databases. It covers both scopes, authenticated owner isolation (including team admins), strict JSON bounds, restart/resume, concurrent unique-insert retries, save-vs-save and save-vs-discard CAS winners, exact retries, stale conflicts and durable discard/no resurrection. `TestBotUnfinishedDatabaseCASAndTombstone` additionally checks physical content clearing, conditional save-vs-discard rejection, cached-repository membership revocation, and absence of Bot/session/job rows. Authentication is a controlled fixture; PostgreSQL and browser integration are not exercised by this test.
