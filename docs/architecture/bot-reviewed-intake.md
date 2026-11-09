# Reviewed Bot intake and configuration

Current default navigation is compact Chat plus Details, not a permanent Memory & charter workspace tab. [Chat-first contracts](bot-chat-first.md) covers bounded conversational proposals and exact decisions; the direct reviewed APIs below remain compatible. Source proposals are recoverable by saved ID and identical decision retry, not a discoverable list of lost reviews. Default tool-free drafting does not execute external tasks.

These APIs run through the authenticated Studio principal, tenant middleware and
explicit `scope=personal|team` Bot store. Public actor/owner/tenant headers and
body fields are not authority. Unknown JSON fields are rejected.

## Runtime policy

`PUT /api/studio/bots/{botID}/configuration?scope=personal`

Body: `expected_revision`, `expected_version`, `reviewed: true`, nonempty `reason`,
`autonomy`, `budget`, and `delivery` (the corresponding BotVersion types).
This is a complete reviewed replacement of those three policy sections. It is
accepted only for draft, paused or stopped Bots. A single transaction checks both
CAS values, saves an immutable version attributed to the authenticated actor,
revokes pending/approved old action grants, disables runtime schedules and records
an audit activity. It does not activate, clear a shutdown barrier or change the
lifecycle status. Trigger configuration remains the separate reviewed trigger API.

Ordinary `POST .../versions` edits remain prose-only, including after a draft has
reviewed runtime policy. Activation remains a separate readiness-gated API.
Delivery binding targets are requests, not authority. Configuration resolves each
binding ID against the server-verified enabled linked identity owned by the Bot
owner, and checks live organization/team membership. Claim and transport send
repeat authorization; revocation before a deferred send records a skipped
notification. Currently only canonical private Telegram user chats (no thread)
are provable. Slack linked member IDs do not establish installation ownership or
room access: Slack, email and group destinations fail closed until reliable scoped
installation/destination proof is available. No notification payload reaches the
daemon-global Slack credentials on that unsupported path. Ordinary interactive
channel delivery is unchanged; configuration itself never sends.

## Studio configuration editor

`BotConfigurationPanel` (`web/src/components/bots/BotConfigurationPanel.tsx`)
exports a self-contained panel with `{ bot, readOnly, onChange }` props for the
Memory & charter workspace. Its typed `api/botConfiguration.ts` transport uses
`teamFetch` and explicit scope, and loads the exact immutable root version.
The labeled form covers all four autonomy levels, allow/deny tools, paths, hosts,
contact/publish rights, purchase caps and currency, all runtime budgets, and
notification preferences and destination bindings. Optional empty budgets are
omitted rather than converted to zero. Model-price support is not inferred and
the UI does not promise enforcement of unsupported money limits.

Review shows baseline and proposed values, the reason, revision and version.
Saving requires both configuration review and a separate explicit rights,
spending and destinations consent. Edits invalidate both consents; no activation,
run, approval grant or trigger endpoint is called. Active/deleted and non-owner
views are read-only. The server remains authoritative for ownership and supported
delivery identity proofs.

The editor is keyed by tenant/scope/Bot, not revision. Polling never silently
replaces edits or upgrades CAS values. A 409 preserves edits, blocks retry, and
requires **Load latest baseline, keep edits**, followed by a fresh difference
review and both consents. Other server errors remain visible with edits intact.
Draft contents remain in component memory, not local storage. The workspace
must keep the panel mounted during tab changes to preserve edits.

Focused verification: `cd web && npm test -- src/api/__tests__/botConfiguration.test.ts src/components/bots/__tests__/BotConfigurationPanel.test.tsx && npm run typecheck`.

## Durable source-bound review proposals

The direct reviewed promotion, conversion and template fork APIs below remain
compatible. New clients can instead persist an owner-private review before there
is any Bot:

- `POST /api/studio/bots/source-proposals?scope=personal|team`: `mode` (`chat`,
  `adaptive`, `template`), `source_id`, `name`, `charter`, `carryover`, and
  `idempotency_key`. Templates additionally require `template_revision` and team
  scope; their destination is always the authenticated owner's personal scope.
- `GET .../source-proposals/{proposalID}` restores the exact immutable review.
- `POST .../source-proposals/{proposalID}/decision`: only `digest`, `decision`
  (`confirm` or `reject`), and `reviewed_for_team` are accepted. Team Chat/adaptive
  confirmations require the latter. Actor and replacement payload fields are
  rejected; worker contexts cannot consent to their own proposals.

The server binds the complete selected summary, memories, goals and text artifacts
and authenticated owner to a canonical digest. Chat source freshness hashes
metadata and event contents (only the hash is persisted, never the transcript).
Adaptive review stores only the schedule/instructions/update stamp, never Params
or delivery credentials. Template review includes the exact published revision.
Changed source content invalidates confirmation; a template revision mismatch
returns conflict and never silently upgrades. Free-text selection still requires
human redaction; this is not a credential detector.

Records use a typed `source-intake-v1:` envelope in existing owner-private BotIntake
storage. Ordinary unfinished saves/reads/deletes reject this envelope rather than
allowing replacement. Proposals create no placeholder Bot, Home or scheduled job.
Chat/adaptive confirmation atomically creates an inert draft and durable applied
receipt; conversion pauses the original only inside that same transaction. Source
Chat is unchanged. Local source transactions are serializable; contention errors
are safe to retry with the same ID/digest.

Templates cannot be atomic across team and personal databases. Confirmation first
persists `applying` intent, then takes a proposal write fence, creates a private
inert draft using a deterministic proposal/digest key, and records `applied` only
after the destination committed. A receipt-write failure leaves `applying`, not a
false success or a rejectable proposal. Retrying recovers the same destination
before checking a newer template revision. Rejection after applying intent is a
conflict. No path autoactivates. Terminal applied/rejected decisions replay their
original receipt; changed idempotent creation payloads conflict.

Verification: `go test ./pkg/store/entstore ./pkg/api -run
'TestBotSourceIntake|TestBotConversationalControlHTTPAcceptance' -count=1` covers
real routed HTTP, source staleness, selection immutability, worker consent denial,
source preservation, rollback, close/reopen and cross-database receipt recovery.
The API named conversational acceptance gate includes Chat, adaptive and template
intake; the launcher package retains its separate model/worker acceptance gate.

## Chat promotion

`POST /api/studio/bots/promote-chat/{sessionID}?scope=personal`

Body: `name`, `idempotency_key`, `charter`, `reviewed: true`, optional
`reviewed_for_team: true`, and `carryover`:

- `summary`: explicitly selected/redacted summary text;
- `memories`: selected `{content, category}` text copies;
- `goals`: selected `{title, completion_criteria}`;
- `artifacts`: selected `{title, content}` text snapshots.

The source must be an ordinary Chat owned by the authenticated actor in the same
scope. Team promotion additionally requires `reviewed_for_team`. There is no
private-to-team fallback. A new draft and separate Bot Home are created atomically;
the source Chat, transcript and metadata are not changed. Summary/memory copies
are inserted into the new Home's memory, goals get new identifiers, and text
artifact snapshots are retained in the creation activity. **Binary files and
filesystem artifact copying are not supported by this API.** Credentials, source
metadata, raw transcripts, runtime bindings and grants are never imported. Human
review/redaction of supplied free text is required; this is not automatic secret
detection or automatic summarization.

## Adaptive schedule conversion

`GET /api/studio/bots/convert-adaptive/{jobID}?scope=personal` returns the owned,
active adaptive job's reviewed `source_job_id`, `updated_at`, `schedule` and
`instructions`, without Params/delivery/credentials.

`POST` to the same URL takes `expected_updated_at` from that review, `name`,
`idempotency_key`, `charter`, `reviewed: true`, and `reviewed_for_team` for team scope.
It creates an inert replacement draft with a disabled cron trigger carrying only
the reviewed schedule and instructions. The original is paused **in the same
transaction**, after all replacement writes; rollback preserves the original and
removes all replacement rows. The original is not deleted. Activation and trigger
enablement remain separate review actions. Non-adaptive jobs are rejected.
Timestamp CAS uses the persisted microsecond bucket to match SQLite's timestamp
read precision (PostgreSQL also persists microseconds); the exact read value is
checked before the conditional update.

Both intake operations bind idempotency to operation, source ID and reviewed
payload. Retrying the same committed conversion returns its existing replacement,
including after the original was paused. A changed payload/source returns 409.

## Studio intake increment

`BotDraftDialog` retains the existing New Bot and ordinary Chat promotion entry
points and shared Chat renderer. Intake now has separate editable content and
read-only review steps. Promotion accepts manually selected/redacted summary,
memories, goals with completion criteria, and text artifact snapshots; empty
added rows must be completed or removed. It never fetches or copies a transcript.
Template selection fetches the current team template, displays its language,
goals, criteria and disabled schedule suggestions, and pins that revision when
forking to a private draft. A stale revision is a visible server error, not a
silent upgrade.

Back preserves fields and clears consent. Failed saves preserve fields and
consent for retry; an unchanged request retains its idempotency key. Editing the
payload produces a new key. Escape, outside-click, close and Cancel require
explicit discard when fields are dirty; Keep editing preserves the intake.
A `beforeunload` guard asks the browser to warn before reload/navigation while
unsaved content exists (browser support and user interaction requirements apply).
No sensitive draft text is stored in browser storage. Successful Save uses the
existing atomic backend creation, navigates to the persisted private draft, and
never calls activation. Blank creation additionally reviews optional language,
timezone, identity, working style and owner preferences via the existing
`definition.persona` DTO. Promotion and template fork do not accept that DTO, so
those modes do not offer unsupported persona inputs. Review states the supervised,
no-contact/no-publishing/no-spend defaults and absence of active schedules.
Policy configuration, activation and trigger enablement remain separate actions.

## Durable unfinished intake backend

The owner-private unfinished-intake backend is implemented separately from
`BotIntakeStore` reviewed promotion/conversion. See [Unfinished Bot intake](bot-unfinished-intake.md)
for GET/PUT/DELETE DTOs, the 64 KiB bound, revision CAS, exact immediate retry,
content-free discard tombstones and scoped personal/team Ent storage.
No Bot, Home session or schedule is created by autosave. Reviewed final creation
still uses the existing create/promotion/fork/conversion APIs and reauthorizes
source selections. After reviewed creation succeeds, discard the intake with its
latest revision; a conflict must preserve newer content rather than erase it.

This backend does not itself wire Studio autosave/resume. Browser integration,
combined policy/schedule intake and adaptive conversion UI remain separate work.
SQLite routed HTTP tests prove reopen, owner privacy (including team admins),
concurrent save CAS, strict bounds, membership revocation and durable discard.
Store tests verify conditional save-vs-discard rejection, cleared content and zero
Bot/session/job rows. PostgreSQL concurrency is not claimed by these tests.

## Verification

```
go test ./pkg/api -run '^TestBotReviewed' -count=1
go test ./pkg/store/entstore -run 'TestBotReviewedConfigurationRevokesOldGrants|TestBotAdaptiveConversionRollback|TestBotReviewedDraftRejectsUnreviewedPolicyReplacement' -count=1
```

HTTP acceptance tests use real HTTP servers, production routing and tenant
middleware, and SQLite for both personal/team stores. Store tests additionally
inject a failure after the original pause to prove full transaction rollback and
exercise old-grant revocation while retaining paused lifecycle state.
