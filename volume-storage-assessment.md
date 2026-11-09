# Volume Management and Per-User Storage Usage

## Executive conclusion

Astonish has enough ownership metadata to build a useful per-user storage report, but **does not currently expose a complete per-user disk-usage total or persistent-storage quota**. Measuring saved session archives is relatively straightforward; measuring all current user storage accurately requires a collector covering active sandbox writes, saved archives, personal databases, and shared resources.

This is a read-only source assessment of the current checkout. No deployed cluster, PVC utilization, or live user usage was measured. The main analysis concerns the direct Kubernetes backend; Docker and OpenShell must not be assumed to have identical physical storage layouts.

## 1. Kubernetes storage layout

| Data | Where it lives | Ownership/accounting |
|---|---|---|
| Base and template layers | Shared layers PVC; `@base/rootfs` and `<layer-hash>/rootfs` | Shared and content-addressed; do not charge the full chain to every user |
| Saved session changes | Shared uppers PVC; `<session-directory>/upper.tar.zst` | Normally attributable through the sandbox/chat owner |
| Current writable session data | Pod-local `emptyDir`, upper/work directories at `/var/astonish/overlay` | Not included in saved archive size; needs a live measurement |
| Composed filesystem | `/sandbox/rootfs` | Includes shared lower layers; measuring this as user-exclusive storage overcounts |

The Kubernetes backend does **not** provision a separate PVC per user or session. It mounts the specific session upper directory through PVC `subPath`. Session IDs form directory names; certain non-simple IDs are converted to `sid-<hash>`. Org/team/user ownership is held in registry metadata and pod labels/annotations, rather than a user-directory tree.

Sources: `pkg/sandbox/k8s/session.go:84-131,238-263,302-328`; `pkg/sandbox/overlay_capture.go:35-60`.

Helm defaults allocate **100Gi for layers** and **50Gi for uppers**, both ReadWriteMany. StorageClass must be configured. These values are requested capacities, not actual bytes consumed and not per-user allowances. PVC/storage deletion behavior also depends on the selected PV/StorageClass reclaim policy.

Sources: `deploy/helm/astonish/values.yaml:206-217`; `deploy/helm/astonish/templates/sandbox/pvcs.yaml:1-37`.

## 2. Persistence, retention, and deletion

### Stop and resume

Stopping a Kubernetes session archives its writable upper with tar/zstd into a temporary archive, then atomically renames the archive. The pod is deleted only after successful persistence. If archival fails, the session remains running. Resume recreates the pod with its recorded lower-layer chain and restores the archive into a staged upper directory.

Active writes are **not continuously replicated to the PVC**. Abrupt pod/node loss can lose changes since the last successful archive; the graceful termination hook is not a guarantee for abrupt failure.

Sources: `pkg/sandbox/k8s/session.go:541-608,648-742`; `pkg/sandbox/k8s/overlay_entrypoint.go:337-356`; termination hook at `session.go:277-299`.

### Destruction and garbage collection

Explicit destruction deletes the pod and registry row. Immediate archive removal is conditional; some cleanup is deferred to garbage collection. This means removing a session does not necessarily reclaim every saved byte immediately.

The GC reconciler defaults to an hourly cycle after a two-minute startup delay, with:

- 24-hour grace for unreferenced layers.
- One-hour grace for orphan uppers.
- Up to 500 upper reclaims per cycle.
- 14-day default expiry for eligible unpinned, stopped/terminated sessions; configurable and disableable.

Sources: `pkg/sandbox/k8s/session.go:752-859`; `pkg/sandbox/k8s/gc_reconciler.go:105-174,283-381`; `pkg/config/app_config.go:500-510`.

Templates reuse identical content-addressed layers. Team-template deletion decrements references before reclaiming a layer; another template's reference can retain the shared data. Physical deletion and metadata deletion are distinct operations.

Sources: `pkg/sandbox/overlay_capture.go:43-55`; `pkg/api/team_template_handlers.go:390-460`; `pkg/sandbox/k8s/template.go:277-324`.

### Cleanup caveats worth validating

Source inspection identified two potential naming mismatches, not live-confirmed leaks:

1. Orphan-layer directory scanning recognizes UUID-shaped names, while captures produce SHA256-shaped names (`pkg/sandbox/k8s/gc_reconciler.go:483-495`; `pkg/sandbox/overlay_capture.go:43-55`).
2. Upper GC compares directory names with raw session IDs and uses raw IDs for some deletion paths, while provisioning can hash session directory names (`pkg/sandbox/k8s/gc_reconciler.go:261-275,308-312`; `pkg/sandbox/k8s/session.go:112-131`).

These deserve focused tests before treating GC as complete reclamation of all possible directory formats.

## 3. Existing usage information

There is no discovered aggregate user storage API/UI or user-wide disk accounting service. Existing information is partial:

- Layer records have `SizeBytes`, parent links, reference counts, and optional creator metadata (`ent/platform/sandboxlayer.go:19-33`).
- Kubernetes layer capture uses `du -sb` on extracted content: apparent bytes, not allocated blocks. A capture-script variant can emit zero size (`pkg/sandbox/overlay_capture.go:29-33`; `pkg/sandbox/k8s/template.go:520-530`).
- Docker capture sums file lengths, which is not precise physical accounting and ignores walk errors (`pkg/sandbox/docker/template.go:493-501`).
- The admin UI displays base-layer size, not a user's total (`web/src/components/platformAdmin/SandboxBaseTab.tsx:483`).
- Sandbox registry rows identify the chat session/creator but do not store a current usage measurement (`pkg/store/sandbox.go:374-393`).

Chat sandbox ownership usually follows the linked chat's user ID, with creator fallback. Fleet and template-editor sandboxes are team-scoped and require a separate allocation policy.

Source: `pkg/api/sandbox_backend.go:225-286`.

## 4. What “space a user is using” must include

A useful report should separate:

1. **Active writable sandbox bytes** — measure the upper, not the composed rootfs.
2. **Saved archive bytes** — measure the compressed archive actually stored on the uppers PVC.
3. **Personal database bytes** — sessions, memory, documents, apps/state, flows, credentials, settings, and other personal records.
4. **Shared/team storage** — templates, fleet storage, base layers; show separately unless an allocation rule is agreed.
5. **Unattributed/operational storage** — orphan data, staging, images, logs, backups, database WAL and storage overhead.

An active session can have both a live upper and an older saved archive. Those are distinct stored copies; for physical accounting both matter. Compressed archive size and uncompressed upper size are not interchangeable.

Personal PostgreSQL data uses a per-user schema within each organization database. SQLite personal data uses `orgs/<org>/personal/<user>.db`, with WAL. A user belonging to multiple organizations needs aggregation across those scopes.

Sources: `pkg/store/entstore/tenant_router.go:129-132,1011-1019,1058-1068,1242-1279`.

Artifact references are not a complete byte ledger: content can be served from host files, sandbox files, or reconstructed session events. Avoid counting a reference as another physical file while overlooking duplicated persisted content.

Source: `pkg/api/session_handlers.go:1180-1213`.

## 5. Ease of implementation and recommendation

**A limited administrative report is relatively easy:** enumerate sandbox ownership and measure saved session archive sizes. Label it “saved sandbox storage,” not “total user storage.” A shared PVC's capacity or `df` output cannot answer that question per user.

**A useful user-wide report is moderate cross-cutting work:** add a periodic, backend-aware collector; record measurement time and status; aggregate active uppers, archives, and personal database usage; expose a scoped API/admin UI. Keep shared and unattributed bytes visible. Measurements should be cached rather than repeatedly walking large filesystems on interactive requests.

**Reliable billing or hard quotas need additional design:** decide allocated versus apparent bytes, treatment of shared layers, compressed snapshots, database overhead, replication/backups, and stale measurements. Then add enforcement. The existing optional pod `DiskMiB` limit maps to ephemeral storage; it is not a quota on the shared PVC or all of a user's sessions, and ordinary sandbox limit conversion leaves disk unset.

Sources: `pkg/sandbox/backend.go:314`; `pkg/sandbox/k8s/session.go:369-370`; `pkg/config/app_config.go:1050-1067`; `pkg/sandbox/flow_test.go:78-80`.

Recommended sequence: validate the GC naming cases; add saved-archive reporting; extend with active-upper and database measurements; only then introduce quotas if required.

## 6. Other execution modes

- **Docker sandboxes:** live writable data resides in a Docker volume. Stop archives it but retains the container/volume; destroy intentionally removes the container, volume, registry record, and persisted upper. Active volumes and archives therefore need separate accounting (`docs/architecture/sandbox-backends.md:337-338`).
- **Local Code TUI:** works on the host filesystem rather than providing a platform user-volume boundary. Project files cannot automatically be attributed to a platform user using sandbox accounting.
- **OpenShell:** separate gateway-managed backend. This assessment does not establish its deployed persistence layout or quota behavior; inspect that backend/deployment separately before applying direct-Kubernetes conclusions.
