# Docker retained Bot-home artifact reads and purge

`BotHomeBackend.EnsureBotHome` remains the optional provisioning contract.
`BotHomeArtifactBackend` and `BotHomeDeleteBackend` are separate optional
capabilities; their presence is **not authorization**.

## Session-owned file IO

Routine file/content/workspace operations execute in the Bot's one bound normal
session container, using its configured Python tool layer. Binding is metadata-only
and backend-instance-local. A missing launch recipe fails closed: trusted launch
composition must create the session with its actual template before file codecs
run; file access must not silently create an @base-only session. Existing recovery
recipes retain template, owner, resource limits and fresh-credential requirements.
Codec IO inside SQL never re-enters the session repository: stopped, paused or
missing compute fails with a lifecycle-preparation error there. Authorized content
hydration outside SQL may prepare the original HomeSessionID through
`BotHomeSessionPreparer`; this uses normal StartSession/WaitForSessionReady and
never fabricates a recipe or creates a file-helper container. HTTP message and
regular/scheduled run admission hydrate after scope/owner authorization but before
SQL, then repeat admission checks transactionally. Bot Get/List remain metadata-only
and do not prepare compute; file-backed discovery reads prepare only when needed.

Production `BotWorker.execute` first reads only the authorized Bot control row,
rechecks owner directory membership, and restores the existing HomeSessionID launch
recipe before run/version hydration. Initial provisioning pins the actual version
sandbox template before selecting staged content into Home. Title-only turns stay
on staging and create no persistent compute. Run-session IDs remain transcript
identities; their pool aliases and run cleanup never destroy the shared Home client.

`TestBotNewMessageDockerBetweenCallReproduction` runs the production worker and
canonical factory with a controlled (unpaid) model: title question, long new message,
real retained file read, persisted follow-up question, and exactly one physically
inspected Home-session container. It additionally evicts only that test container
while retaining its launch recipe, posts a new HTTP message after eviction, then
independently evicts again before executing the admitted worker turn. Passing this
sequence proves admission recovery and worker recovery separately. It is not proof
of a full scheduler polling cycle after eviction, full recipe deletion/reconstruction,
long-model deliverable generation, or Kubernetes support.

Each operation owns one exec process, not a container. Cancellation marks a unique
exec token and kills only its matching PID; the shell reaps it and cleans its token.
Concurrent unrelated execs and the session container remain running. Normal file
operations create no helper containers. Initial Home/admission, archival and the
legacy bounded artifact reader still have their separate administrative helpers.

The Home and workspace-private volumes remain independently retained across
DestroySession. Descriptors select their roots, but are **not a security boundary**:
the Python interpreter comes from the mutable session overlay. Content stored there
must not be treated as trusted approval/authority metadata. SQL authorization and
commit barriers remain external to this content. Existing helper-inspection tests
remain relevant to archival/admission, not routine file IO.

Physical acceptance: `TestBotSessionFilesDockerLive` requires explicit
`ASTONISH_TEST_DOCKER=1`, `ASTONISH_SANDBOX_IMAGE`, absolute
`ASTONISH_BOT_HOME_TEST_LAYERS`, and `ASTONISH_BOT_HOME_TEST_TEMPLATE` naming an
already provisioned Python tool layer. It verifies five warm cycles with zero
helper creation, exact private/workspace bytes, isolated cancellation, and changed
working bytes plus unchanged published bytes after compute destruction/recreation.
All five binary private revisions and the last workspace write are read back after
recreation, with stream and resource cleanup errors checked.

The historical `TestHomeFileTransactionHelperDockerLive` and
`TestBotHomeFilesDockerLive` still construct unbound helper-only backends and are
not session-cutover acceptance: their helper-count/start/remove fault assumptions
must be ported before including them in a full physical suite. They are retained,
not silently deleted or relabelled as passing. The scoped unit suite passing with
Docker opt-in unset does not establish that these historical live tests pass.

## Offline artifacts

`launcher.BotHomeManager.ReadArtifact` authorizes the caller using its scoped
`BotStore`, verifies the complete registered home and artifact HomeID, and checks
SHA-256 when metadata supplies it. The manager caps reads at 16 MiB. It never
calls Ensure, binds a pool, or recreates a logical/physical run for a download.

The Docker backend verifies the scope-derived volume name, scope/version labels,
local driver, and absence of driver options. A disposable internally named
container mounts only that home read-only. It runs a fixed shell reader from the
configured sandbox image, without network, capabilities, writable rootfs, or user
commands. Pulling images is disabled. Its exact mount is inspected before start.

Paths must be canonical home-relative paths. Resolved paths and the opened file
descriptor must both remain beneath the mounted home. Only regular files are
returned. Container reads and daemon CLI output are bounded independently; errors
return no bytes. Reads have a 30-second deadline. The helper is removed with an
independent 15-second cleanup context after checking its nonce and scope labels;
cleanup failures are errors, not successful downloads. No Docker mountpoint is
opened from the daemon host.

## Authorized HTTP downloads

The existing Studio artifact download and content endpoints resolve Bot run
sessions through the scoped session/Bot stores before reading retained storage.
A report artifact is selected from that run's persisted `Report.Artifacts` by its
canonical relative path (or `/root/`-prefixed sandbox spelling). The URL is only a
selector: home ID, Bot identity, storage reference, relative path, and SHA-256
come from the persisted records. The home must still match the Bot's registered
binding. Team members retain authorized history reads; storage scope uses the
recorded Bot owner, never the viewer as an execution-owner substitute.

The request-scoped production backend factory supplies the optional
`BotHomeArtifactBackend` directly, avoiding an API/launcher dependency cycle.
No provisioning, pool binding, run resume, or daemon-host reads occur. Returned
bytes are bounded and digest-checked before publishing. A failed retained read
cannot fall through to stale transcript bytes. On deployments lacking the
optional reader, transcript fallback for a report artifact requires the same
recorded digest. Ordinary Chat artifact tiers and Bot PDF-render restrictions
are unchanged; Bot home-session transcript artifacts remain transcript-backed.

For report artifacts carrying `ContentRef`, both existing session content/download
URL aliases use `readRetainedBotArtifact` and open the immutable content object via
`BotHomeFilesBackend.OpenBotContent`, never the current workspace `RelativePath`.
The request path is only a selector. Persisted Bot/run/Home/scope, content kind,
digest and size must agree; streamed entry metadata, actual byte count/hash, close
errors and cancellation are checked before returning bytes. Overwriting or deleting
the working file cannot change historical publication. A bad reference fails closed,
without mutable-file or transcript fallback. Artifacts without `ContentRef` retain
the legacy authorized retained-reader path and existing digest validation. Controlled
HTTP fixtures exercise the aliases and mutation/deletion boundary; they do not prove
physical backend deployment.

`TestBotOfflineArtifactHTTP` exercises real HTTP content/download requests through
the production backend factory with an empty destroyed-run registry and a fake
retained reader. It checks binary bytes unavailable in transcripts, team-member
access, owner/scope/home/path/hash denial, and ordinary Chat transcript fallback.
This is not a live Docker acceptance test.

## Retention deletion

`BotHomeManager.Delete` is disabled unless trusted supervisor/retention composition
installs `AuthorizeDelete`. That callback must authorize the exact registered
binding, expired restoration window, and stopped workers before returning a
server-derived sandbox binding. An owner request alone does not authorize purge.
The post-operation `BotCleanupVerifier` receipt callback is not a pre-deletion
authorizer and must not be reused to initiate storage deletion.

`DeleteBotHome` verifies the same volume ownership and driver invariants, rejects
any attached container (including stopped runs), and uses non-forced volume
removal. Docker's reference check rejects concurrent attachments. Missing owned
homes are idempotent; arbitrary or differently scoped references are rejected.
No public endpoint, request boolean, or automatic session cleanup enables purge.

## Immutable content Put readback

Docker and Kubernetes implement the optional `BotHomeContentReadbackBackend` for
immutable objects up to 8 MiB. The existing confined Put helper publishes under
the unchanged commit barrier, reopens the committed private object without
following links, verifies size/digest and file identity, then streams its actual
stored bytes before exiting. These are not echoed upload bytes. Helper terminal
and cleanup errors still fail the operation; partial readback is never returned
as success. Larger objects and backends without the optional capability retain
the ordinary streaming Put/Open path.

Entstore independently hashes the input and checks the complete returned
reference, then hashes the stored readback before placing a private byte copy in
its existing operation/transaction cache (8 MiB, 256 objects). Keys include the
complete content reference, backend and Home storage identity. The cache neither
supplies authority nor skips fresh Bot/binding lookups. First Home selection's
destination verification and final logical validation reuse these bytes rather
than starting another helper per object. Ending the operation drops the bytes;
missing or corrupt content in a subsequent operation still fails closed.

Scoped tests exercise the shared Python program on temporary files, Kubernetes
adapter protocol/commit behavior with local exec, and personal/team SQL selection
with 11 readback puts and zero separate destination opens. This is not a physical
Docker or Kubernetes latency claim; combined live timing remains a separate gate.

## Stream archives and cross-process attachment admission

`ExportBotHomeArchive` and `RestoreBotHomeArchive` acquire an exclusive kernel
`flock` on the existing protected workspace volume's `.workspace-lock`. This is
not a daemon-host lock or a process-local mutex: independent clients of the same
Docker engine use the same inode. A confined, fixed-program admission helper
mounts only that exact private volume, with no Home mount, network, capabilities
or writable rootfs. Its image, command, labels, stdin lifetime and exact mount are
validated. The lock file is never unlinked or replaced.

Session creation (including persisted recovery) and stopped/paused session start
acquire the same lock before admitting a Home attachment. Other Homes have their
own locks. Export never provisions missing private storage. Missing retained
start metadata cannot bypass admission by treating a Home-mounted container as
an ordinary session.

Lock order is **session lifecycle lock, then exact-Home storage lock** for
attachment. Archive callers must authorize and quiesce writers and complete
session stop/detach work before invoking the barrier operation; archive takes no
session lock while holding storage exclusion. Stream callbacks must not call
session lifecycle APIs. The platform writer barrier remains mandatory: Docker
attachment exclusion is not authorization or a replacement for worker quiescence.

The acquired lock spans attachment checks, streaming and helper cleanup, including
cancellation cleanup. Waiting acquisition is cancellable; after acquisition,
caller cancellation does not release the lock ahead of deferred cleanup. Explicit
release closes the holder, and client process death closes its Docker stdin so
the holder exits and the kernel releases the lock. A surviving archive helper
blocks subsequent session admission until explicit owned cleanup. Unknown or
stopped attachments reject export/restore; they are never forcibly detached.
Only validated fixed-program admission holders/waiters are exempt from the
private-volume attachment check.

The isolated physical gate exercises independent backend instances and a killed
subprocess, blocked create/recover/start, unrelated-Home progress, cancellation
ordering, foreign attachment preservation, and a 24 MiB ordinary plus 24 MiB
protected archive round trip. It does not launch an overlay or exercise the
public backup CLI/platform writer barrier:

```sh
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE=ghcr.io/sap/astonish-sandbox-base:dev \
go test ./pkg/sandbox/docker \
  -run '^TestBotHome(AdmissionDockerLive|StreamArchiveDockerLive)$' \
  -count=1 -v -timeout=5m
```

## Helper lifecycle transport cost

File and workspace operations retain **one exclusive disposable helper per
request**. Terminal exit state and exact nonce/scope cleanup labels are now read
in one fresh Docker inspection, followed by the same owned removal. Missing or
running terminal state, nonzero exit, malformed ownership, and removal failures
fail the request. Mount and volume checks are not cached or omitted. Cancellation
still joins and removes only the request's helper; no other stream shares it.

`TestBotHomeHelperInspectionDockerLive` traces the real Docker CLI for three file
listings and three workspace listings: each mode drops from nine container
inspections to six (one mount and one combined terminal/ownership inspection per
operation). This removes one CLI/engine round trip, **not container create/start/
remove overhead**. In the isolated local sample, file listings were 0.971 s before
and 1.051 s after, workspace listings 1.158 s before and 1.074 s after; this is not
evidence of an end-to-end latency win. The physical stream suite separately
verifies immutable snapshots, >16 MiB reads, scoped access, cancellation and no
leaked helpers.

Persistent helper pooling is deliberately not introduced: retained mounts would
participate in archive/purge attachment exclusion, and the backend currently has
no shutdown lifecycle. Reuse needs coordinated admission/drain semantics plus
exclusive or individually cancellable exec ownership, not merely an idle cache.
No live Bot or paid model replay is covered by these transport tests.

## Live acceptance

`TestBotPermanentDeleteAttachedDockerAcceptance` in `pkg/daemon` creates an
exact-owned running session with Home and overlay bytes plus a retained private
workspace. Raw deletion fails closed. A foreign container with a forged Home
label blocks authorized cleanup without stopping either attachment. After fixture
removal of that foreign container, a fresh backend, disk registry and worker
complete reconciliation and verify absence of the owned container, overlay,
recovery directory, workspace, Home and registry record, then retry idempotently.
The retention store is a fixture: this test proves physical Docker lifecycle,
not SQL row purge. No shared network, layer or production registry is modified.

Run this physical gate separately from optional persistence suites:

```sh
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE=ghcr.io/sap/astonish-sandbox-base:dev \
go test ./pkg/daemon -run '^TestBotPermanentDeleteAttachedDockerAcceptance$' -count=1 -timeout=5m
```

The image must already exist locally. `ASTONISH_BOT_HOME_TEST_LAYERS` may select
an existing read-only layers directory; otherwise this gate uses the local
Astonish config directory's `sandbox/layers`. Writable directories, volumes and
an internal network are disposable. Missing prerequisites fail when enabled.


`TestBotHomeDockerPersistenceLive` (also used by the designated persistence
acceptance) creates unique Bot homes and run containers using the production
backend factory and registry. It destroys both original runs, reads the old
artifact without recreating them, tests traversal/symlink/size rejection, purges
only the owned home, verifies idempotence and the other Bot's bytes, and asserts
no reader helpers remain. It requires an explicit local image and read-only
layers directory via `ASTONISH_SANDBOX_IMAGE` and
`ASTONISH_BOT_HOME_TEST_LAYERS`, with `ASTONISH_TEST_DOCKER=1`.
