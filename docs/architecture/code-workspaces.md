# Managed Local Code Workspaces

## Purpose and boundary

An independent workspace is a **local detached linked Git worktree** with its own files, HEAD and index. Two Code terminals can edit and commit the same paths without sharing a staging area. Open the original project as usual; Astonish records which private checkout belongs to each saved conversation. No remote repository, clone, fetch or push is required.

This is checkout isolation, **not a sandbox**. Code tools still run on the host with the user's OS permissions. Git objects, named branches, tags and repository configuration remain shared through Git's common directory. Global configuration and credentials, ports, Docker resources, databases, external services and unrestricted shell operations are not isolated. Do not share writable build directories as an isolation shortcut; install dependencies or provide secrets explicitly in each checkout when needed.

## Launch and terminal workflow

```sh
astonish code                                      # current checkout, unchanged default
astonish code --workspace independent               # detached checkout from committed HEAD
astonish code --workspace independent --workspace-copy current-work
astonish code --workspace independent --workspace-base feature/base
astonish code --workspace ask                       # choose interactively
astonish code --resume <session-id>                 # explicit saved-session resume
astonish code --dir <retained-checkout>              # fresh conversation in retained files
```

`--dir`/`-C` selects the invocation directory. `--workspace-copy` accepts `committed` or `current-work`; base/copy options require `independent` or `ask`. Current-work capture requires HEAD, not an alternate base ref. Bare TTY `astonish` and ordinary `astonish code` continue to use Current workspace and start a fresh conversation, never implicitly resuming history.

In initialized local Code, `/new` offers **Current workspace** (a fresh conversation in the existing files) and **Independent workspace**. Independent creation then asks for **Committed HEAD (clean base)** or **Include current staged, unstaged and untracked work**. The “Always current/independent” entries save the new-session picker preference; they do not change the ordinary CLI launch default. `/workspaces` lists the original checkout and managed records with branch/detached, active and retained state; select a record for **Resume**, **Review changes**, **Create branch**, or **Remove checkout**. `/sessions` discovers project conversations, including those in independent workspaces. Moving an existing session into an independent workspace is the same explicit preparation flow: create the checkout, bind that session to the new workspace, then hand off to a fresh Code process in that checkout while preserving its transcript and plan history.

The bordered picker uses the active Code palette (including light palettes and `NO_COLOR`). Arrows or j/k select; Enter proceeds. Esc backs out of choices/details/confirmation or closes errors; `r` retries operation errors and refreshes lists, and `n` opens creation from the list. An empty independent inventory has an explicit create affordance (a Git project normally also has the original Current row). Preparation/apply/removal show an in-progress state and ignore duplicate/cancel input after dispatch. Busy turns, approvals, compaction and verification must finish or be cancelled before switching or mutating workspace state. Stale asynchronous replies are checked against picker generation, backend slot, transcript owner and session. Platform Chat and Bot surfaces do not acquire these Code-only capabilities.

## Creation and recorded baseline

`pkg/gitutil.CodeWorkspaceManager.Create` runs `git worktree add --detach`, leaving the source branch, HEAD, files and index alone. It records:

- the selected base commit;
- the logical staged tree;
- the complete initial working tree used as the integration baseline;
- the original project, source checkout, private checkout root and invocation subdirectory;
- Git administrative directory, lifecycle state and session bindings.

Committed mode uses the base commit tree for both staged/full trees. Current-work mode builds snapshots using **private temporary indexes**, preserving the staged-versus-working distinction. It verifies source HEAD, index bytes and captured work before/after materialization and rejects detected changes rather than silently stashing or resetting. Copied untracked files remain untracked in the new index. Ignored files are not copied; additional untracked exclusions include `.env`, `.env.*`, `*.pem`, `*.key`, `node_modules`, `vendor`, `.venv`, `venv`, `__pycache__`, `.cache`, `dist`, `build` and `target`. Tracked files are not silently excluded, so this is not a guarantee that a repository contains no secrets.

Initial objects are pinned under `refs/astonish/workspaces/<workspace-id>/{committed,staged,full}` in the shared repository. These refs are local, not new remote repositories. Later review compares the workspace's current files against its **recorded full initial tree**, including committed changes; copied pre-existing dirty work is not incorrectly presented as new workspace work. Launching from a subdirectory preserves the corresponding subdirectory if available and safe, otherwise falls back to the worktree root.

## Storage, identity and locks

The launcher uses the canonical configuration directory returned by `config.GetConfigDir()` (XDG config override or the platform configuration/application-data fallback):

```text
<configDir>/code-workspaces/<project-id>/
  registry.json                   # versioned project + workspace records
  registry.lock                   # permanent registry mutation lock
  delete-session-<session-id>.json # validated full-tree deletion journal while pending
  delete-session-<session-id>.json.lock # permanent per-root deletion lock
  owners.json                     # registered directory-owner aliases
  owners.json.lock
  <workspace-id>.writer.lock      # exclusive managed Code writer lease
  integration.lock                # serialized project integration operations
  apply-journal.json              # pending recovery, when present
  applied-<workspace-id>.json      # successful apply receipt
  worktrees/<workspace-id>/       # private checkout
```

Project ID is the hex encoding of the first 16 SHA-256 bytes of the **canonical Git common-directory path**. `ResolveProject` preserves the registry's initially recorded original checkout even when invoked from a managed worktree. Physical workspace identity is a UUID and is separate from transcript storage identity. Moving a repository/configuration tree is not an automatic identity migration.

Registry reads/mutations take the permanent sidecar lock; mutations reload the latest record, write a 0600 temporary file, sync, rename and sync the directory. Owner aliases use their own sidecar lock and synced temporary-file rename. Private managed directories are created with 0700 permissions. Registry/lock safety checks reject unsafe files and managed-root symlink ancestry; validation checks canonical checkout paths, common-directory identity, recorded Git administrative directory and its worktree backlink. These checks are not a security boundary against a malicious same-user process racing filesystem changes.

A managed Code writer holds `<workspace-id>.writer.lock` for its process lifetime, with validation both before and after acquisition. Another cooperating Code writer receives a busy/unavailable error; OS locks release on process death. Lock files remain on disk—file existence does not mean the lock is held. Registry locks protect metadata, not tool execution; the writer lease protects a managed checkout, not arbitrary external editors/Git commands or multiple Current sessions in the original checkout. Integration uses an additional project lock and, for a managed target, its writer lease.

## Sessions and fresh-process handoff

Existing Code transcript identities and paths remain unchanged: `codeUserIDForDir` produces `local_user_` plus the first eight SHA-256 bytes of the trimmed absolute invocation path (16 hex characters), with `local_user` as the empty-path fallback. It does **not** replace that hash with project ID or retroactively canonicalize old identities. Code remains in `<sessionsDir>/code/`, under app `astonish_code` and the recorded user ID, with existing JSONL and PLAN.md sidecars.

`owners.json` adds exact invocation, original-root and canonical-path aliases for project grouping; workspace session bindings retain the session's app/user identity. Discovery enumerates registered project owners, not every local transcript. Legacy subdirectory/path aliases become discoverable when registered by a launch from that path; no global migration or scan is implied. Explicit resume validates metadata and binding ownership before opening history. A missing/removed/foreign checkout fails visibly; it is not automatically recreated or resumed in the wrong directory. Checkout removal retains history, but retained history alone does not make that checkout executable again.

A cross-workspace transition returns a typed launch request, closes the current TUI/runtime resources and writer lease, then starts **a fresh Code process in the same terminal**. Unix uses executable replacement; the Windows implementation starts a child attached to the same standard streams, but Windows runtime behavior is unverified. Directory resolution happens before factory/model/tools and project guidance initialization. This is not a live `os.Chdir` of an agent with tools, delegates or verification running.

The handoff argument allowlist carries destination, optional session ID, configured model pin, explicit theme, debug and explicitly configured auto-approve (`--yolo`) behavior. Saved history/plan and ordinary saved theme preferences are loaded through their existing stores. Process-local tool/folder grants and a session-local full-access policy are not copied. Existing owner-validated durable broad-folder consent is rehydrated only when resuming its owning session; new sessions do not inherit it. Explicit launch auto-approve remains explicit user configuration, not a restored session permission. Live browser sessions, background jobs and other transient resources are not migrated; factory-owned MCP cleanup must not close another runtime's manager. A failed launch retains prepared files and reports recovery via `astonish code --dir <path>`.

## Review, apply and recovery

`Review` compares raw file bytes and executable modes against the recorded baseline and original target. It includes additions, modifications and deletions, including binary files and already committed workspace changes. Renames are represented by changed whole-file paths, not a merge/hunk editor. Conflicting same-path target edits and unsupported paths/types are reported conservatively; unrelated dirty/staged target paths can remain untouched. Symlink destinations/parents, special files, unsafe path traversal, repository-operation conflicts and case/path collisions are rejected rather than followed or overwritten.

The TUI displays the changed-path/conflict summary and requires explicit apply confirmation; it does not offer line-level conflict resolution. The manager API accepts selected reviewed **whole files** (`WorkspaceApplyOptions.Paths`); empty selection means all changes. `ApplyCodeWorkspace` targets the recorded original checkout. Apply does not commit, merge branches or stage files: the target HEAD, branch and index must remain unchanged, while selected working files receive the desired bytes. Inspect and stage/commit that result explicitly afterward.

A review digest binds observed workspace and target state. Apply re-reviews and checks the digest and preconditions before installing changes. Known conflicts/unsupported selected files fail before target writes. Multi-file installation is **not one filesystem-atomic transaction**: synced prepared files and `apply-journal.json` record preimages/postimages, target HEAD/branch/index, created directories and the previous receipt. Failures attempt conservative rollback; ambiguous external changes leave the journal intact. Successful apply writes a receipt and clears the journal. A successful-apply receipt recognizes an already-applied untracked addition only while its target still matches that receipt and the current desired bytes; it is not a general incremental merge baseline.

A pending journal blocks new apply, branch creation and checkout removal. Merely opening a picker does **not** roll it back. Recovery is explicit through `CodeWorkspaceManager.RecoverApply` and the local backend's `RecoverCodeWorkspaceApply` adapter; there is currently no dedicated recovery slash command. Recovery revalidates ownership, target HEAD/branch/index and every affected preimage/postimage before restoring files and the prior receipt. An external edit or changed target state leaves recovery blocked for manual reconciliation; do not delete the journal to bypass it. Optimistic checks and advisory locks do not promise protection from noncooperating external writers.

## Lifetime and explicit cleanup

Exit releases the writer lease and retains workspace files, conversation and plan history. Registry states include `preparing`, `ready`, `retained`, `cleanup-blocked` and `removed`: creation records preparing before Git mutation, marks ready on success and cleanup-blocked on preparation failure; normal launcher shutdown marks a managed workspace retained. Removed is a retained metadata/history record, not an auto-recreation instruction. A crash can leave the last recorded state behind; activity is determined by the lease, not the label alone.

**Create branch** attaches an inactive detached checkout at its current HEAD to a **new local branch**. Existing branches are never reset/switched/deleted. It preserves detached commits without pushing them. To operate on a workspace currently active in Code, leave it and manage it from another checkout after its lease has released.

**Remove checkout** requires explicit confirmation and a fresh eligible removal review. The manager refuses active/foreign workspaces, pending recovery, staged/tracked/untracked changes, ignored data, embedded repositories, symlinks/special files and commits (including abandoned HEAD-reflog commits) not preserved by a named local branch or tag. This is commit-preservation checking, not a claim that every change has been merged into the original branch. The digest includes the filesystem inventory, so newly created ignored data invalidates confirmation. Removal uses `git worktree remove` **without `--force`**, retains the registry/session bindings/transcripts and named branch, and performs no automatic branch deletion, ref garbage collection or age-based cleanup.

**Delete session** in Code is a separate destructive operation: its confirmation warns that an exclusively owned managed workspace, dirty/staged/untracked/ignored files and detached commits will be discarded. The original checkout and named branches/tags are preserved. Deletion collects the entire child-session tree, resolves each transcript's recorded app/user identity, and removes its checkpoints, plan sidecars and transcripts as well as exact workspace bindings. A checkout shared with a session outside that tree is retained until its last owning session is deleted. A session without a managed workspace never causes checkout deletion.

Workspace cleanup validates ownership and holds the integration, writer and registry locks; it uses a single `git worktree remove --force`, never a recursive arbitrary-path deletion or double force. Another active writer, a Git-locked checkout or pending apply recovery blocks deletion visibly and keeps the conversation available for retry. Workspace cleanup first persists a `deleting` intent before invoking Git; an interrupted attempt can be retried after verifying the checkout, Git administration directory and worktree registration. Ownership is re-evaluated under the integration lock when removing bindings, so concurrent deletion of the last sessions cannot leave an unowned checkout. Physical removal leaves a temporary removed record with its exact app/user/session bindings until both transcript removal and the fallible session-index deletion succeed. A transcript or index cleanup failure retains project discovery after reopening from the original checkout, allowing deletion to be retried without recreating or removing the checkout twice. Before destructive workspace or transcript cleanup, the launcher writes a synced, atomically renamed project-scoped `delete-session-<session-id>.json` journal under a permanent per-root lock. It records the validated full child tree, project, storage location and directory-owner identity; retries use this complete tree rather than reconstructing it from partially removed indices. Reads reject mismatched projects, storage, app/user identities, unsafe components and inconsistent remaining children. Pending roots remain discoverable in `/sessions` even after all session indices disappear, and the journal is cleared only after `ForgetSessions` succeeds. This preserves retry across a fresh FileStore/manager launch from the original checkout, including a failure after final index deletion or during partial child-index cleanup. Like the other private registries, this journal is not a security boundary against a malicious same-user process rewriting trusted metadata. Only then does successful metadata cleanup remove the unused record, apply receipt and private baseline refs. Permanent lock files and project-wide owner/registry files remain because they are shared coordination data, not dedicated session files. Cleanup is not an atomic transaction across Git and transcript storage; a partial failure can leave some dedicated files already removed, but errors are reported rather than claiming success.

When deleting the workspace currently running Code, the backend returns an internal `WorkspaceLaunchRequest.DeleteSessionID` handoff. The TUI quits and joins owned resources before releasing the writer lease; `RunCodeWorkspaceHandoff` then finishes deletion from the original checkout and opens a fresh Code process there. The deletion ID is not exposed as a CLI flag or propagated to the new process. Busy turns, approvals, compaction and verification block deletion; dispatched deletion ignores duplicate/cancel input. Exit without deletion continues to retain all workspace files and history. Ordinary **Remove checkout** retains its conservative, non-forced review behavior described above.

## Supported scope and verification

Independent creation requires Git with linked-worktree support and a committed HEAD. Non-Git directories remain usable in Current mode; bare repositories, unborn repositories, submodules (including repositories containing gitlinks), sparse checkouts and unresolved index conflicts are unsupported for independent creation. Binary regular files and executable-bit changes are supported by review/apply; symlink/submodule integration and line-level merges are not.

Focused coverage lives in `pkg/gitutil/code_workspace*_test.go`, `pkg/session/index_test.go`, `pkg/launcher/code_workspaces_test.go`, `pkg/tui/code_workspaces_test.go` and real CLI PTY workspace tests under `cmd/astonish/`. These separate storage/concurrency, lifecycle, UI and executable-handoff checks. Consult the execution results for tests actually run: source coverage alone is not proof of a live platform or Windows terminal. Windows runtime and interference from unmanaged external writers are explicitly outside established acceptance guarantees.
