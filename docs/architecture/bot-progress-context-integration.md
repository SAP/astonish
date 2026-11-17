# Durable progress context integration status

New Bots select `progress_v1` through `AppConfig.EffectiveBotRuntimePolicy` when
neither an explicit runtime policy nor historical `bot_mandates` operator ceilings
are configured. Explicit legacy policy and saved finite ceilings retain their
semantics; existing versions and grants are never upgraded on read. A compatible
explicit progress policy may opt new authorization into a finite active-time cap.

The production Chat adapter installs dispatch accounting, context restoration and
the employee progress controller. Its activation guard remains fail-closed when
required stores or work state are missing; it is not a blanket feature refusal.
Initial title clarification uses the bounded ask-user-only approval path before a
work journal exists. Answering the title resumes the saved assignment with its
progress policy intact.

`store.BotProgressContextStore` stores bounded private working bytes in the existing
scoped Bot outbox. References contain the run, work revision and SHA-256 digest;
loading requires a current fenced lease and the reference/digest selected by the
committed progress checkpoint. Arbitrary paths are never opened. Context writes
are immutable/idempotent and use the existing root lock and activity sequence.
An orphaned context record cannot be loaded as committed context. These private
records share the existing Bot outbox deletion lifecycle.

`bot.ProgressCheckpointer` writes context before the progress checkpoint and restores
controller cadence, measured fingerprint and last decision only after verifying
committed bytes. The repository still independently rejects unsettled dispatches.
Restore returns working bytes without inserting an owner message. The runtime must
still deserialize these bytes into its ordinary producer; this helper does not
execute or resume an agent.

`TestBotProgressControllerDurableContextRestart` composes the actual controller,
checkpointer and personal/team SQLite stores, performs two employee evaluations,
closes/reopens the database, restores a fresh controller, and performs a third
evaluation. It checks altered digests, stale lease fencing and context size limits.
This is a scoped repository composition test, not worker/HTTP employee acceptance.

Production-path regressions `TestBotProgressEmployeeHTTP` and
`TestBotProgressRecoveryHTTP` exercise the worker/adapter with controlled models
and isolated SQLite, including restored ordinary producer context.
`TestBotTitleConfiguredSearchHTTP` uses the default operator policy, creates a
personal/team title over HTTP, answers its real question over HTTP, and executes
the confirmed assignment through the production worker. Configured search yields
a final read-only result; missing search produces an honest blocker. Trusted read
dispatch settlement is not a content-success receipt and never proves an unknown
write completed. Real-provider quality and wall-clock soak evidence are separate
from these deterministic regressions.

## Evidence-informed evaluation and bound tools

Progress observations now include the pinned/confirmed assignment, scoped owner
answers, persona, current fenced work notes and bounded actual tool-response
excerpts. Excerpts remain private, redacted and explicitly untrusted. Partial and
thought content is excluded. Encoded observation is capped at 48 KiB, including
at most 32 KiB of excerpts and 8 KiB of reference metadata. The reference fingerprint
remains separate from model-written notes; rewriting notes does not manufacture
measured progress. Omitted material is unknown, not absent. Internal context gaps
must not become requests for the owner to repeat established scope.

Progress and synthesis timeout wrappers preserve the private trusted
`botBoundToolContext` marker without granting it to arbitrary incoming contexts.
Workflow binding, owner identity, cancellation, deadlines and budget checks remain
intact. `TestBotWorkflowBindingThroughProgressAndTimeout` exercises the real question
adapter through both wrappers; negative tests reject unbound and foreign contexts.
`bot_runner_progress_context_test.go` covers compose/observation/evaluation with
actual scoped transcript data.

Source-access flushing reuses the captured evidence reference instead of scanning
the same transcript twice. A 12-source benchmark halved reads (24 to 12) and roughly
halved time/allocation for large responses. This does not explain the original
live 28-second gap; mandated SQLite profiling did not reproduce that delay. No
speculative quota, authorization or indexing change was made.

## Fresh real-provider browser evidence

On November 16, 2026, `TestBotRealProgressBrowserAcceptance` created a disposable
personal Bot through built Studio in actual Chromium, answered its real title
question, and used real `gpt-5.6-terra` generation with `sonar-pro` search. Durable
mandates and progress policy were enabled. A short 10-second test cadence exercised
five source-bearing progress evaluations. The run used six searches, asked only
the initial scope question, succeeded in 77.23 seconds and published one report.
The complete persisted report and ordered links matched exactly once in the
original tab and after exactly one reload. Evidence and screenshots are retained
under `.tmp/real-progress-browser-41130366/`.

Earlier runs are separate evidence: an unmandated run did not cover progress;
two mandated runs completed but encountered browser harness errors. Those errors
were reproduced and corrected, not counted as passes. The final journey uses
isolated SQLite, HTTP and Home/node transport, not the normal daemon or a real
sandbox container. It proves real-model execution and browser delivery, not
exhaustive factual verification, guaranteed latency or deployment to existing
Bots. The opt-in harness requires explicit provider configuration and bounded
execution; raw private artifacts must not be published wholesale.
