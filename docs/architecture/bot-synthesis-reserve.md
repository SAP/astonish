# Bot final synthesis and accounting-only execution

## Purpose

Production uses `NewRunAccounting` / `RestoreRunAccounting`: token, tool,
model-cost and active-runtime totals are accounting, not execution allowances.
Historic `BudgetSpec` caps do not stop research or trigger final synthesis.
`newBotSynthesisReserve` reads the live meter's `TokenLimit()`, which is nil for
production accounting; it does not resurrect a saved token cap. With neither an
effective token limit nor a deadline, `prepare` appends no reserve guidance.
In particular it never instructs production to preserve "unlimited" tokens.

Authority expiry remains authoritative. When an authority deadline exists, the
request-local guidance describes only that deadline and synthesis time; it does
not invent a token allowance. Cancellation, revocation, lease fencing, operation
timeouts and generation-health monitoring remain active. Model context/output
capacity is a per-request constraint, not a whole-task execution budget.

## Finite legacy helpers: estimation and admission

The following token-pressure policy applies only to explicitly finite helper
meters (`NewRunBudget` / `RestoreRunBudget`), retained for compatibility tests.
It is not production accounting behavior. A failed model admission after
switching to final mode is not successful synthesis.

The factory asks the wrapped model for its request-scoped input estimate.
`botProgressLLM` forwards this query to the same `BudgetLLM` estimator used for
admission; enabling `progress_v1` must not revert reserve decisions to a separate
cold estimator. Provider-reported prompt usage can bound an unchanged prefix,
while appended evidence is charged conservatively. Replacing prior history or
changing the model configuration invalidates reuse of that measurement.

Reserve guidance is appended as request-local user context. It does not mutate
the caller's history or stable system instructions. Changing remaining-budget
text must not repeatedly discard the useful measured history prefix.

The stopping threshold needs room for both the next research request and a
subsequent final request, including their output ceilings and conservative
guidance bounds. The final request removes tools and tool configuration. That
configuration change can invalidate the research measurement, so the final-input
allowance must cover its cold estimate rather than assume the cheaper research
estimate applies twice. Arithmetic must remain overflow-safe.

Once final mode begins, it is sticky: no further research, delegation, or producer
tool execution is allowed. The separate, deterministic file-completion IO described
below is not a return to the producer tool loop. All model calls still use the
original budget's reservation and settlement checks. Progress dispatch, generation health checks,
live authorization, and final-answer validation remain in the call path. A
reserve cannot promise completion after arbitrary external spending, enormous
new evidence, or provider failure; normal admission must still fail closed when
the remaining budget cannot cover the request.

## File-deliverable completion

The employee can select `set_delivery` using `bot_work_update`: `chat_only`, or
`file_deliverable` with `markdown` or `binary` format. This is explicit assignment
interpretation, not a length/extension heuristic or permission grant. Once selected,
file delivery and its format cannot be downgraded within the run. Finished work
with required file delivery must carry an immutable saved-file reference.

After a complete accepted answer, a missing Markdown delivery permits one local
completion recovery. The production factory saves the entire accepted answer to
`outputs/<run>-report.md`, reads back and hashes the actual bytes, then snapshots
through the scoped `BotFiles` service. It returns trusted run-local receipts, not
model-supplied JSON. The operation has a ten-second ceiling, records the existing
tool accounting, and rechecks live lease/authorization and the shared meter. Autonomous
recovery also requires both `write_file` and `publish_file` to be already allowed by
the reviewed run policy; it cannot request or consume an exact-action approval. It does
not invoke a model, research tool, shell, or external action. Oversize content is
rejected rather than truncated. Cancelled, incomplete, uncertain-effect, and binary
results cannot use this text recovery. A binary request needs an actual generated
file, never text renamed to a binary extension. Failed recovery never claims success.
Canonical publication still commits the accepted artifact envelope separately.

`recoverBotFileDelivery` checks the captured final-event identity, no completion
error or pending controls, and the current work boundary before invoking recovery.
The factory callback shares a single attempt flag across its consumers: even a
failed attempt cannot become an unbounded retry loop. It uses create-only output,
reserves `save_file_delivery` from the existing tool ledger, validates write size
and digest, then reopens the file and verifies read/close success before snapshot.
The resulting receipt must match this run's `runs/<run>/` namespace, Bot/owner,
Home, artifact content kind, digest and size. Saving alone does not emit a card;
only [canonical postcommit publication](bot-home-assessment-publication.md#canonical-file-publication)
delivers it. These checks do not certify factual quality or external effects.

Agent-led Bot summaries are no longer expanded from report bodies. Ordinary Chat
report resolution is unchanged. Recovery replaces only its own full-source chat
answer with a short, accurate saved-source summary.

## Trust boundary

Only a fully consumed, successfully settled response with positive authoritative
prompt usage may establish a measured prefix. This is not a learned bytes/token
ratio or a cached-input discount. Changed model, system instructions, tools,
configuration, or history invalidates the observation. Media and cached content
still require a model-aware estimator; custom estimators remain authoritative
and their errors must not be swallowed.

The factory marks only its new request-local guidance message through
`WithBudgetInputGuidance`. Request identity, last-message pointer identity, and
a digest must match before excluding that message from prefix matching only.
User or tool prose is never recognized as guidance by wording. Copied requests,
mutated marks, and unmarked lookalikes fail closed.

The observation retains the entire measured prompt count, including previous
guidance. Current guidance is charged again at its full byte bound; no unknown
old token contribution is subtracted. Stored history contains only hashes and
counts behind a per-decorator mutex. Estimation alone neither learns nor spends.
Overflow must fail rather than replace a larger measured count with a smaller
byte count.

## Regression coverage

`TestBotSynthesisReserveAccountingIgnoresHistoricCaps` restores production
accounting beyond historic token/tool/runtime caps (including 13 retained tool
calls). It verifies tools and output ceiling remain intact, no quota guidance or
nonexecution receipt is produced, a tool dispatch still runs, authority-only
guidance is retained, and cancellation is not bypassed. The finite-helper tests
below verify compatibility only; they do not establish production quotas.

`BotChatSafeResources.SynthesisNow` is an optional trusted composition clock for
only the soft stopping policy (nil uses real time). It cannot change the run
budget, lease, authorization, or operation deadlines. The HTTP receipt regression
advances this clock after the real research read and before the rejected Task
update, rather than sleeping near the hard deadline. It retains the reviewed
10-second hard limit, three model calls, one actual tool dispatch, and exactly-one
final publication checks for both personal and team Bots.

`TestBotSynthesisFactoryMeasuredPromptContinues` uses the factory model chain,
a real restored `RunBudget`, a metered deterministic provider, and the existing
progress-dispatch fixture. It activates `progress_v1` on the captured factory
binding after construction to test the generation branch independently of the
durable runner's context-restoration requirements.

The regression verifies:

- Provider dispatch and settlement occur for every generation, with generation
  health active and no whole-generation age deadline.
- Repeated same-prefix research continues despite updated budget guidance.
- Changed history remains conservative and switches to final synthesis sooner.
- Successive research calls spend from one ledger until genuine pressure starts
  final synthesis; the tools-disabled provider call returns a complete answer,
  rather than failing admission or resetting the budget.
- Input and output usage rise by exactly the provider-reported amounts, tool
  usage remains retained, and total usage never exceeds the original limit.

Run the focused regression with:

```sh
go test ./pkg/launcher -run '^TestBotSynthesisFactoryMeasuredPromptContinues$' -count=1
```

This is a deterministic model-chain regression, not a live-provider or durable
progress-store integration test.
