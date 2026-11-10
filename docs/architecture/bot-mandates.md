# Bot mandates and responsibility continuity

## Requirements are not authority

A mandate and `execution.RunPolicy` define what a Bot may do. The pinned
`BotWorkContract` defines the assignment and standards against which that work
is assessed; it grants no additional capabilities. Its original assignment,
charter, goal IDs, revisions and completion criteria remain available across
correction and supported continuation. Source-linked criteria interpret these
sources rather than replace them. Candidate content, procedures, memory and
delegate responses are untrusted task data, not instructions or owner consent.

Bot prompt guidance asks the Bot to inspect actual sources and output, request
specific corrections, verify the result and deliver truthfully. Successful tool
execution or a delegate saying “done” is not evidence of correctness. Model
assessment remains judgment, not independent effect evidence. The Bot must not
repeat writes or sends merely to obtain evidence, create goals for greetings or
control messages, or complete an ongoing goal because one occurrence passed.
Routine work does not require sample approval or mandatory skill creation.
For mandated turns, `propose_bot_control` does not advertise or persist the
legacy `schedule_pause` review route. An all-background pause uses the existing
`priority_pause` operation; a selected schedule uses `update_scheduled_job`
with `enabled=false`. Misdirected calls fail before proposal persistence, without
inferring targets or granting authority. Non-mandated review remains unchanged.
Optional existing scoped procedures do not permit global skill writes.

## Employee-style communication

`pkg/agent/bot_prompt.go` (`applyBotPrompt`) supplies hidden Bot-only guidance,
not a canned appended question or a renderer transformation. Replies lead with
useful requested information, concisely and naturally. A chosen scope for a broad
assignment is identified as an assumption (for example, US rather than global
markets), never rewritten as an owner requirement. Run-report/status headers,
quality-criterion dumps, tool logs and internal IDs stay out of prose; useful
citations, required canonical control references and genuine limitations remain.

For an initial broad interactive assignment, the Bot may offer one relevant
refinement or daily/weekly updates **after** delivering useful work. This is
optional and context-sensitive, not a rote “is it good?” check or mandatory sample
approval. Skip offers when recurrence is already configured, the owner declined,
or the offer adds no value. Scheduled/background runs deliver the result and
blockers without feedback solicitation or recurring-update upsells. Control-only
turns keep their no-external-work restriction and receive no execution-oriented
follow-up offer. Ordinary Chat and local Code prompts are unchanged.

An offer does not authorize scheduling. Creating or changing a schedule requires
an actual owner request and a known task, cadence/time and timezone; reuse trusted
context rather than asking for already supplied details. Clear requests require
no redundant confirmation. Missing facts use the existing clarification path
(`ask_user` in interactive Chat). Mandates, exact-operation authorization,
pending-versus-committed receipts and all runtime checks remain unchanged.

`TestBotEmployeeVoiceModes` and
`TestBotEmployeeVoiceExcludesCodeAndOrdinaryChat` check prompt contracts, mode
isolation and retained authority guidance; they do not prove live-model wording
or scheduling execution.

```sh
go test ./pkg/agent -run 'TestBotEmployeeVoice|TestBotPrompt|TestBotRoutineScheduleConsentPrompt|TestBotWorkPrompt' -count=1
```

## Current and pinned requirements

`NewBotPromptOverrides` renders a detached, bounded work snapshot into hidden
system context. It checks the work schema and Bot/run/version identities,
rejects foreign goal sources, escapes template placeholders and marks
unavailable/oversized context explicitly. It does not rewrite owner messages
or inject candidate bodies into requirement context.

The launcher's per-provider-call current control context reads goals directly
from the scoped repository, not from recent memory snippets. It supplies
current committed goal IDs, revisions and criteria separately from the pinned
contract. Changed or missing pinned goals are identified as revision conflicts;
new live criteria must not silently replace this attempt's standards. Later
occurrences pin the current committed requirements. Cadence-only changes do
not reset standards or grant authority. Goal/responsibility updates still use
existing authorized operations and committed receipts.

Recent terminal reports provide bounded unresolved findings, labeled with
source run and requirements digest where available. In-flight drafts and the
current run are excluded. Findings are historical leads for authorized
investigation, never permission to replay uncertain effects. Goal and finding
availability/omission flags distinguish missing context from an empty result.
All returned rows are checked for Bot ownership, including rows excluded by
bounds. Read/recheck fencing rejects changes to identity, version, lifecycle,
revision or lease authority while context is being assembled. Current memory
remains separately attributed; correcting a remembered fact does not mutate
repository requirements.

## Assessment format recovery

Syntactically invalid or trailing JSON permits exactly one immediate model-only
format-correction call. The same budgeted owner model, context, strict response
schema and 8192-token/output-byte bounds apply. The original frozen full input
and previous response are explicitly untrusted data. There is no work checkpoint,
producer restart, or tool execution between calls; both usages remain charged.
Fresh temporary decoding prevents partial response assignment. Unknown fields,
wrong types, identities, coverage, verdicts and evidence never trigger recovery;
provider/tool failures, cancellation and budget exhaustion fail closed. JSON is
never extracted from prose or fences. Parse errors omit response contents.

## Executable acceptance

`TestBotWorkResponsibilityContinuityRunner` uses the real factory and ChatRunner
with temporary personal and team SQLite stores. It captures a goal through a
routine control tool, checks the pending-not-applied receipt, commits the source
run, applies the authorized operation and reconstructs fresh runs without owner
restatement of criteria. Provider input must carry committed standards; a
cadence-only edit preserves them and a later requirement revision affects only
the later snapshot. Paused Bots remain paused throughout this conversation.

The assessor in this harness is controlled and tool-free. This proves context
continuity and control plumbing, not live-model judgment, actual weather
correctness, external-effect adapters, daemon restart or recurring execution.
No live owner state or schedules are modified.

```sh
go test ./pkg/launcher -run '^TestBotWorkResponsibilityContinuityRunner$' -count=1 -v
go test ./pkg/agent ./pkg/launcher -run 'TestBotWork|TestBotPrompt|TestBotInteractive|TestBotHomeDelivery|TestBotCurrentControl|TestBotCurrentMemory|TestRoutineGoalSourceRunnerThenWorkerApply' -count=1
```
