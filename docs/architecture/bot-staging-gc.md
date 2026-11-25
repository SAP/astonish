# Scoped Bot staging maintenance

`entstore.CollectBotStagingOrphans(ctx, bots, botID, grace)` is an explicit owner-only maintenance capability for the concrete personal/team repositories. It is not called by GET, listing, startup, or a daemon timer. The grace period must be at least 24 hours. Active runs, leases, deleted Bots, foreign callers and unsupported stores fail closed.

## What is collected

Only immutable `document` objects (configuration/version and goal bodies) in the exact Bot's control-plane staging namespace are eligible. A candidate must be older than the grace period and absent from the complete typed live reference inventory. Document writers generate new references while holding the existing Bot SQL write fence; public APIs do not select arbitrary staged document references. Historical version rows remain roots.

The inventory reuses backup's explicit Home-record, memory-head and session-initial-state roots and typed run/report/artifact edges. All referenced bytes are opened and their size/digest checked before any deletion. Missing, corrupt, foreign or unsupported references abort collection. Opaque model JSON is never recursively interpreted as storage authority.

Unreferenced checkpoint, evidence, memory, artifact, task, skill and other non-document objects remain retained and are counted as `OldUnclassified`. This is intentionally not a claim of universal orphan reclamation. `.stage-*` temporary objects are always retained, even beyond grace. Young immutable objects and all reachable objects remain retained. Independent intake/template and other tenant/Bot namespaces are not enumerated.

## Database runtime closure

Format-3 runtime rows remain part of logical validation although Home selection
does not copy them to files. Inline size/digest, scoped identity and production
logical decoders must validate before collection. Only named run report/candidate
artifacts and report/publication activity projections add external roots.
Format-2 roots remain supported in mixed scopes. Legacy evidence/checkpoints
remain unclassified and retained; no recursive JSON discovery or new reclamation
category is introduced.

## Coordination and failure behavior

Every `botDiskContent.Put` holds a cross-process advisory `flock` throughout streaming, validation and its publication barrier. Collection holds both the Bot root SQL write fence and the same per-namespace lock through inventory and unlink. Lock acquisition is nonblocking and returns `ErrBotBusy`: callers may retry later. It must never wait while holding SQL, because a public publication barrier may acquire SQL in the reverse order.

Lock files are `.publish-<namespace hash>` siblings of staging namespaces. They are private control-plane coordination inodes, not Bot content. They must never be unlinked or rotated while the control plane is active, including when purging a namespace, or processes could lock different inodes for the same namespace.

The flat directory is descriptor-confined, bounded to 100,000 entries, and fully validated before unlink. Unexpected names, directories, links or multiply-linked files abort collection. Removed counts record actual unlink operations. Filesystem failures or SQL commit failures can leave a partial removal of already-proven orphans; referenced bytes and SQL authority remain unchanged and retry is safe. This is not a transactional filesystem rollback guarantee.

## Evidence

The focused `pkg/store/entstore` tests cover personal/team collection, committed references, rollback-created orphans, grace, stale in-flight publishers, foreign authorization, namespace isolation, nested artifact closure, missing nested references and real child-process lock exclusion. They use disposable SQLite/filesystem fixtures and do not constitute dedicated PostgreSQL, Docker or Kubernetes live acceptance.
