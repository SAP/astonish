# Bot Task inspection and design assets

## Existing authority, not host parity

The mandated production path composes concrete constrained research adapters, then
the launcher adds Bot work/skill/Task tools and filters them against the saved
mandate. `NewBotRoutineReadMandatePolicy` already includes `bot_task_read`,
`bot_task_capture`, and `bot_task_asset` for newly admitted mandates. Historical
mandates keep their saved resources. No new resource, approval bypass, generic
filesystem tool, shell, credential access, or implicit project mount is added here.

The usable design-document path is:

1. Select an owned Task, or capture a draft when none is selected.
2. Read `TASK.md` with `bot_task_read`; its `available_paths` is the sorted manifest
   of the exact loaded, pinned bundle. Inspect supplied source/assets by their
   Task-relative paths. This is not a repository checkout browser.
3. Save Markdown or Mermaid **source text** using `bot_task_asset`, for example
   `references/design.md` or `references/architecture.mmd`.
4. Read that asset back. A successful scoped publication attests retained bytes,
   not design correctness, rendered diagrams, executable validation, or a
   downloadable report. The final answer can always carry the textual design;
   creating a Task is not a prerequisite for answering.

Tool descriptions advertise this alternative through the existing filtered
catalogue. Missing files now return `ErrBotNotFound`, rather than success with
empty content and a false successful-read receipt. Absolute and traversing paths
return `ErrBotInvalid`; an actually present zero-byte asset still reads normally.
No filesystem fallback exists: the lookup operates on `TaskWorkspace.Load`'s
verified bundle. Existing workspace implementations retain their symlink/hardlink
confinement, Bot ownership, lease fencing and publication-receipt contracts.

## Remaining project-access boundary

The daemon's host checkout is not the Bot's workspace. A saved `web_fetch` grant
or a generic `read_file` name cannot authorize arbitrary repository files.
General source inspection requires separately designed, owner-backed admission:

- an immutable project/source identity (owner, Bot, repository/snapshot digest and
  allowed subtree), provenance from the authenticated source event, and explicit
  installation of those bytes into the pinned session/Home;
- concrete read/list/search adapters with bounded output and race-safe symlink,
  hardlink and special-file confinement, checking the exact source resource plus
  tool at classification and dispatch;
- current-policy, tenant, session, cancellation, lease and quota rechecks;
- an explicit new grant/revision for existing runs, never filling missing resources
  into historical grants or routing them to ordinary host tools.

A distinct rendering/publishing adapter similarly needs exact authorized output
resources and server-observed artifact receipts before it can claim a download.
This patch deliberately does **not** claim full general Chat tool parity.

## Verification

`TestBotTaskBundleInspection` covers pinned manifest ordering, default path,
missing versus empty assets, absolute paths and traversal.
`TestBotTaskInspectionExactReceipt` drives the concrete Task reader and verifies
that missing/invalid reads have no successful receipt and receipts do not transfer
to another call ID. `TestBotTaskDesignAssetRoundTrip` drives the concrete asset
writer/readback against a controlled workspace, proving versioned Markdown/diagram
source bytes without executing a script or inventing a downloadable artifact.
`TestBotTaskAssetDiscoveryDescribesBoundedAlternative` checks the actual installed
description. These are local controlled-workspace tests, not physical sandbox,
live-provider, restart or browser acceptance.
