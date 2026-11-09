# Phase 3 runtime: evidence and remaining acceptance

> Historical continuation record. For the current additive message/proposal/source-intake and compact UI contracts, see [Chat-first contracts](bot-chat-first.md). Older phase status and per-intent composer assumptions below are not current release evidence. Real live smoke and larger deployed specification scenarios remain pending; tool-free default drafting does not authorize external execution.

## Status — 2026-11-05

**Phase 3 remains running.** The runtime is implemented and exercised substantially beyond a persistence-only foundation, but there are still missing runtime behaviors, not merely a final test command. Do not mark the phase complete from passing package tests, catalogue presence, a blocked unsafe adapter, or the bounded exact-action continuation alone.

This is a continuation record for the runtime work, not a new execution plan and not a completion announcement. [Bots](bots.md) describes the control plane and runtime; [production adapters](bot-production-adapters.md) describes individual adapter/security boundaries. The implementation locations below are the source of truth where older capability descriptions differ.

## Implemented and exercised

- [x] Production composition: `pkg/daemon/bot_production.go` and `bot_worker.go` compose owner-scoped services, model selection, launcher, supervisor and ChatRunner independently of interactive messaging. Live membership, lease/fence, isolation and budget checks guard dispatch; team runs do not borrow personal authority.
- [x] Public activation and scheduled execution: `TestBotRuntimeHTTPAcceptance` exercises HTTP draft/configuration/explicit activation, missing-readiness rejection, dropped admission acknowledgement, **two distinct scheduler-admitted runs with distinct transcript sessions**, and one saved approved action per run. This is no longer an empty selector or SQL-seeded successful-run fixture.
- [x] Reopen and exact-action continuation: the acceptance journey closes/reopens SQLite and reconstructs worker/approval continuation. `TestBotApprovalSQLiteReopenChatRunner` independently exercises durable requeue, fresh fencing and replay rejection. This supports saved-action dispatch followed by a report, not arbitrary continuation of the original task.
- [x] Publication: trusted observations yield changed evidence and a verified report artifact for the first run, unchanged evidence for the second, durable home/transcript publication and **one threshold notification** across repeat publication of both outcomes. `TestBotNotifications` and waiting/final publication tests exercise suppression, thresholds, deferral, uncertainty and replay separately.
- [x] Persist-before-publish transcript events, authorized snapshot/delta SSE and cursor replay are implemented. Reading history or opening a stream does not execute the Bot.
- [x] Shared/resumed budget accounting, crash recovery without automatic effect retry, request-local scope and denied child escalation have named coverage. Production cost-capped models and purchase execution remain fail-closed where no conservative pricing or cap-enforcing adapter exists.
- [x] Production capability adapters include sandbox HTTP and MCP, browser operations, scoped memory/knowledge, plans, schedules, workflow tools, guarded drill execution and scoped email. MCP is **not absent**: separately authorized discovery and calls run in the pinned sandbox with configuration-bound identities. Supported drill steps use the terminally bound catalogue, not a host-fallback executor. Supported email calls use same-scope account settings and credentials. These statements do not mean every ordinary Chat handler is Bot-safe or supported.
- [x] Owner question capture, typed response persistence and serial post-answer ChatRunner continuation exist. `pkg/api/bot_runner_interaction.go` correlates the saved answer with the original tool invocation; resume consumes it before model dispatch and restores the conversation without resubmitting the original task. `TestBotQuestionSQLiteReopenChatRunner` exercises one prior read, question, owner answer, one subsequent write and final report across personal/team SQLite reopen, with terminal replay rejection.

The execution owner reports that all named runtime selectors now exist and pass, and that the latest full package suites passed after hardening. This documentation update does not rerun those suites or substitute a newly invented command, duration or log for the execution owner's recorded evidence. Preserve the actual command/output in the active execution record when closing a verification gate.

### What the successful journey does not prove

Authentication, model, tools, isolation preparation, home registration and notification delivery are controlled. Four distinct temporary-directory workspaces model the two starting attempts and two resumed attempts; they are **not four real sandbox containers**. A controlled HTTP receipt proves the fixture's deduplication contract, not Telegram/email/Slack delivery. Package success can coexist with skipped environment-dependent tests; a skip is not physical-home or provider acceptance.

## Remaining runtime implementation checklist

These items stay open during continued runtime execution. For each, implement the behavior and its acceptance evidence, or obtain an explicit scope decision that names the unavailable behavior. Merely retaining an unsupported stub or relabeling the whole runtime complete is not equivalent.

### 1. Continue a task after an owner answer

- [x] Extend `BotChatRuntimeAdapter.resumeInteraction` with a durable serial task continuation that injects the validated saved answer into the correct saved conversation, without resubmitting the task or replaying earlier tools.
- [x] Correlate the answer to the saved question/tool invocation and consume under the run's fresh fence before model execution. The answer is not an action grant; ordinary current policy and budget checks remain in the resumed path.
- [ ] Extend acceptance for repeated question chains, revocation/cancellation and uncertain post-dispatch failures specifically during interactive continuation. Existing invalid-answer, owner, transcript-correlation and terminal replay checks do not prove every transition.
- [x] `TestBotQuestionSQLiteReopenChatRunner` exercises both scopes through one observed read, question, SQLite reopen, typed answer, one post-answer write, final model report, another reopen and replay rejection.

**Remaining verification limits:** the continuation journey uses a controlled model and tool counters, not a production provider or sandbox. It ends `partial` because unverified-effect evidence is preserved, not because post-answer work is absent. PostgreSQL, repeated question chains and post-dispatch crash injection are not covered by this journey.

### 2. Detached tutorial and human-intervention lifecycles

- [ ] Add owner-bound durable checkpoint/resume handling for tutorial creator approval and browser human handoff; do not install interactive goroutine waits, host listeners or viewer credentials in a detached worker.
- [ ] Define wait, explicit answer/handoff completion, timeout, cancellation, authority revocation and restart transitions, including revalidation of sandbox/session ownership before resuming.
- [ ] Acceptance: detach/restart while waiting, prove no held execution resources or intervening tool dispatch, reject another user's response, then resume the intended work once under the current owner and budget.

`pkg/launcher/bot_workflow_tools.go` deliberately rejects `present_tutorial_blueprint` and `blueprint_to_tutorial_drill`; `bot_browser_extras.go` rejects native human handoff. This does **not** mean all drill execution or tutorial-mode ordinary steps are unavailable. `BotDrillTools` already exists; its unsupported human/visual-artifact paths and child-guard guarantees are distinct.

### 3. Safely compose remaining Fleet/template handlers

- [ ] Introduce scoped authority and storage/transport adapters before exposing global Fleet activation, setup review, task-board or disk-template behavior. Bind draft/task ownership and live membership; keep private, team and global resources distinct.
- [ ] Compose only handlers whose actual dependencies and effects are authorized by the Bot request. Tool schemas or a wrapper around a global handler are insufficient.
- [ ] Acceptance: exercise the intended operation in personal/team scope; deny foreign drafts/resources, revoked membership and personal-to-team fallback before mutation or external I/O; prove no daemon-global registry/activator authority is inherited.

`productionBotWorkflowTools` explicitly does not initialize global Fleet activators or disk template registries. Workflow wrappers reject unsafe Fleet operations; `get_setup_profile` is narrowly available without a draft ID. Scoped document authoring/template reads that already work must not be confused with this missing composition. Reviewed **Bot team templates** in the HTTP control plane are a separate, already implemented feature.

### 4. Bind email replies to reviewed recipients

- [x] Implement a recipient-bound immutable reply snapshot: account fingerprint, original-message identity/content and effective To/Cc/reply-all recipients are included in exact-action authorization and rechecked at the node before send.
- [x] Never resolve a mutable message to new recipients after approving only a message ID. Provider I/O uses the scoped node adapter; same-scope vault provisioning, post-provisioning live-guard checks and secret redaction remain in place.
- [x] Controlled acceptance: an approved unchanged reply sends once; changed account/message/recipient state, child invocation and revoked authority reject before send; an ambiguous HTTP 504 response cannot reuse the grant.

`TestBotEmailReplyGraphSnapshot` and `TestBotEmailReplySMTPSnapshot` exercise the recipient-bound adapter in `pkg/launcher/bot_email_tools.go`, with source/snapshot construction in `pkg/email/reply_snapshot.go` and pre-send revalidation in `pkg/tools/node_email.go`. The reply retains the ordinary Chat schema and requires a separately authorized source read, or the matching saved approval checkpoint. The checkpoint restoration fixture is in-process: it does not establish an actual worker restart, approval UI journey, deployed sandbox or real mail delivery. [Email verification](bot-email-tools.md#verification-and-limitations) records the commands and exact boundary. Email **tools** and Bot outcome **notification bindings** remain different contracts.

### 5. Obtain independently meaningful external receipts

- [ ] Add provider-specific trusted receipt/observation handling if claiming confirmed delivery or verified external effects. Preserve durable pre-I/O uncertainty and exact destination authorization; never turn an error-free function return into proof of delivery.
- [ ] Acceptance: verify an actual authorized provider receipt (and precisely what it attests), durable reopen/reconciliation, duplicate suppression and ambiguous-response handling without blind retry.

The current shared `ChannelManager.Send` API exposes no provider receipt ID. A nil transport error still produces an uncertain Bot notification receipt. Only private Telegram notification destinations are currently provable by the binding authorizer; Slack, email notifications, groups/threads and other unsupported destinations fail closed. The controlled HTTP threshold result remains useful evidence but does not close this production receipt gap. Successful remote-tool dispatch likewise does not by itself establish a trusted outcome observation.

## Separate physical-home and client acceptance gates

These are explicit remaining product rollout gates, not a reason to obscure the concrete runtime items above or to silently expand the runtime phase into a UI rewrite.

- [ ] **Real durable home:** validate image/layer prerequisites, run the supported live home acceptance, demonstrate fresh physical compute with retained home bytes across runs/restart, and prove mount isolation, teardown barriers, retained deletion, restore and authorized purge. No real-home acceptance is established here. The historical unsupported `bot-home` scenario/malformed image expansion and a skipped `TestBotHomeDockerPersistenceLive` are not evidence. Kubernetes/OpenShell require their own backend acceptance.
- [ ] **Bot Studio and terminal workspace:** implement and verify thread/runs/goals/schedules/memory/decisions views, approvals, unread state and reconnect behavior in empty/error/populated/paused/deleted states. Ordinary Chat compatibility and API SSE tests do not prove a Bot client exists.
- [ ] **Bot artifact PDF surface:** distinguish sandbox `browser_pdf` (implemented) from Bot transcript/report PDF rendering and authorized user download (not established).
- [ ] **Broader product scenarios:** future-run effects of memory corrections, physical cleanup/recovery and deployed provider billing/delivery remain separately demonstrated outcomes, not inferences from persistence or controlled runtime fixtures.

Keep the physical-home and client phases open in the overall rollout plan. If the runtime phase itself claims any of these outcomes, it must carry the corresponding live acceptance rather than borrow proof from a later phase.

## Next execution sequence and closure rule

1. Continue the runtime phase with durable post-answer continuation and its restart/no-replay acceptance; use that lifecycle to implement detached tutorial/human handling rather than inventing a second interactive runtime.
2. Complete or explicitly scope the missing Fleet/template adapters. Recipient-bound replies now have controlled adapter acceptance above; retain their documented external-delivery and restart limits.
3. Exercise receipt/uncertainty semantics against the intended real transport, or retain an explicit “delivery unverified” capability boundary; do not rename the controlled HTTP result as provider delivery.
4. Rerun the full affected packages after implementation and retain nonempty results for the named runtime acceptance, public activation/two ticks/reopen continuation, budget/recovery, notification and adapter-security cases. The existing bounded journey must continue passing while new continuation cases prove the expanded behavior.
5. Record exact remaining rollout limitations and leave physical-home/UI phases open. Only the execution owner should close Phase 3 through the existing plan workflow when its actual scope and verification obligations are satisfied. This document does not close it.
