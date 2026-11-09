# Legacy employee schedule storage proof

The employee legacy probe observes a copied launcher state with the exact legacy
`bot_id`; it does not replace the primary Bot/deletion state. `employee_readonly`
selects scheduler rows using exact decoded `payload.bot_id` within the owned
SQLite database. A whole-store diagnostic is not a scoped proof response. The Go
validator must reject foreign rows, not filter them a second time. Regression
coverage includes a deliberate primary job beside the two legacy jobs and proves
both selections and database bytes remain unchanged.

The failed `astonish-chat-first-pzhp38hv` attempt retained an API version 3 with
two triggers, but a disk snapshot at draft/version 2 with only the historical
scheduler row. A later offline read of that same retained SQLite database contains
the two version-3 legacy jobs. This establishes an observation mismatch, not its
underlying cause and not a duplicate job. After API application the probe allows a
bounded 30-second read-only convergence window; it neither repeats the message nor
mutates schedules. Missing or invalid evidence still times out as failure.

`employeeLegacyStorageTime` accepts RFC3339 API-fixture timestamps and the actual
SQLite Go-time representation (for example `2026-11-16 00:00:00 +0000 UTC`). Raw
observer evidence stays unchanged. Empty, malformed and zero timestamps fail.
An active new schedule needs a nonzero next run; a paused schedule needs null.
Both exact scheduler IDs and both exact trigger IDs must occur once. The original
historical trigger comparison and disabled/no-next-run/no-execution checks remain.

The minimized fixture at
`tests/e2eboot/chat_first_browser/testdata/employee_legacy_schedule.json` retains
saved API identity/trigger data, the stale one-job projection, and the later
read-only database's two-job projection. It contains no credentials. The Python
three-job case explicitly adds synthetic cross-Bot interference; it is not a claim
that the retained database had three jobs.

Checks (offline only):

```sh
go test ./tests/e2eboot/chat_first_browser -run 'TestEmployee(Legacy|Historical)' -count=1
python3 -m unittest discover -s tests/e2eboot -p 'employee_*test.py'
```

These checks do not establish seventeen-check live acceptance, delivery, daemon
restart behavior, or production external-adapter behavior.
