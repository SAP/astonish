# Bot runtime report evidence

## Budget-stop evidence

`bot.BudgetLimitError` snapshots token limit, accounted usage (including holds), remaining allowance and optional request estimates at enforcement under the meter lock. `BudgetFailureMessage` recognizes native typed errors, existing mandate token quota dimensions, and legacy budget sentinels—not error text. First-call rejection is not consumption. Unknown/overflow/runtime/tool cases receive a nonnumeric fallback; no current configuration defaults are consulted.

`BotChatRuntimeAdapter` freezes that explanation in `BotReport.Summary` and adds optional `Failure` (`kind=budget`, safe technical details). Native causes remain on `RunError`, with failed status and prior observed effects retained. Diagnostics contain selected enforcement facts, never arbitrary wrapped provider messages or request bodies; `RenderReport` indents them as code. No scheduler state or retry policy changes. Deadline cancellation and pre-admission failures retain their existing lifecycle; this is not a universal pre-claim/runtime reporting redesign.

`BotChatRuntimeAdapter` installs a per-attempt `bot.ToolOutcomes` ledger in the execution context. A trusted server tool calls `bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), outcome)` **after** independently observing its effects. ChatRunner consumes that entry only when the corresponding current tool response arrives. ADK JSON-round-trips function-tool results, so attestations travel out-of-band rather than being reconstructed from result JSON. The launcher explicitly transfers only this ledger across its owner-context isolation boundary using `bot.WithToolOutcomesFromContext`; ambient viewer values remain discarded.

Model prose, tool arguments, remote JSON and serialized transcript replays cannot create ledger entries. They never populate `Changed` or `Unchanged`. Unknown outcomes remain `Unverified`, making a completed tool-bearing run partial rather than asserting success. Recognized verified outcomes can succeed; later execution failures retain already-observed evidence without changing the failure status.

Observations require an observation kind, nonempty source and summary, observation timestamp and actor. Exact repeated observations are deduplicated. Tools may attest both changed and unchanged observations, explicit unknowns, report artifacts and a server-observed next-run timestamp. A trusted implementation must not copy these fields from model output. A positive dispatch result alone is not evidence of an external effect.

Report artifacts include a nonzero artifact ID, captured bytes, SHA-256, a canonical relative path and durable Bot home ID. Runtime checks the digest, path containment and current Bot home before exposing the artifact or using its body. `scheduler.ResolveAdaptiveReportBody` reuses adaptive `astonish-report` marker extraction with these captured bodies, without any host-file read callback. A report body remains presentation content: parsing its prose never grants evidence or notification authority.

The resulting `store.BotReport` flows through the existing Supervisor commit and replay-safe Publisher. `NotifyOnChange` thresholds count the validated changed observations, not words in the final answer. No transport or delivery-authorization gates are weakened.

## Built-in file receipts

`write_file` reads back and compares actual file bytes in the same node invocation
before returning a SHA-256 receipt. It reports `content_changed` only when the
pre-write content (or absence) was observed: identical writes are `Unchanged`,
new/different content is `Changed`, and an unknown baseline remains unverified.
`load_file` hashes its existing metadata scan
and checks size/mtime consistency; it still returns no file body. Only the pinned
built-in sandbox adapter validates these narrow receipts and records server-bound
actor/session/call/time observations in the ledger. It never decodes ToolOutcome
JSON, accepts MCP receipts, or upgrades old nodes without receipts. No additional
tool dispatch, budget exemption or exact-action continuation expansion is added.
Load inspection does not attest browser download or create a durable report
artifact. Proposal capture reads back the saved proposal and attests only saved
for review, not applied.

`TestBotBuiltinFileOutcomesThroughRunner` reproduces the formerly partial result
using actual built-in dispatch, JSON node transport, catalogue/owner binding, ADK
and ChatRunner, with one node call and one tool usage per operation. Its transport
and model are controlled; it is not live container/provider acceptance.

## Acceptance coverage and limits

`TestBotRuntimeReport` runs the real ChatRunner with controlled tools and tests verified, unverified, JSON-spoofed, invalid and foreign-home results. `TestBotRuntimeHTTPAcceptance` uses real HTTP handlers, SQLite stores, scheduler admission, daemon worker, launcher and ChatRunner. It exercises distinct scheduled occurrences, duplicate admission, approval reconstruction, distinct controlled filesystem execution directories, captured artifact publication and change-threshold notification replay protection.

The home backend, isolation provider, model and notification endpoint in this journey are controlled fixtures. It is not proof of production container isolation or an external provider's delivery. Native tools that do not explicitly record independent outcome metadata remain unverified; no generic tool success is upgraded to verified evidence.
