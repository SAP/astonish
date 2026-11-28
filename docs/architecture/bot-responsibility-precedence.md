# Responsibility precedence and owner timezones

The Bot prompt (`pkg/agent/bot_prompt.go`, `applyBotPrompt`) guides how a run
interprets its pinned assignment. It does not grant permissions or replace typed
runtime authorization, bounded approvals, or committed receipts.

## Which responsibility applies

- Applicable charter and goal constraints pinned for this occurrence take
  precedence over conflicting older trigger wording, including reporting dates,
  cutoffs, and quality standards. A saved scheduled task is not a fresh owner
  instruction merely because the runner presents it as the user turn.
- Preserve task-specific scope and non-conflicting details. A broad charter does
  not expand an unrelated task. Report unclear applicability or irreconcilable
  requirements instead of silently choosing old wording or inventing criteria.
- An admitted run keeps its pinned contract through continuation and correction.
  Newly admitted occurrences use the newly committed responsibility snapshot;
  pending proposals and historical assistant claims are not replacements.
- A responsibility update does not rewrite every trigger, change cadence, replace
  unrelated tasks, or replay prior occurrences. If a targeted schedule edit is
  needed, explain it and use a supported, separately authorized operation when
  requested. `update_scheduled_job` cannot edit task instructions.

## Wall-clock rules versus cadence

For a rule such as “after 4PM report the next day's news,” use an explicit timezone
in the request or a previous owner selection evidenced in trusted owner context.
A schedule, persona, or server default—even UTC—and earlier assistant assumptions
are not evidence of the owner's preference. Reuse a known selection without
redundant confirmation.

If the timezone is missing or conflicting, clarify before saving or interpreting
the temporal responsibility; Chat should use `ask_user` when available. An
unattended run reports the missing timezone as a blocker, not an interactive
question or a guess. An already committed explicit timezone remains pinned for
that attempt. Suspected earlier assumptions need clarification and an authorized
correction, not silent rewriting.

Relative cadence, such as every two minutes, does not require inferring an owner
wall-clock timezone. Do not change that cadence to resolve a content cutoff.

## Confirmation in Home

A supported routine responsibility update remains internal while queued or
applying. Only the committed, verified metadata receipt produces the ordinary
assistant confirmation “I've updated my responsibilities.” The source's pure
`control_pending` acknowledgment must not become a second bubble or technical
card, including after reload or a later report. See
[confirmed routine-control conversations](bot-routine-control-presentation.md)
for receipt, publication, and authorization-exception details.

`TestBotResponsibilityConfirmedOnlyBrowserAcceptance` exercises built Studio with
a controlled model, real tool/worker/HTTP/SSE paths, and isolated SQLite. It checks
pending live/reload, exact committed charter readback, one confirmation, and
ordering after a later report live/reload. This is not live-provider acceptance
and does not modify a user's daemon or data.
