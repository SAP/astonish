# Bot Task workspace contracts

## Configured search quota and discovery

Configured searches serialize in a private run/session-bound episode. Successful
durable search reservations count against the unchanged admitted call ceiling.
When this exact episode has demonstrably exhausted that ceiling, later facets
are rejected before provider/MCP dispatch. The latest observation remains
`failed`, not search success, but earlier successful discovery still permits
source inspection. Each detail admission revalidates current mandate and exact
configured route; revocation, missing/changed route, cancellation and unrelated
provider/reservation failures remain fail-closed. Foreign child cancellation
cannot mutate the parent's episode.

Predispatch quota/reservation rejections carry a trusted exact-call nonexecution
receipt, never a successful source/effect receipt. Configured search is already
classified as a concrete read adapter; this receipt also prevents a failed
invocation from being mistaken for an uncertain external effect. Successful
results expose `remaining_episode_search_ceiling` and `search_call_limit`, and
the tool declaration states the shared facet ceiling. The hint is an upper
bound from actual reservations in this episode, not an invented durable quota
read: restored/other reservations can make the durable remaining allowance
smaller. Unknown durable denials do not receive the discovery exception.

## Status and scope

This document defines the shared Task contracts and DDL foundation, not completed
Task runtime acceptance. `pkg/store/bot_tasks.go` and
`pkg/sandbox/bot_home_workspace.go` are optional capabilities; existing BotStore,
Backend, ChatAgent/ChatRunner and Runtime signatures remain unchanged. No Task is
created, activated, scheduled or granted resources during schema reconciliation.

A Task is an assignment/procedure owned by exactly one Bot. A Skill is reusable
operating knowledge; a Goal is a standing outcome; a mandate/permission envelope
is authority. None substitutes for another. A Task's optional Goal and Skill
references must identify the same Bot and carry pinned revisions. Shape validation
rejects foreign references; repositories must additionally resolve them in scope.

## File authority and archival recovery

The current executable assignment lives in the Bot's retained physical Home at
`tasks/<task-id>/TASK.md`, with declared helper files in the same bundle. The scoped
`bot_tasks` registry keeps identity, title, lifecycle and committed head, not a
second editable definition. `bot_task_revisions.data` holds immutable recovery
snapshots of parsed manifest plus exact complete bytes. Consumers must load from
Home and compare against the committed revision/digest before running. A missing
or mismatching file fails closed; a database snapshot is not an alternate live
instruction source. Explicit recovery materializes files before a new execution.

`BotTaskManifest` separates assignment, owner requirements, assumptions, raw JSON
parameters, research mode, optional Goal/Skill refs, output rules and assets. Raw
parameters preserve numbers above 2^53. The immutable database JSON must use the
existing precision-protecting envelope rather than map-to-float64 conversion.
`BotTaskRevision.ContentDigest` includes identity, parent, parsed content and exact
bundle bytes, excluding its own digest and server archival timestamp. The snapshot
is validated against every declared asset; undeclared files and truncated bytes
cannot certify a bundle. These checks certify byte/shape integrity, not truth.

## Lifecycle and publication

Task lifecycle transitions are Draft → Ready upon a usable committed executable
definition; Ready → Draft on an explicit incomplete replacement; Draft or Ready →
Archived on authorized archival; Archived → Draft on explicit reopening. No other
transition is allowed. Draft may retain incomplete assignment. Ready is not factual
certification and does not authorize recurrence or resources.

Publication has independent typed states:

- Prepared → FilesReady requires the installed trusted workspace capability to
  read back all actual bytes and persist a receipt bound to exact Home, publication
  and bundle. Caller-provided `verified=true` is not a contract.
- FilesReady → Committed additionally requires registry head CAS, original live
  authority, membership and lease/fence. Only then may the registry head advance.
- Prepared or FilesReady → Held on restore/revocation; neither resumes automatically.
- Prepared or FilesReady → Aborted on explicit authorized discard.
- Committed, Held and Aborted are terminal. A recovery attempt needs a new,
  authorized publication, not replay of uncertain effects.

`ValidateBotTaskPublicationTransition` checks shape only; it cannot establish
readback, permission or CAS. The scoped Ent repositories install a trusted provenance
resolver and enforce these requirements. `BotTaskStore` provides list/get/revision,
prepare/commit and explicit lifecycle operations. `BotTaskRecoveryStore` provides
pending enumeration, holding and authorized discard. The workspace launcher installs
the verifier only when a supported backend and registered Home are available.

## Confined Home capability

The optional `BotHomeWorkspaceBackend` supplies atomic publish, trusted receipt
resolution, bounded complete read, mutable supporting-file write, deterministic
paginated list and complete snapshot. It accepts only server-derived authorized
Home bindings and canonical relative paths; no host fallback. Implementations must
reject traversal, symlinks and hardlinks (including ancestors), devices, FIFOs and
race substitutions with confined filesystem operations. Path validation alone is
not confinement proof. Mutable write must not modify committed TASK.md or pinned
executable assets behind their revision. State and output remain distinct from the
immutable executable bundle.

Limits: 64 assets, 1 MiB per asset, 16 MiB total definition/bundle, 128 MiB mutable
Task state, 512 MiB total Home workspace. Reads fail rather than silently truncate;
aggregate quota enforcement must include existing retained bytes. File names,
links, duplicate/ancestor aliases, unknown schemas/types, foreign references and
invalid digests are rejected. Secret storage and uncontrolled scripts are not
permitted merely because a path is valid.

## Authorization, retry allocation and scripts

Candidate revision numbers are allocated monotonically by the repository over
**all** publications, including Held and Aborted candidates. A fresh authorized
candidate keeps the selected committed head as its CAS parent; it can skip abandoned
numbers, including an abandoned first publication. No terminal candidate or audit
snapshot is deleted. Prepare returns the server-normalized reference/digest; callers
must pin that returned reference rather than their proposed candidate number.
An exact prepare replay preserves its original parent, registry title/status, CAS
revision, actor and run/worker/fence identity. Editing any of those fields under the
same idempotency key fails; renewing the same lease expiry does not change identity.
Personal/team SQLite regression tests cover Held/Aborted first candidates and
replacements, retained terminal snapshots, normalized replay and resolvable heads.

The launcher rejects missing or foreign leases when a runtime BotRunIdentity is
present. Mutable writes, publication and script state commit pass both a server-only
`BeforeCommit` transaction barrier and `WithBotHomeWorkspaceCommitGuard` live check.
The backend helper stages under its storage lock and requests approval immediately
before durable mutation. The Ent barrier takes the same root sequence CAS and run
fence used for cancellation/revocation; the final check uses that locked transaction,
not a second SQLite connection. It rechecks live permission, cancellation and lease
expiry without deadlocking the single-connection scoped store. An after-write
check alone is insufficient. Transaction failure after physical mutation is uncertain
and must not be advertised as replay-safe. Unsupported repository adapters fail closed.

`bot.WorkspaceScriptBackend` is an optional companion to TaskWorkspace. The launcher
`Execute` resolves and reads back the exact committed reference, accepts only declared
executable Python scripts under `scripts/`, requires the original runtime identity and
live lease, reserves a tool call from the shared run budget and rechecks that budget
inside the commit barrier. It never trusts a model-supplied tested/verified boolean.
A backend must confine executable inputs, stage/lock mutable state, enforce existing
aggregate quotas and disable network/credentials. The callback is an authorization
boundary, not proof that a particular backend implemented transactional state isolation.
Launcher fake tests prove wiring only; physical script cancellation and quota behavior
require separately collected backend evidence. No host fallback or dependency install.

## Restore safeguards and snapshot boundary

Logical restore validates Task registry identities, status/head digests and the
immutable publication envelope against its indexed columns. Revisions must refer
to an existing same-Bot Task; Tasks must refer to an existing Bot. Pending Prepared
and FilesReady rows become Held without rewriting their archival envelope or
issuing replacement authority. Scope mapping preserves Task historical bytes and
receipts rather than recursively replacing strings. Task registry rows import
before revisions, and verified permanent cleanup deletes scoped revisions before
Task registries under the existing root cleanup barrier.

The shared confined helper supports a trusted `SnapshotBotHomeWorkspace` request
with path `workspace`: it inventories the entire backend-private tree under the
same exclusive lock held across scripts and state publication. It includes state,
outputs, immutable revisions, heads and receipts, excluding only the lock inode;
its bounded snapshot fails on overflow rather than truncating. Logical archive
export now includes checked `bot-workspace-v1` entries when supplied the exact
server-derived Home resolver and workspace backend. The export barrier fences
writers across registry and physical snapshot capture; a missing capability fails
closed rather than silently exporting only revision bundles. Explicit offline
recovery verifies scope, bindings and snapshot digests, requires a newly registered
non-active target Home, and does not restore execution authority. Physical Docker
and disposable PostgreSQL acceptance exercise complete file/state export and
restore; they do not certify semantic retrieval or real-provider answer quality.

## Migration and verification boundaries

DDL marker 3 adds separate personal/team Task and TaskRevision tables through
current Ent reconciliation; BotVersion and Skill JSON schema versions remain 1.
Heads advance independently from BotVersion. Reconciliation preserves existing
sessions, legacy triggers, jobs and hashes. SQLite archive compatibility reconstructs
only exact known pre-Bot, marker-1 and marker-2 schema deltas. Real marker-2 fixtures
retain Skill tables, omit both Task tables, and reject Task-bearing manifests or
unrelated column drift. See [migrations](migrations.md).
Generate each scope using `go run generate.go` from its directory (the ignore-tagged
entrypoint is not automatically executed by `go generate` without a directive).

`python3 -B tests/e2eboot/bot_task_acceptance.py --stage compatibility` requires
exact current-invocation Go run/pass events for existing real HTTP toolless answers,
exact-once answer publication, legacy instruction scheduling and approval/denial/
expiry, using controlled models and disposable SQLite only. Missing, foreign,
replayed or skipped tests fail the gate. Compatibility is not Task certification.
`workspace` now requires exact current-invocation physical Docker, scoped Ent
personal/team publication/recovery, HTTP creation/registry reopening and launcher
authorization evidence, including personal/team Docker cancellation during physical
staging and physically published Held/Aborted candidates followed by new authorized
same-Task publications. The gate rejects baseline-only or skipped adversarial cases.
The Docker helper is a separately provisioned digest-pinned
Python/SQLite image; it never pulls dependencies at execution time. Files are staged,
fsynced, verified and renamed into immutable revision directories. A pre-head crash
can retry exact bytes; an edited retry fails. The launcher resolves canonical
executable reads through the registry-selected revision, so an uncommitted backend
head is not authority. Mutable state has a distinct confined network-disabled mount.

`execution` exercises owned owner-HTTP admissions through production factories and
workers against physical Docker Home files and disposable personal/team SQLite.
It checks greeting non-capture, automatic incomplete draft capture during configured
search without optional retention tools, owner correction with an immutable older run
pin, manual reuse, three real scheduler callback occurrences with fresh source fixture
data, schedule deletion and manual reuse of retained corrected files. Scheduler
payloads carry Task identifiers rather than copied working instructions. The scoped
admission tests also reject uncommitted, archived, invalid and foreign Task heads and
check detached pin bytes. Team configured-search policy reads use the same trusted
admission transaction, matched to the outer team client; they do not open a second
connection or reuse another team's transaction. These controlled fixtures do not
certify real provider answer quality.

`search` has its own configured-route and physical candidate-node gate. `continuity`
requires a real owned daemon subprocess replacement, identical Task/helper/SQLite
bytes, disposable PostgreSQL authority/no-replay checks, and full workspace archive
export/explicit restore. Its restart fixture uses a test-owned loopback embedding
provider through supported production configuration, avoiding unrelated fresh model
downloads; it makes no semantic-retrieval claim. `all` now orchestrates the
controlled stages and a fresh-only Task candidate launcher, but its accountable
grounding and real-candidate lifecycle/surface obligations remain incomplete.
Missing private AI Core/search configuration blocks before paid work. Kubernetes
workspace has nonempty fake protocol/filesystem safety
tests, but no live-cluster certification; OpenShell does not expose the optional
workspace capability. Real source research remains separate acceptance work.
Docker execution evidence is not whole Task-runtime certification.
