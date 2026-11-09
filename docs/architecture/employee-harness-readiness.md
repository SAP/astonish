# Employee harness readiness

The disposable SQLite/Docker employee harness (`tests/e2eboot/local_bot_smoke_launcher.py`)
waits up to 300 seconds by default for startup and restart. Local 132-document
embedding initialization has measured 165–180 seconds; this is not evidence of a
mutex hang. `LOCAL_BOT_STARTUP_TIMEOUT_SECONDS` accepts integers 1–900 and is
validated before creating a root. Ownership and process errors are not retryable.

Every ten seconds, stderr reports elapsed time, deadline, and a classified phase
from the owned daemon log. The log is not dumped, avoiding credential disclosure.
Sandbox completion without tool-index completion is labeled pending initialization,
not a proven hang. A missing log is an early-start state, not successful readiness.

Readiness requires a typed unauthenticated `/api/auth/setup-status` response:
HTTP 200, boolean initialized, and builtin auth. `/health` is unsuitable for this
candidate because the Studio SPA fallback returns HTML with HTTP 200. Errors,
malformed JSON, and non-200 responses cannot satisfy the gate. Restart callbacks
also require a changed Docker StartedAt and retain their no-replay checkpoint.

Explicit employee state resume preserves the root, credentials, Bot/checkpoint,
and candidate hash. The config must match canonical owned state. Resume is always
diagnostic and cannot count as a fresh final release invocation. No automatic
candidate repinning or provider database writes occur: explicit provider settings
are published only through authenticated platform settings after readiness.

Tests: `python3 -m unittest discover -s tests/e2eboot -p '*test.py'`.

Fresh launches default to `docker-native-image-v2`. Host Python creates a tar
containing only the Linux candidate, Linux Docker CLI, launcher wrapper, Dockerfile,
and (when explicitly configured) all six hash-pinned embedding assets plus a checksum
manifest. Assets are reverified before packaging and by `sha256sum -c` inside the
offline image build, then stored read-only at `/opt/astonish-models`. Only after the
build succeeds, the exact fresh owned model subdirectory is replaced with an absolute
link to that native path. The product's existing XDG model lookup follows this link;
no invented environment variable or production change is involved. The host source
cache stays untouched. Image labels pin the manifest hash; resume rejects changed
links, model hashes, and mounts shadowing native assets.
Host Docker sends those bytes via `docker build -` (240-second bound).
The launcher selects the classic local-image builder: BuildKit interprets local
`sha256:` IDs in FROM as registry tags. Builds use a private empty Docker config
and the resolved local Unix Docker endpoint, without copying registry credentials
or invoking unrelated credential helpers. Engines without this builder or a local
Unix endpoint fail closed; this harness targets local Docker acceptance.
The existing base is resolved to an immutable image ID, never retagged. A unique
launcher-owner + candidate-SHA tag is created; an existing tag is rejected, not
overwritten. Owner, candidate SHA, CLI SHA, and mode are image labels and are
checked against the owned state on resume. The image is retained for diagnosis;
there is no prune, automatic deletion, or user-image mutation.

The daemon runs `/opt/astonish`; the shared product entrypoint uses the identical
candidate at `/usr/local/bin/astonish-host` for child nodes. Both, the Linux Docker
CLI, and the launcher wrapper live in image layers, not host executable mounts.
`ASTONISH_SANDBOX_IMAGE` pins child launches to the same immutable image ID; the
wrapper rejects unowned/unpinned child launches. The base entrypoint is inherited.
Old owned state without `candidate_storage` retains its previous compatibility
path; it is not silently migrated or repinned.

All existing explicit configuration fields remain unchanged. Root/data, SQLite,
and sandbox layers still use existing host storage. The configured model assets
and executables no longer require runtime host file reads; this does not remove
every host file-sharing dependency. A
host-data stall must remain a reported blocker, not a false readiness pass.
Live employee acceptance is still required separately; readiness alone proves no
schedule, model invocation, approval, or recurrence outcome.

## Owned storage observer

`employee_readonly` opens only the exact Bot's database below the owned disposable
root, with SQLite `mode=ro` and `query_only`. Its per-table allowlists are checked
against both personal and team Ent schemas. `bots.home_binding` is projected only
for the selected Bot; neither peer documents nor credential/encryption/embedding
columns are returned. Text JSON stays unchanged; JSON BLOBs retain exact byte
sidecars and strictly decoded UTF-8, including Home bindings consumed by Go's
physical-Home identity gates. Absent optional bindings are not synthesized.

Run session discovery accepts flat, `run`, and persisted `Run` documents, optionally
inside one `bot_document` JSON-string wrapper. Duplicate/case-aliased boundary
members, recursive/mixed wrappers, and row/document identity conflicts block the
observation rather than providing fallback evidence. A session from a run requires
matching document/row Bot and run IDs and, where present, the indexed session ID.
Raw run evidence remains unchanged; decoding does not establish execution success.

Only session identity columns are consulted for ancestry. Peer Bot Home/run IDs
and explicit session Bot/run attribution exclude cross-Bot traversal. Retained
session IDs permit observing cleanup after deletion; copying state to select a
peer with a different Home does not copy the original Bot's session cache.
Persisted ADK `Actions.StateDelta` and lower-case projections share the same
operation extraction; ambiguous containers fail closed. Approval rows remain
opaque scoped evidence for deletion, and memory gates use the authenticated
memory API, not a broader database projection.

## Employee operation polling

`chat_first_browser/newemployee.go` shares `employeeOperationPoll` between natural
schedule creation and later cadence, memory, pause, paused-goal, resume and
exception capture gates. Each iteration reads runs **before** the snapshot:
operation capture precedes source completion, while worker application can follow
it. Only the expected operation type with matching Bot, Home, **admitted target
run** and nonempty matching user event counts as captured; old operation IDs remain
excluded. A new scheduled run (or another manual turn) is not the target.

`newemployee_admission.go` refreshes and reviews the current version through
Details → Refresh Bot before typing. It requires the unchanged Home URL, empty
initial composer, exact intended input, enabled Send and no visible alert; it
saves these states. Clicking Send only begins a bounded admission wait. A read-only
finite Home transcript must contain the exact user text under the event ID of a
new manual/Home-interactive run with a persisted history cursor, and the composer
must clear after the UI receives its receipt. Duplicate admission, UI rejection,
wrong Home or timeout fails closed without another send. The observed run is
saved separately from the later operation receipt; no hidden message/API POST is
used for admission. UI freshness can still race another version change: such a
rejection is reported, never automatically retried.

A captured pending/applying operation keeps the bounded wait alive even when its
target source is terminal. Applying never fabricates pending evidence. A terminal
target without a matching current operation fails immediately, as do its failed,
cancelled or unexpected blocked operations. All target captures are checked before
returning any ready operation, so a sibling from that target cannot hide a failure.
Unrelated runs/operations can neither satisfy nor fail the gate, including during
pending admission. The explicit exception gate alone expects blocked + require_authorization;
ordinary gates require applied + execute. No polling result grants authority.

Existing durable schedule/trigger/job/next-run, memory, Bot lifecycle, exception
and receipt validators remain mandatory. Unit tests cover the recorded cadence
pending-to-applied sequence and mixed queued runs via
`go test ./tests/e2eboot/chat_first_browser`. Passing these tests does not replace
fresh live employee acceptance or upgrade previously blocked receipts.

## Preservation-peer creation barrier

The deletion stage records `peer-create` before its single UI submit. It does not
retry creation if the response, navigation, or rendering is delayed. A bounded
30-second browser promise observes visible validation errors, dialog closure,
the exact owned-base personal Bot route (no suffixes), and the mounted workspace's
matching personal `StudioChat.botThread.botId`. The old Home remaining visible is
pending, not proof of creation. No navigation or application state is forced.
The observer reads the existing React host-element props because the workspace has
no DOM Bot-ID attribute; a changed/missing props shape fails closed rather than
falling back to a URL-only check.

An independent read-only before/after Bot inventory must identify exactly one new
owned peer with a distinct Bot and Home, the submitted title, and matching route.
Existing peers and multiple new peers cannot be substituted. Before memory seeding,
the existing bounded terminal wait correlates exactly one successful title run to
the persisted Home user event containing `Preservation witness`. The read uses
`GET /api/studio/bots/{peer}/sessions/{peer-home}/stream?scope=personal`, the finite
persisted transcript endpoint, not `/api/studio/sessions/{home}` (a JSON display
summary without event IDs). The root API closure uses the supplied path verbatim;
no primary-Bot snapshot/run closure is reused for this peer read.

Empty/incomplete snapshots or an as-yet absent source event remain pending within
the six-minute bound, returning no admitted run. A complete `done` frame with
`persisted=true`, exactly one title event, and exactly one matching manual Home
interactive run with its recorded event ID and positive history cursor are required.
The admitted run/event/cursor must remain identical until success. No assistant
response, JSON message count, or terminal status alone substitutes for provenance.
Malformed JSON/SSE, duplicate events/runs, source mismatch, and invalid completion
counts fail closed. HTTP errors (including 404) retain the exact GET path/status and
are not presumed transient. Each observation saves path, elapsed time and run state;
HTTP-200 shape errors include path and elapsed time. A timeout remains a failure,
never a receipt. Actual delete confirmation, durable cleanup and byte-for-byte peer
record/volume preservation assertions remain unchanged.

The sanitized `testdata/employee-peer-session-summary.json` preserves the saved
failure's response shape without live IDs/timestamps. Unit coverage distinguishes
that wrong endpoint shape from pending snapshots and verifies actual ADK event
encoding, duplicates, wrong source and endpoint wiring; it does not prove a fresh
native create-to-delete journey.

Regression coverage: `go test ./tests/e2eboot/chat_first_browser`; opt in to isolated
native-browser observer tests with `ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1`. These
DOM fixtures are not a live employee journey, and do not establish deletion success.

## Deletion payload boundaries

`employee_peer_payload.go` records each peer GET's artifact source, path, HTTP
status and Content-Type. JSON inventory/snapshot/memory/run/session-summary
responses and finite persisted SSE are distinct typed payloads: JSON cannot
satisfy title provenance, and SSE is never passed to a document decoder. New
transcript artifacts use `.sse`; old saved `.json` artifacts are not rewritten.

The independent deletion observer decodes `bots[0].home_binding`, optionally
inside its single `bot_document` wrapper, as `store.BotHomeBinding`.
`pkg/daemon/bot_home_setup.go` registers `StorageKey: actual.Ref`: on this Docker
path the field is the plain volume name, not serialized `sandbox.BotHomeBinding`.
`employeeHomeVolume` checks binding identity, personal scope, Docker backend,
revision and volume-name syntax and returns that opaque key without a second
JSON decode. Disk parser errors retain the artifact name and field path.

`TestEmployeeDeleteSavedArtifact` optionally reads a saved artifact directory via
`EMPLOYEE_DELETE_SAVED_ARTIFACT`; it reproduces the former field decode error,
checks the corrected disk-to-volume mapping and exact historical title run/event/
cursor, without opening its database or resuming the workflow.
`TestEmployeePeerDeleteParserBrowser` uses built Studio, production HTTP handlers,
a disposable Ent SQLite store and the Python read-only observer. It creates one
peer via UI, validates actual finite SSE admission, deletes an inert target via
UI, and asserts unchanged peer history/rows and storage-key mapping. Its peer
run remains queued; its registered storage reference is controlled, not a real
Docker volume. This isolated test does not claim provider execution, terminal
success or full live employee acceptance.

## Final stopped-run evidence

The retained `terminal-evidence` gate name means **stopped execution**, not that
all historical runs succeeded or became non-resumable. Immediately after the
`delete-before` capture and again at the final gate, `employeeFinalStoppedEvidence`
uses `employeeExecutionIdle` on that exact capture. The earlier idle poll alone
cannot prove the later capture, and empty post-delete rows cannot prove teardown.
The same Bot must have explicit SQL NULL active-run and lease columns; every
recorded run must belong to it, have an explicit NULL lease, and have an allowed
stopped indexed status. Missing history, omitted/malformed lease evidence,
running/queued/waiting/unknown states, or cross-Bot rows fail closed.

Interrupted history remains `interrupted`, including after pause fencing; the
production `RunStatus.Terminal()` contract is unchanged. Receipt detail reports
counts of every indexed status, without converting interrupted, partial or failed
runs into success. Stored run documents remain untouched and retain strict
parsing/identity validation, while indexed status remains authoritative after
fencing. Actual operation outcomes and all prior gates still require their own
receipts; this ownership check cannot substitute for them.

Regression tests cover one interrupted plus thirteen succeeded runs, ownership
and missing-field negatives, exact capture wiring, and unchanged raw history.
Launcher tests reject missing final receipt fields and all thirteen-receipt
results, even if their outer status is changed to verified. Previously blocked
live reports/receipts remain failures; passing these unit checks does not retrofit
them or establish a fresh fourteen-gate live acceptance.

