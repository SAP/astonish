# Docker retained Bot-home artifact reads and purge

`BotHomeBackend.EnsureBotHome` remains the optional provisioning contract.
`BotHomeArtifactBackend` and `BotHomeDeleteBackend` are separate optional
capabilities; their presence is **not authorization**.

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

## Live acceptance

`TestBotHomeDockerPersistenceLive` (also used by the designated persistence
acceptance) creates unique Bot homes and run containers using the production
backend factory and registry. It destroys both original runs, reads the old
artifact without recreating them, tests traversal/symlink/size rejection, purges
only the owned home, verifies idempotence and the other Bot's bytes, and asserts
no reader helpers remain. It requires an explicit local image and read-only
layers directory via `ASTONISH_SANDBOX_IMAGE` and
`ASTONISH_BOT_HOME_TEST_LAYERS`, with `ASTONISH_TEST_DOCKER=1`.
