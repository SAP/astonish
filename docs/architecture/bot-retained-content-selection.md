# Retained Bot content selection

`entstore.SelectBotHomeConfiguration` retains its original name but now selects the
complete supported Bot-owned typed reference closure into the registered retained
Home. This completes selection for the disk codecs, **not the whole file-first Bot
cutover**: generic memory persistence and archive/cleanup integration remain separate
work.

## Database runtime records

New session events, runs (including prepared-plan opaque leaves), approvals and
activity/outbox records use runtime format 3 inside the existing typed SQL wrapper.
Their exclusive inline byte payload is size/digest checked and does not open a Home
runtime-content file. Legacy format-2 references still use immutable-file readers;
workspace/configuration codecs remain format 2. Home selection inventories and
validates runtime rows but leaves format-3 rows unchanged rather than relocating
them. Actual nested artifact references still belong to the typed closure: inline
runtime storage is not permission to omit artifact validation or reclaim old files.
There is no migration during GET and no automatic fallback from corrupt payloads.

SQL status, lease and expiry remain authoritative. Bot session load uses Chat's
ordered replay before requested history filters; no fixed row window may erase an
earlier state delta or pending tool-call/answer correlation. Format 3 is not readable
by older binaries; downgrade requires a compatible backup or dual-reader binary.

## Between-call immutable windows

The API runtime installs one empty immutable-window slot before owner/agent context
rebinding. Only the pointer is copied; identity and authorization are not copied.
Joined successful progress model/tool returns begin a gap. Synchronous journal,
settlement, checkpoint and next producer preparation borrow its verified bytes with
the existing 8 MiB/256-object bounds and full physical Home/reference keys. Actual
provider creation, executable dispatch, boundary errors and invocation teardown
close the gap. Retained contexts cannot reopen it. Producer preparation without an
explicit active gap owns fresh bytes, even when a caller retained another read
operation. Failures, mutable files, SQL authority and lease decisions are not cached.

## Trusted composition

The production worker installs `Store.InstallBotContentHomeBackend` once. Scoped
codecs retain a synchronized getter so repositories created before installation
see the same capability. Home setup selects content after registration; production
preparation checks selection before execution. An already selected exact binding
uses the transaction-fenced typed SQL closure to confirm that no staged references
remain, without rehydrating historical bodies or rewriting their rows. A remaining
staged reference takes the full copy-and-verify path. Every actual content consumer
still verifies the selected bytes and rejects missing/corrupt objects; this retry
optimization is not a content-health check or a cross-request cache. Unsupported backends are rejected
rather than given a writable host fallback.

Opening selected content never calls `PrepareBotHomeSession`: run/history reads
and backup hydration are not execution admission. The storage transport may use
an owned cold attachment, but must not start the Home compute session. Execution
preflight remains responsible for preparing that session before tool use and SQL
transactions. `TestBotHomeHydrationDoesNotPrepareCompute` covers both scopes,
inside and outside SQL, while preserving rejection of a changed owner binding.

Personal and team SQL transactions reuse fully verified immutable content reads
within that transaction only, bounded to 8 MiB and 256 objects. The key includes
the complete content reference and physical Home binding. Each codec open still
performs the current transaction's Bot/owner/binding lookup; mutable Home files,
range reads, failed reads, and intake content are not cached. Streams must pass
size, SHA-256, read and close checks before reuse. A subsequent transaction opens
and verifies the physical bytes again, including missing/corrupt-content failure.
`TestBotTransactionContentReuse` covers both adapters' bounded helper counts and
fresh verification; `TestBotTransactionContentBoundsAndIntegrity` covers limits,
Home identity, cancellation and failed streams.

`WithBotContentReadOperation` also permits the same bounded verified-byte reuse
across transactions within a single synchronous factory construction or Home
selection. Its explicit end function clears the bytes and disables reuse even on
retained contexts. Selection shares destination verification with the final logical
codec validation, not with later calls. Model tool-declaration preparation uses a
separate cache keyed by the actual ADK request pointer (`botDeclarationReads`);
each declaration still checks current Bot, run, lease, mandate and isolation. A
changed request, failed processor, entry to generation/tool execution, or factory
cleanup ends that cache. No authorization result, SQL row, root lookup, mutable
Home file or provider response is cached. Tests cover fresh fencing during reuse,
request replacement, ended contexts, cancellation and corrupt subsequent reads.
Nested owner-context rebinding explicitly carries only this internal read operation;
all principal, service and authorization values still come from the captured owner.
Prompt/control-context assembly starts a separate synchronous read operation and
ends it before invoking the provider (including synthesis). Provider health checks,
retries and tool execution therefore reopen bytes rather than inheriting that cache.
`TestBotDeclarationReadsEndBeforeProvider` checks the retained-context boundary.

Lease fencing reads the validated SQL run-control envelope without opening its
checkpoint object. It still checks the current owner, run identity, version,
status, active-run pointer, fence, worker and both lease expirations on every call.
This is not a hydrated run: work and checkpoint consumers continue through the
full codec and reject unavailable or corrupt content. Both personal and team
adapters are covered by `TestBotContentLeaseControlDoesNotHydrateCheckpoint`.

A context retaining an already-ended read operation does not disable the next
transaction's own bounded cache. The transaction checks the operation's active
state, not merely the presence of its context key. Its first read verifies current
physical bytes; duplicate reads reuse those verified bytes only within that new
transaction. Subsequent transactions still reject missing or corrupt content.
`TestBotContentReadOperationLifetime` exercises this provider-settlement boundary
with two reads per transaction and fresh binding checks on every read.

Control proposal listing selects SQL deduplication keys with the exact Bot ID and
`control-proposal:` prefix, then hydrates only the latest proposal/decision through
its original verified codec. Unrelated outbox transcript objects are not opened;
proposal and decision keys resolve to one result per ID. The personal/team
`TestBotContentProgressControlPersistence` enforces one content open for its
committed decision, rather than the former three-object scan.

The selector requires an authenticated owner and an independently verified backend
binding. Inside one SQL transaction it loads the Bot, fences its coordination row,
and rechecks the stored owner and exact Home. File commit guards use that same
transaction's `GetBot`, not an outer repository. Personal staging uses the stable
personal schema namespace; retained references explicitly use the verified Home's
original team namespace. The `bot_content_home_v1` attestation pins this routing.

## Copy, verify, then select

The explicit typed allowlist covers versions, goals, runs, Task publications and
revisions, Skill revisions and metadata-only Skill roots, approvals, activity/outbox
payloads, knowledge extractions, tracked items and Bot-linked session events.
`homeRecordEnvelope` handles the Task publication's named nested candidate envelope;
it does not recursively search arbitrary JSON. Run work, reports, checkpoints and
other nested logical bodies are already inside immutable disk content, not nested
SQL body copies.

Each row is validated through its logical codec before selection. Staged bytes are
copied without changing their logical digest, size, kind or revision. Each copy is
reopened and verified before selecting any replacement SQL reference. All selected
references and the Home routing attestation commit together. Immutable version,
Task revision and Skill revision rows are replaced transactionally while preserving
identity, digests, timestamps and coordination fields. The transaction hydrates the
selected rows through the installed codec before commit, preventing selection when
runtime routing was not installed.

A failed copy, cancellation, corrupt destination, missing installer or SQL error
rolls back reference selection. Old staging objects remain readable; verified
orphan Home objects may remain. Selected Home references never fall back to staging
when their bytes are missing. New writes through the selected codec go to Home.
The selector never removes staging objects; cleanup needs separate reference-aware
authorization.

## Explicit boundaries

Template and pre-Bot intake ownership remain independent and intentionally are not
adopted into a Bot Home. Private content storage is not the ordinary working-file
tree and is not mounted writable into the agent runtime. Selection does not grant
execution authority or validate the truth of file content.

The typed `bot_record_v2` SQL envelopes retain references and coordination metadata;
hydrated API values are not the persisted DTOs. Inline development content is
explicitly rejected, with no backfill, dual read or automatic deletion. DDL marker
4 does not certify or convert content. Generic Bot memory SQL/FTS/source-record
elimination and compact file-backed MEMORY.md remain pending. Public Bot archive
export/restore rejects both legacy inline archives and reference-only SQL recovery
until the file-first archive protocol is implemented.

## Evidence boundary

`TestBotContentHomeConfigurationSQL` and
`TestBotContentHomeConfigurationRollback` exercise personal/team SQLite adapters,
transaction-local codec routing, byte identity, retries and rollback.
`TestBotContentHomeSelectionRetryDoesNotReadHistory` proves that retries do not
consume historical bodies, remaining staged references are still selected, wrong
bindings fail, and actual consumers reject corrupt and missing retained bytes.
`TestBotInteractiveAutoHomeHTTP` exercises HTTP admission and daemon preflight with
an installed in-memory retained backend. These are not physical retained-selection,
Kubernetes, dedicated PostgreSQL or restart/backup acceptance evidence. Existing
physical Docker Home/workspace foundation evidence is separate and does not close
the remaining file-first acceptance tiers.
