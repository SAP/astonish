# Paused writer recovery after daemon restart

## Authenticated owner-message priority

A newly admitted owner message transactionally quiesces only the current running
attempt when it is autonomous (non-home-interactive presentation or cron trigger).
Idempotent replay, denied/cross-scope requests and failed admission transactions
have no cancellation effects. Another interactive owner turn is never preempted.
The shared exact-run cancellation primitive revokes approvals and advances the
root fence without clearing `ActiveRunID`. Scheduling remains enabled until an
authorized pause operation executes; admission itself is not a global pause.

The worker visits teardown, owner-turn and background lanes across all pages in
that order. The transactional claim guard independently refuses autonomous claims
while a home-interactive owner turn is queued, including competing schedulers.
Verified destruction of the exact old session remains mandatory before the new
owner writer can claim. No new parsing, authority grant or approval bypass is
introduced by message priority.

The first transition to paused revokes the running attempt's lease, increments
the root fence, and leaves its active-run barrier intact. A queued conversation
is not a writer and must not prevent an exact teardown receipt from releasing
that barrier.

## Repeated priority pause during an explicit conversation

`botAuthorizedOperationRuntime.pauseRoutineTx` treats `priority_pause` against
an already-paused Bot as lifecycle-idempotent. It still disables schedules and
persists an applied operation receipt, but does not advance the Bot revision or
root fence, quiesce runs, revoke approvals, or emit another lifecycle cancellation.
The normal scoped lease, mandate, provenance and operation authorization checks
still precede this branch. Idempotency holds both for a repeated tool-call key
and for a newly issued pause call with a distinct key.

| Current lifecycle | Authorized priority pause |
| --- | --- |
| Active or draft | Transition to paused; retain first-pause fencing and cancellation. |
| Paused | Preserve explicit conversations and their leases; autonomous admission remains blocked. |
| Stopped or deleted | Reject; do not revive the Bot. |

This is not an implicit resume or an exemption from explicit cancellation.
Exact-run `CancelRun` still stops an owner conversation. No natural-language
intent matching or authorization bypass is involved.

For each model invocation, `botFactoryBinding.currentControlContext` fetches
current owner-scoped lifecycle state and supplies `lifecycle_status`, `run_id`,
`user_event_id`, `intent` and `execution_mode` alongside current memory/control
metadata. The prompt distinguishes the current user question from earlier pause
requests and tool calls, and instructs the model not to repeat `priority_pause`
merely to confirm the earlier pause. Request history is not rewritten. The
priority-pause tool receipt describes paused autonomous work without falsely
claiming that every applied pause fenced the source conversation.

Regression coverage: `TestRoutinePauseAlreadyPausedConversation` verifies both
personal and team scopes, repeated and distinct operation keys, a surviving
conversation lease and successful commit, rejected autonomous admission, and
explicit exact-run cancellation. `TestRoutinePriorityPauseLifecycleMatrix`
checks first-pause interruption and rejected inactive states.
`TestBotCurrentControlContextPausedConversation` covers legacy and interactive
execution modes, current memory, lifecycle/provenance metadata and unchanged
history. `TestBotPriorityPauseToolReceiptDoesNotClaimConversationFenced` checks
the receipt language and source lease/tool-call provenance.

## Exact teardown recovery

`BotWorker.pollStore` revisits interrupted runs, including those from a previous
worker process. `confirmCancelledRun` permits a foreign worker only when its
lease is revoked (zero expiry) or expired; running attempts and still-leased
foreign attempts are not adopted. The authenticated owner must match the Bot.
There is no re-execution, cancellation request, or physical deletion in this
recovery path: it observes completed teardown only.

The optional backend `VerifySessionDestroyed` capability must prove physical
absence of the exact stored run session. Docker requires explicit container
not-found; Kubernetes checks physical pod deletion. Registry absence, expired
leases, cancellation, stopped containers, and deletion requests are not proof.
Backends without this capability retain the barrier, with no host fallback.
If the old physical session still exists, this recovery deliberately does not
remove it; trusted lifecycle teardown must finish before a subsequent poll can
confirm it.

`BotCleanupStore.ConfirmCleanup` still checks service-only tenant scope, exact
home/revision, active run, run fence and root fence before verification and again
transactionally afterward. The daemon requests only the immediate revoked
fence (`RunFence+1`), not an arbitrary later lifecycle generation. Queued/waiting
work does not block a run-teardown receipt in paused state, just as in active
state. Home retirement still requires all busy work absent; a run receipt never
authorizes deletion of the retained home. Revoked approvals are not restored.

Evidence: `TestBotWorkerPauseRecoveryAfterDatabaseReopen` exercises both SQLite
scopes, pause during a claimed run, a queued conversation, database close/reopen
and replacement-worker polling, failed physical verification, successful exact
verification, stale-writer rejection and the conversation's new fenced claim.
The poll fixture hides queued runs only to isolate claim assertions from model
execution. `TestBotIntentHTTPWorkerConversation` separately covers the production
conversation runtime's zero external tool effects. Backend verification is a
controlled fixture here, not live Docker or process-restart acceptance.
