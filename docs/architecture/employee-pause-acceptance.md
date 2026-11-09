# Employee pause acceptance

The employee harness distinguishes stopped execution from resumable history.
`RunInterrupted` deliberately remains nonterminal in production; interrupted
history alone must not hold the harness idle wait open.

`chat_first_browser/newemployee_idle.go` consumes the owned SQLite observer:

- Exactly one matching Bot must have explicit NULL `active_run_id` and
  `lease_expires_at`.
- Every owned run must have explicit NULL `lease_expires_at` and an indexed status
  of interrupted, succeeded, partial, failed, skipped, or cancelled. Queued,
  running, waiting-approval, unknown, and missing statuses block idle.
- Missing ownership columns never stand in for NULL. Even expired leases must
  be cleared. A failed run is physically idle only after ownership clears; it
  never proves a successful result or an applied operation.

The observer projects only these extra indexed ownership columns for the selected
Bot and its runs, under one read-only SQLite transaction. Indexed columns are
authoritative: stored Run JSON and historical worker/fence IDs can remain after
fencing. Production's `active_run_id` is also the teardown/admission barrier;
`ConfirmCleanup` clears it only after verified teardown. There is no separate
run-teardown boolean. `cleanup_complete` is Home-retirement state, not a pause
prerequisite. Pending question/approval records are independent of compute;
a waiting-approval **run** still blocks this scenario's idle gate.

The pause gate still first requires the applied pause operation and paused Bot.
It then requires cleared execution ownership and all observed recurrence jobs
paused with explicit NULL `next_run_at`. This scenario already created a
recurrence, so an empty job list cannot satisfy the gate. The receipt includes
`employee-paused-before.json`, preserving the actual ownership/job evidence.
The later owned restart and 130-second no-new-run due-boundary gate is unchanged.

Tests: `go test ./tests/e2eboot/chat_first_browser -count=1` and
`python3 -m unittest discover -s tests/e2eboot -p '*test.py'`. These are harness
predicate/projection tests, not live acceptance or independent goroutine
inspection. A blocked live receipt is not retroactively upgraded by this fix.
