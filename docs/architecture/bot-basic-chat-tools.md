# Bot basic Chat tools

Production Bot Chat supplies `memory_save`, `memory_search`, `memory_get`, and
`memory_delete` through `launcher.BotMemoryTools`, before composing the isolated
catalogue. Argument/result types are the existing Chat contracts. Search paths
are memory IDs, not daemon filesystem paths.

## Scope and authorization

Memory reads use the Bot's session-prefiltered service in the selected personal
or team store. The factory shadows generic owner-wide memory/search context
slots. Runtime mutations use `NewBotRunMemoryService` and a transactional live
lease check. Writes, overwrites and deletion are confined to the producing run's
session; autonomous tools cannot rewrite prior-run or home-session evidence.
Attributed content retains its existing reviewed-edit protections. Credentials
are placeholderized before saving.

The production classifier treats save as a reversible write and deletion as a
delete, not as reads. Autonomy/allow-list/exact approval gates therefore still
apply. Reads remain read effects. Unsupported backends fail explicitly.

## Plans

The factory installs the existing `announce_plan`, `update_plan`, and
`announce_completion` implementations with invocation-local callbacks onto the
Bot's ChatAgent. Announced plans are approved for bookkeeping inside an already
published Bot run; that does **not** approve their verification commands.
Replacement of an active approved plan is rejected. Completion is rejected until
all phases have passing verification evidence, and it does not rerun commands.

Verification calls the same bound `shell_command` adapter as ordinary Bot tool
execution, retaining budget, policy, lease and sandbox checks. It never invokes
Chat's daemon-host default verifier. `PLAN.md` is mirrored via bound `write_file`
when available, and markdown is placed in ADK session state (`bot:execution_plan`)
for event persistence and restoration on subsequent attempts. Existing plan
progress events are reused; no UI/TUI protocol or Code-mode interface changes.

## Delegated output

`delegate_tasks` uses the factory's native per-invocation SubAgentManager. Its
large-result store and `read_task_result` share a captured request-local store.
No global cleanup goroutine or daemon-global output store is used by Bot runs.
Results are invocation-local, not durable across worker restart; small outputs
continue to be returned inline under the existing schema. Ordinary non-Bot Chat
retains its compatibility fallback.

All these tools remain in Chat's existing lexical catalogue/progressive dispatch
path; their policy checks are not bypassed by direct or deferred invocation.

## Evidence

- `pkg/launcher/bot_basic_tools_test.go`: factory-built personal/team SQLite memory
  save/search/get/overwrite; plan replacement and premature-completion rejection,
  failed/passing guarded verification and session-state restoration; actual
  controlled delegated child output retrieval and cross-invocation isolation.
- `pkg/store/entstore/bot_memory_runtime_test.go`: personal/team runtime memory
  CRUD/search, foreign/prior session rejection, stale-fence rejection.
- `pkg/tools/scoped_plan_tools_test.go`: scoped callback selection and global
  delegated-result isolation.

These are controlled package acceptance tests, not proof of a live daemon,
container, external model provider, or PostgreSQL deployment.
