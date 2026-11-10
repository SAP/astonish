# Isolated Bot assessment state evidence

The production factory supplies `BotChatResources.AssessmentContext`, a runtime-owned read-only metadata callback. It uses the same captured owner context and live run/lease/version/mandate checks as producer control-context reads, without copying that producer prompt or granting the assessor tools. It does not consume tool-call quota: this is trusted prompt-context preparation, not model-dispatched execution. Exact-action continuations skip this extra read.

Before candidate capture is saved, the callback reads current version-bound cron schedules and goals from the scope-selected repositories. Effective enabled state, cadence and timezone come from the persisted scheduler job, never the desired trigger flag. Owner, Bot, trigger, scheduler ID and version bindings must match. Unbound triggers have no effective enabled flag. Goal content is untrusted data, not reviewer guidance.

The runtime stores the JSON as `candidate.bodies["observed-current-controls"]` and appends an observed-read record with owner, timestamp and a scoped source URI. Both are covered by the existing candidate digest and existing candidate/observation reference validation. No new model-generated mechanical verdict is accepted. A disabled schedule observation proves that sampled effective state only, not future state or successful application of a queued operation.

Reads are bounded to 256 triggers, 256 goals and 64 KiB total; excess, unsupported reads, binding conflicts and revoked/expired authority block capture rather than silently asserting absence. Historical injected factories without the callback retain their prior behavior. Pending controls are neither applied, replayed nor polled. No producer instructions, charter, trigger task instructions, historical findings, credentials or channel bindings are projected.

Tests cover disabled legacy state, effective/desired mismatch, foreign bindings, stale scheduler version, fenced lease, unavailable scheduler, invalid snapshot capture and candidate digest binding. Live deployments and mixed report/receipt final-text publication are not covered by this change.
