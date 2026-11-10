# Criterion temporal scope

`BotWorkCriterion.fulfillment_scope` records a model's source-grounded interpretation of fulfillment for the pinned assignment:

- `current_turn`: assess the actual requested deliverable or behavior.
- `future_task_definition`: assess whether the requested standard is fully encoded in the saved task definition. In a pending-control review, the independently captured complete pending payload is evidence of encoding, **not** application or an actual report occurrence.

Omitted fields retain the legacy current-turn interpretation. Unknown values fail validation. Scope is immutable after extraction alongside criteria; original contract, source IDs, requirement text and source digest remain intact. Strict extraction responses emit the enum, but legacy persisted criteria and fixtures need not.

Review independently checks temporal intent from the original latest assignment, charter and goals. It cannot blindly accept extracted scope or exclude charter requirements. Saving standards without changing cadence is not a new report request merely because the charter names a report topic. Honesty, permission and useful-outcome constraints still apply to the save operation. Missing standards in the payload fail. Missing genuinely requested reports still fail, including reports retained only as private producer drafts.

Each new scheduled occurrence extracts its own criteria against that occurrence's assignment and saved goal standards; those standards then apply to the actual report, not just to their stored definition. Scope grants no authority and changes neither cadence nor publication policy.

## Regression proof

The opt-in `TestBotWorkModelOnlyReproduction` temporal mode uses `employee-quality-failure-disk.json`, a read-only capture of the original failing run's complete persisted work. It retains the full original contract, candidate bodies and exact pending controls, then makes two isolated real-model requests: extraction and pending review. No daemon, Docker, report fetch or control mutation occurs. The original candidate is not rewritten to hide its unsolicited draft. Model outputs and requests are saved privately under `review-model-reproduction-temporal`.

The opt-in `ASTONISH_REVIEW_REPRO_PREPARE=1` mode instead reads `prepare-failure-disk.json` and makes exactly one tool-free extraction request against its original contract using the current production prompt/schema. It validates the extracted producing state without requiring a candidate or executing the producer. An extraction that passes now does not establish why an earlier transient model response failed.

## Safe preparation diagnostics

Criterion validation errors retain `errors.Is(err, store.ErrBotInvalid)` and include fixed field names and zero-based criterion/source positions. They never echo model-supplied IDs, invalid enum values, source text, or exclusion reasons. Preparation adds `preparation criteria rejected` context to the existing blocked report. Unknown enum values, duplicate IDs, missing text, invalid source references, unsafe exclusions and incomplete source coverage still fail; no semantic retry or silent criterion repair is introduced. Omitted kind/scope retain the existing model/current-turn defaults.

Verified on the saved failure: 10 extracted criteria, 5 future-definition criteria, all 10 current-turn criteria satisfied, with pending application still unverified. This does not demonstrate a later scheduled occurrence or full producer execution.
