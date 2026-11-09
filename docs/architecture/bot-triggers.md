# Bot trigger admission

Reviewed trigger changes use `BotTriggerStore.ChangeTrigger`. Cron definitions create disabled, Bot-owned scheduler rows. Explicit enable requires an active Bot; disable clears the next occurrence, and removal deletes the owned row. Generic scheduler mutation cannot bypass this ownership.

Hooks/webhooks start disabled. A signed `dry-run` verifies the exact current version without queuing work, then explicit enable consumes that reviewed state. Saving any change or disabling the trigger invalidates its proof. The stored proof fingerprint is a domain-separated HMAC over the trigger definition and Bot scope/identity/owner; changing or removing the credential invalidates enable/admission. A rotated key requires disable/save, a new dry-run, and explicit enable. Existing unkeyed proof fingerprints fail closed and require a fresh dry-run.

## Scheduled owner routing

Bot-owned schedules persist `team_slug` from the authenticated principal during `ChangeTrigger`, never from request JSON, ambient store context, or a default team. Personal scheduler iteration supplies its trusted org/user lane and this persisted exact team to `BotWorker.SchedulerCallbacks`; the worker still reauthenticates the live owner and org/team membership before admission. Existing nonempty routing is retained across edits. Legacy schedules without routing remain fail-closed until an explicit owner-authorized trigger mutation fills it; no startup backfill or team scanning is used to repair them.

`TestBotSchedulerSQLiteTrustedTeamAdmission` reproduces the production personal/team scheduler lanes with real SQLite jobs and Bot repositories. It verifies authenticated (not ambient) team propagation, explicit legacy-row repair, queued admission, wrong/missing team and user rejection, personal/team isolation, and revoked membership rejection. Activation/home readiness and runtime are controlled; it does not prove a deployed daemon tick or model execution.

## Credential transaction boundary

Credential resolution occurs before the Bot transaction, using only the authenticated owner's current scoped store (personal or team, never fallback). The resolved secret lives only in a private request-local context value. A second credential query inside a Bot transaction would deadlock the single-connection SQLite backend.

Inside the transaction, signature validation checks the exact Bot ID, owner, scope, revision, immutable version, and complete trigger snapshot against the preflight binding. Current status, enabled/proof state, event signature purpose/timestamp/body, replay uniqueness, ownership and admission budget are still checked transactionally. Definition or lifecycle mutations between lookup and admission fail closed. No secret or external payload is persisted into Bot versions, activities or executable instructions; queued instructions come from the reviewed immutable definition.

Credentials are a preflight snapshot, not a cross-store atomic transaction: an already-resolved request can race a subsequent independent credential-store revocation. Requests resolving after removal fail closed; key rotation invalidates the stored proof. There is no long-lived credential cache.

Channel admission requires an authenticated channel principal and exact channel/chat/thread binding. The production channel resolver injects fresh team/personal Bot repositories in `store.Services`, without inheriting ambient repositories. `/bot_run personal|team bot-id trigger-id` carries the inbound message ID as replay identity and queues only the explicitly named, bound trigger; it never broadcasts or executes arbitrary message text. It does not resolve signing credentials. External event replay is a conflict, unlike idempotent schedule occurrence replay.

## Goal deadlines

`ChangeTrigger` accepts `goal_deadline` only for an existing active goal with a due date. Save creates a disabled Bot-owned schedule; enable sets its exact UTC due occurrence using the internal `@bot-goal-deadline` marker rather than recurring cron. The scheduler consumes it once and does not regenerate it on restart. Goal completion disables associated deadline schedules and clears their next occurrence; admission checks goal status again, and unrelated configuration changes cannot revive a completed goal's followups.

## HTTP authentication boundary

The Studio events endpoint continues to require authenticated, tenant-scoped owner access **and** a valid event signature. Public signature-only ingress is separately mounted at exactly `POST /api/bot-webhooks/events`; device and platform authentication exempt only that method/path. Its handler limits traffic to 10 requests/second (burst 20) per server, bodies to 96 KiB and requests to 10 seconds. Invalid signatures and rejected bindings return generic errors without scoped details.

An authenticated owner registers an enabled reviewed webhook using `POST /api/studio/bots/{botID}/triggers/{triggerID}/webhook-binding`. The response contains an opaque `binding_id` and the ingress path. The platform persists a server-owned exact org/scope/owner/Bot/trigger/fingerprint routing record. Ingress reads only that record, discards ambient principal/context values, resolves the registered scoped repository, rechecks live ownership/membership, and performs normal signed transactional admission. It neither scans tenants nor accepts caller-supplied org/user authority. The request is `{"binding_id":"…","event":{"bot_id":"…","trigger_id":"…","version":1,"event_id":"…","timestamp":123,"payload":"…"}}`, with the 64-character hex signature in `X-Astonish-Signature`. The signature covers target IDs, immutable version, purpose, event ID, timestamp and exact decoded payload bytes; the JSON payload is base64-encoded `[]byte`. Replay, stale timestamp, stale definition, disabled trigger and changed credential fail closed.

## Deterministic hook polling

A reviewed `hook` can specify `poll: {url, allowed_host}` plus cron/timezone. Save is disabled; dry-run fetches a baseline content hash and binds proof to the reviewed definition and resolved signing key; explicit enable creates/enables its Bot-owned occurrence in the existing scheduler. `BotSchedulerCallbacks.Admission` dispatches scoped `BotHookPollStore` implementations before normal cron admission. There is no second timer engine, model polling, or required unimplemented callback.

Each occurrence validates live Bot/version/proof and the exact persisted owned schedule before fetching. Requests are GET-only HTTPS, exact reviewed host, port 443, no userinfo/query/fragment, no ambient proxy/credentials/cookies, no redirects, 5-second timeout, 8-KiB response headers and 64-KiB response bodies. DNS is checked at dial time and the checked address is dialed; private, loopback, link-local, reserved and IPv6 transition ranges are rejected. Sources requiring authentication, custom ports, query strings or private-network access are intentionally unsupported.

An unchanged baseline queues nothing. Changed content admits one ordinary Bot run; its SHA-256 content identity uses the durable run replay index, so unchanged or previously seen content is never queued twice for that trigger. The scheduler advances successful no-change occurrences and preserves/backoffs failed occurrences. Payload bytes are untrusted observations, not executable instructions; admitted instructions remain the reviewed definition.

## Verification and boundaries

`TestBotTriggersHTTPAdmission` uses real HTTP, production routing/tenant middleware and SQLite. Requests and handlers have bounded deadlines. It exercises owned schedules, signed hook/webhook dry-run and admission, invalid/expired/replayed signatures, proof invalidation after edit, and an exactly bound channel command. Initial activation is a controlled fixture; no worker, model, or outbound channel send runs.

`TestBotTriggerLifecycleAndAdmission`, `TestBotTriggerCredentialBindingAndRevocation`, `TestBotTriggerResolvedCredentialExactBinding` and `TestBotTriggerConfigurationCASAndAuthorization` cover personal/team lifecycle, key rotation/removal, mutation during credential lookup, exact evidence binding, authorization, replay and non-persistence of signing secrets.

`TestBotExternalTriggersHTTPWorkerExecution` adds real-socket hook/webhook admission and linked channel commands followed by production `BotWorker.execute`, supervisor and ChatRuntime execution against SQLite. Its controlled model emits a known failure: assertions require model invocation, the expected error, durable terminal failed reports and transcript events for all three trigger kinds. It retains unauthorized, tampered, expired, replay and changed-proof rejection checks. HTTP principal injection, activation readiness, home backend and isolation preparation are controlled fixtures; this is not a live sandbox/provider or daemon polling test. `TestChannelBotServices` verifies production linked-user resolution supplies both scoped Bot repositories and rejects unknown senders.

`TestBotGoalDeadlineTriggerConfiguration` verifies personal/team save/enable, exact due occurrence, completion disabling, stale admission rejection and missing-goal rejection. `TestBotGoalDeadlineOneShotTick` verifies consumption survives scheduler restart.

`TestBotHookPollHTTPAdmission` exercises personal/team SQLite repositories with a controlled real HTTPS source, disabled → dry-run → enabled proof, the scheduler's actual 30-second tick dispatch, exactly one changed-content admission, unchanged-content skips and rotated-key rejection. Only DNS/TLS transport is redirected to the fixture; production disallows loopback. `TestBotHookPollBoundedNetwork` checks blocked address ranges, exact host rules and oversized responses; `TestBotSchedulerPollDispatch` checks daemon callback routing. `TestBotWebhookExactAuthException`, `TestBotWebhookBodyBound` and `TestBotWebhookRateAndSignatureGuard` cover the narrowly exempted public path and ingress limits. The public section of `TestBotTriggersHTTPAdmission` uses the real platform auth middleware with no JWT or tenant context, including signature, replay, expiry, wrong binding/target and key-rotation rejection.

These tests do not prove live PostgreSQL trigger execution, a deployed daemon, or production sandbox/provider effects; those remain separate verification gates. Non-Ent platform backends without the registration/admission interface return unsupported instead of accepting signature-only traffic.
