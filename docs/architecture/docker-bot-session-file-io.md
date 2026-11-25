# Docker Bot session-owned file IO — implementation status

Routine `startHomeFiles` executes the compiled Linux file protocol in the Bot's
existing session container. The fixed outer executable is
`/usr/local/bin/astonish-host node --internal-home-files`; descriptors 9 and 8
select the server-derived Home and private roots. No overlay Python, model tool
dispatch, persistent node server or routine storage helper is involved in this
file path. Ordinary model tools retain their existing chroot dispatch.

`resolveBotFileTarget` checks the physical container ID, running state, session
label and exact named-volume/type/RW mounts in one Docker inspection. Execution
and process-only cancellation address that physical ID, not a reusable name.
It performs no store/lifecycle calls; broader prepare/bind paths are unchanged.
Reads do not implicitly start offline compute. Trusted callers must prepare the
existing session before entering transactions.

## Stopped-session export (2026-11-21)

Cold export now accepts an explicitly bound, exited session with its matching
persisted recipe and exact retained mounts, without removing or starting it.
The physical ID is pinned and rechecked; running/paused, unknown, mismatched and
replaced attachments remain refused. Archive admission excludes `StartSession`
until capture and helper cleanup complete. Restore retains detached-target rules.
Fully detached storage does not require a compute recipe: Docker absence is
checked first, while all actual attachments still require validation.

`/tmp/astonish-stopped-archive-before.log` reproduced the stopped-source refusal.
`/tmp/astonish-stopped-archive-final.log` records the subsequent real-Docker checks,
including a startup attempt during streamed export, unchanged stopped physical
identity, exact binary bytes, detached restore/reexport, and original-session
recovery. Binding and detached-without-recipe regressions also passed with `-race`.
The public CLI cold backup test initially caught a missing-recipe regression;
`/tmp/astonish-cold-backup-cli-after.log` records its corrected pass: create,
verify, failed restore, exact cleanup, retry, and reopened HTTP/snapshot downloads
of 17,825,794 bytes. Its source compute is detached, not a stopped attached session.
The added `TestBotFileFirstStoppedBackupCLI` subsequently passed alongside it in
`/tmp/astonish-stopped-backup-cli-isolated.log`, retaining stopped source compute
through public backup and restoring into a target-local backend. Both preserve
exact reopened HTTP/snapshot bytes and fresh destination ownership. The storage
driver now requires both CLI cases as `storage-docker-backup-cli`, with personal
and team children. Team detached/stopped runs passed in
`/tmp/astonish-team-backup-cli-member.log`. Cross-backend backup requirements
remain blocked.

These runs use existing test-owned image `astonish-storage-completion-a1474521:local`
and current host adapter/CLI source. They do not claim a rebuilt sandbox image,
live Kubernetes, or complete controller/backup acceptance.

## Compiled protocol

`RunBotHomeFiles` implements list/stat/open/write/put/snapshot with the existing
JSON metadata and length-framed binary bodies. Writes stage/fsync, emit ready,
wait for `commit`, recheck preconditions and parent/staging identity, then rename
and directory-fsync. Create-only commits use `RENAME_NOREPLACE`. Private objects
retain `content-v1` keys and aggregate workspace quotas. Linux `openat2` enforces
beneath/no-symlink/no-cross-mount resolution; unsupported kernels fail closed.
Regular files must have one link and are opened nonblocking. No error envelope
is appended after a raw body starts: terminal transport errors remain required.

This is not hostile-root isolation. An outer binary and inherited descriptors
do not protect against privileged sandbox processes. File content is never an
authorization grant; ownership, leases and approval remain host responsibilities.

## Compiled file-protocol slice

`RunBotHomeFiles` is now available for Linux internal node dispatch, with a
fail-closed non-Linux implementation. It processes one request and exits; it is
not a persistent storage server or a model-visible tool. Its `list`, `stat`,
`open`, `write`, `put`, and `snapshot` operations retain the existing metadata,
content-v1 keys, length-framed bodies and ready/commit protocol. Linux `openat2`
requires beneath, no-symlink and no-cross-mount resolution; missing kernel support
fails closed. Files must be regular with one link. Create-only commits use
`renameat2(RENAME_NOREPLACE)`; replacements retain expected-digest checks and
cooperative directory locking. Both kinds recheck the parent and staged bytes
at the commit fence and fsync file and directory. This does not protect against
a privileged hostile process that can alter the mount namespace or ignore
cooperative locks.

The protocol-only Linux fixture is separate from real-session evidence. The
Docker live fixtures below additionally exercise root CLI dispatch, full overlay
entrypoint startup, detached archive transport and compute recreation. Neither
tier proves Kubernetes execution or full Bot startup.

## Remaining acceptance boundaries

These fixtures do not complete shared-session storage. Pre-transaction resolved
handle caching, full shared generic exec construction, API/launcher offline
attachment selection, operation-wide offline attachment amortization, cancellation
and remote reaping, stale handles and real Kubernetes recovery need their own
acceptance evidence. Legacy Python constants remaining in source are not proof
that every caller has been ported; validate each transport independently.

`TestCompiledHomeFilesProtocol` runs real Linux syscalls in a disposable Docker
container and covers binary ranges, put/readback, create/CAS, snapshots, denied
commit, truncated input and symlink/hardlink/FIFO/path traversal rejection.
`TestCompiledHomeFilesNoPythonDocker` is the dedicated real-session test using a
test-owned changed binary image and an unmodified `@base` tool layer. It exercises
node dispatch, nested commit-barrier reads, immutable content and ranged
snapshots. Missing infrastructure is blocked evidence, not a passing skip.

## Verification refresh: 2026-11-19 compiled storage

The storage driver was rerun with disposable image
`astonish-storage-finish-87871eb0:local` (image ID
`sha256:1738d020648a4f80587aeea8724377da3fe91fe40a99ec3c4d2efd069ab3edb4`).
It preserves the entrypoint from test-owned
`astonish-bound-routing-33541dfc:local` and replaces the outer CLI with the
workspace-lock-fix build (CLI SHA-256
`8aec500622cff455da3f714c6de0282f1e726ea5d33f0dc14aeb3a13a77e485c`).
No daemon-used image tag was changed.

The driver's four required real-Docker tests passed without skips in 45.849s:
`TestCompiledHomeFilesNoPythonDocker`, `TestBotHomeStreamArchiveDockerLive`,
`TestBotHomeArchivePrivateWriterContentionDockerLive`, and
`TestBotHomeCompiledArchiveDockerLive`. Evidence includes the original binding
through stopped-compute reads, compute recreation with exact retained bytes,
interrupted download with joined cancellation, a stopped commit target with one
attempt and no published destination, and cold export/restore/reexport with
identical manifest and bytes. Separately,
`TestCompiledWorkspaceNodeDockerNestedCommitReads` passed all four cases using
two actual compiled processes: resolve, snapshot, read and concurrent-writer
preservation.

Fresh Linux/arm64 package test binaries were copied into a network-disabled
Debian fixture container. Twenty-two compiled protocol/admission/archive/
workspace/knowledge tests and twenty-two Kubernetes adapter/initializer tests
passed without skips. These include the driver's required Linux protocol and
Kubernetes adapter names, plus the POSIX restore initializer. The Kubernetes API
is fake in this tier; compiled node execution and Linux syscalls are real.
This is **not live Kubernetes evidence**. Legacy Python interoperability
fixtures are outside this no-Python/new-format acceptance tier.

The macOS-hosted driver still returns `BLOCKED`: it does not consume these
separately executed Linux results. Dedicated live Kubernetes, full cross-layer
tool parity, files-before-SQL/schedule restore and complete cross-backend
acceptance remain distinct requirements. No dedicated cluster configuration was
supplied or ambient cluster contacted. This refresh does not claim completion
of shared-session storage.

## Verification refresh: 2026-11-20 retained-byte and terminal-error coverage

This refresh used the explicitly approved **existing** image
`ghcr.io/sap/astonish-sandbox-base:dev`, ID
`sha256:c3e9ad834c75ccdb63126722a3cbfcc0e944e14c5909c00d7fc2023218186d33`
(Linux/arm64), without rebuilding or retagging it. The live adapter code and test
binaries came from the current working tree; the shipped root CLI came from that
existing image. This is not proof of a newly rebuilt CLI artifact.

The five-test Docker command (`TestCompiledHomeFilesCommand`,
`TestCompiledHomeFilesNoPythonDocker`, `TestBotHomeStreamArchiveDockerLive`,
`TestBotHomeArchivePrivateWriterContentionDockerLive`,
`TestBotHomeCompiledArchiveDockerLive`) passed without skips in 50.923s. After
strengthening the retained-file test, command construction and the live no-Python
test passed again without skips in 29.424s:

```sh
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE=ghcr.io/sap/astonish-sandbox-base:dev \
ASTONISH_BOT_HOME_TEST_LAYERS='/absolute/path/to/existing/sandbox/layers' \
  go test ./pkg/sandbox/docker -count=1 -v -timeout 4m \
  -run '^(TestCompiledHomeFilesCommand|TestCompiledHomeFilesNoPythonDocker)$'
```

The ordinary replacement is now **165 binary bytes**, including NUL, non-UTF-8
and CRLF, rather than one ASCII byte. Stopped-compute ordinary/snapshot downloads,
removed-compute downloads and recovered-session downloads assert exact bytes,
reported full size and SHA-256, and successful stream close. Stopped downloads
must retain the original non-running physical container. Existing checks still
prove one warm session, no warm helper, 16 MiB independent-download survival
through cancellation, and one terminal stopped-target commit attempt without a
published destination. Archive evidence remains cold export/restore/reexport with
identical manifest and bytes, not SQL restore ordering.

`TestBotHomeFilesStreamProtocol/overlong` now rejects a body exceeding its declared
length and joins transport cleanup. `TestBotHomeFilesWriteProtocol/terminal`
models a successful commit response followed by transport failure: the adapter
must return that error, return no successful entry, and dispatch/authorize only
once. The four selected host protocol tests passed normally (0.870s) and with
`-race` (2.533s). These are **fake Kubernetes API/exec** tests, not cluster evidence.

Fresh Linux/arm64 package test binaries also ran in disposable network-disabled
containers using this existing image. The Kubernetes `TestBotHomeFiles.*` and
`TestBotHomeContentReadbackAdapter` selection passed without skips, including real
compiled-node filesystem, confinement, blocked-upload cancellation and prospective
quota fixtures. An initial `/tmp` bind was unavailable to the Docker daemon; the
retry used a test-owned temporary directory beside the approved layers and cleaned
it on exit. No retained layer was changed. Root compiled file/archive/workspace
fixtures passed; Python interoperability tests are excluded from the no-Python
selection and are not acceptance evidence.

**BLOCKED — live Kubernetes:** no dedicated authenticated KUBECONFIG/context and
approved disposable namespace/PVC configuration were provided. No ambient cluster
(including `p-qa-de-1`) was contacted or provisioned. Required prerequisites are a
dedicated authorized context, disposable namespace/service account with the
required pod/exec and ConfigMap permissions, distinct provisioned layers/uppers/
RWX Homes claims, an available no-Python base tool layer, and a cluster-accessible
image containing the intended compiled CLI/entrypoint. Physical pod UID/mount
binding, one-session/zero-warm-helper behavior, cancellation/reaping, eviction
recovery and offline archive round trips must then run on that cluster. Fake API
and local Linux results do not satisfy that tier. Full storage/controller
acceptance and a fresh CLI-build provenance check remain separate requirements.

## Reproducible real-Docker evidence

Build the **root CLI**, not a package test runner, into a separate test-owned tag.
Use the existing generated entrypoint (regenerate via `make sandbox-entrypoint`
only if its source changes). For the arm64 host used for this verification:

```sh
make build-linux-arm64
docker buildx build --progress=plain --platform linux/arm64 \
  -f docker/sandbox-base/Dockerfile \
  -t astonish-storage-proper-20585a9b:local --load .
docker container run --rm --network=none \
  --entrypoint=/usr/local/bin/astonish-host \
  astonish-storage-proper-20585a9b:local --version
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE=astonish-storage-proper-20585a9b:local \
ASTONISH_BOT_HOME_TEST_LAYERS='/absolute/path/to/existing/sandbox/layers' \
  go test ./pkg/sandbox/docker -count=1 -v -timeout 12m \
  -run '^(TestCompiledHomeFilesNoPythonDocker|TestBotHomeStreamArchiveDockerLive|TestBotHomeArchivePrivateWriterContentionDockerLive)$'
```

On 2026-11-19 this command exercised all three tests, **without skips**, and
returned exit 0 in 26.146 seconds; a repeat with an exact offline listing
assertion also passed in 25.208 seconds. Image ID:
`sha256:bf096497ba6bc4769d34ad8b7d82bfaeb1abb84acc3862cd9357e9796cf753ee`.
The shipped `astonish-host` SHA-256 was
`1b24307674a5aa65fa287e01a93046040e165e8853ca4857d018ac3b83cb8146`.

- Warm operations retained one physical session, with no Python executable in
  the base tool overlay. CAS, nested authorization read, immutable binary
  put/open and ranged snapshot passed.
- After stopping and removing **only fixture compute**, offline list and
  ordinary/private download succeeded. Both retained volumes had zero remaining
  attachments. This uses an explicitly detached backend in the fixture; it does
  **not** prove API/launcher selection or high-level attachment amortization.
- `PrepareBotHomeSession` recreated a different physical container under the
  original session ID and retained Home binding; ordinary and immutable snapshot
  bytes were identical.
- Detached archive restored 25,165,824 ordinary plus 25,165,824 protected bytes,
  SHA-256 `39b02895e6ac028b07b79fb25fa8ca54a52421297e1b29049bb1013506cedf9f`.
  Truncated restore, nonempty target, missing barrier and unknown attachment
  rejection passed.
- SQL/private-writer contention refused export/restore as busy without timeout
  or output, kept the writer attached, allowed an unrelated Home, then exported
  successfully after the writer finished.

Fixture cleanup passed. A post-run image-filtered container query and queries
for the logged Home volume identities were empty. The user's daemon, templates
and development image tags were not changed. This is Docker evidence only: no
real Kubernetes cluster, paid-provider Bot run or HTTP offline route was tested.

## Bound offline route and cancellation follow-up (2026-11-19)

The Docker backend now has physical evidence for the **same bound instance**
reading a stopped session and then a removed session. `botFileAccessSession`
selects a storage attachment before dispatch, without `StartSession`, readiness
or Home creation. Existing mismatched recipes and inspection errors stay fatal;
a failed warm exec is never retried on the offline route. Detached callers remain
supported, including trusted identities which have never launched compute.

A new fail-first real-Docker test exposed cleanup by reusable attachment name:
a renamed original followed by a same-name replacement (even copying labels)
caused cleanup to delete the replacement. `existingHomeFiles.Close` now checks
labels on the pinned physical ID and removes that ID only. Ambiguous startup
before a physical ID is established retains the existing fresh label check.

Verification used a fresh root-CLI build in test-only image
`astonish-bound-routing-33541dfc:local`, derived from the proper image above,
without rebuilding or changing the retained tool layers. Image ID:
`sha256:448cfd8b3c0002760ed17ffa74d6cf82ed0ee126dac2998a9ff22fde78e2e0fb`.

```sh
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE=astonish-bound-routing-33541dfc:local \
ASTONISH_BOT_HOME_TEST_LAYERS='/absolute/path/to/existing/sandbox/layers' \
  go test ./pkg/sandbox/docker -count=1 -v -timeout 4m \
  -run '^(TestCompiledHomeFilesNoPythonDocker|TestDetachedCompiledHomeFilesDockerLive|TestCompiledHomeFilesStaleAttachmentDockerLive)$'
```

All three ran without skips and passed in 34.951 seconds. In addition to bound
stopped/removed browse and exact ordinary/private download, these prove:

- Cancelling a paused 16 MiB warm download joins the exec and removes its unique
  remote process token, while another operation completes all 16 MiB unchanged.
- Cancelling a paused offline download removes its owned physical attachment;
  another operation's separate attachment remains running and delivers exact bytes.
- A cached stale attachment refuses mutation before the commit callback, removes
  only its original physical container and preserves the same-name replacement.
- Stopping warm compute at the final commit callback produces a terminal failure,
  exactly one commit attempt, and no destination file; there is no offline retry.

**Remaining:** abrupt process death after staging can leave an uncommitted
`.stage-*` file visible in Home listing. This was observed while testing the
stopped commit target; crash-stage reconciliation is not implemented by this
transport change. HTTP/API route selection, full backup/restore integration and
physical Kubernetes remain separate acceptance requirements; no Kubernetes
result is claimed here.

