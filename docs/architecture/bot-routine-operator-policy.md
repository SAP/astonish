# Operator restrictions on Bot routine controls

The optional operator configuration is a restriction, not an execution grant:

```yaml
bot_routine_policy:
  require_approval: [memory_forget]
```

The only accepted selector is `memory_forget`. Unknown selectors and duplicates
fail validation. Omitted/empty policy preserves the current default authority and
all finite `bot_mandates` limits. This is separate from the numeric limits to
preserve their serialization and validation contract.

The production resolver partitions its **current owner baseline**: the exact
`memory_forget` own-control resource is removed from worker constraints and placed
in the trusted exception ceiling only if already present. No tools, wildcard
resources, IDs, personal data, access rights or capability grants can be provided
through this setting. Existing membership checks remain in force.

The existing exception runtime requires the owner's exact operation ID, payload
digest, revision and finite expiry. Approval does not alter the Bot's mandate;
subsequent operations require their own decisions. Revocation/expiry and current
policy are checked again before mutation.

Production daemon readiness also installs `BotAuthorizedOperationFactory` on the
HTTP service bundle; tenant middleware preserves this factory while replacing its
stores with the current request's scoped stores. The factory resolves those stores
at invocation, never capturing a worker tenant. An unavailable worker/policy still
fails closed. Decisions retain owner, source mandate, immutable exact grant,
payload digest, operation revision and current Bot revision checks. Denial persists
`cancelled` + `deny` with an `authorization_denied` receipt and removes pending/retry
metadata atomically. Reconciliation cannot turn denial into execution or issue
another request for the same denied source.

Suppression in `bot_routine_suppression.go` binds the normalized action/ceiling
fingerprint to the **verified persisted owner-source digest** (event, instructions,
attachments), not the model's reason, tool-call ID, new mandate ID, or Bot revision.
A distinct authenticated direct-owner instruction may capture a fresh operation;
under the same restricted ceiling it remains `blocked` / `require_authorization`
until the owner approves that new exact operation. This does not grant authority
from arbitrary new context: source/lease validation and the canonical-source
authorizer still run first, and natural-language relevance remains the owner
turn model's existing responsibility, not a keyword-based consent parser.
Scheduled runs, reconciliation and restarts cannot manufacture such a source.

Older bot-wide suppression booleans are interpreted using the reserved operation
head index and immutable denial receipts, without deleting markers or changing
prior receipts. An unexplained legacy marker fails closed. Exact capture replay
returns the original cancelled operation; altered narration/tool IDs within that
same source remain forbidden. New owner requests never revive the old operation.
`TestRoutineDeniedFreshOwnerException` covers both personal/team SQLite stores,
current/legacy markers, restart/ticks without another receipt, fresh bounded
capture, no auto-grant, exactly-once mutation and unchanged prior denial.

## Integration boundary

The production daemon composes the approved exact own-control baseline after
current-owner membership and network-read resolution, then applies this restriction.
The baseline includes ask_user, the control capture adapter, exact schedule tools,
and the closed routine operation resources (memory, goals, persona/responsibility,
priority pause, targeted resume and one task). It grants no configuration, spend,
external write, wildcard or legacy broad resume capability. Canonical direct-owner
source checks and immutable grants remain required. Scheduled child task mandates
strip schedule and routine-control resources, while paused turns keep only existing
non-task controls.

The smoke launcher's explicit JSON may include
`"bot_routine_policy": {"require_approval": ["memory_forget"]}`. It validates the
closed selector and writes the policy into its fresh isolated configuration before
daemon construction. Omission leaves the normal baseline unrestricted. User daemon
configuration is never changed.
