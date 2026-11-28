# Grounding mechanical metadata requirements

Source identity is not requirement grounding. A preparation response can cite the
correct assignment ID yet invent a mandatory source URL or retrieval-time field.
The runtime must not select a mechanical output parser solely because the model
emitted `verification_kind=source_url` or `retrieval_time`.

## Selection and compatibility

`botWorkMechanicalRequirementGrounded` checks for field vocabulary in the original
text of the criterion's exact pinned sources: assignment, charter, goal title,
description/completion criteria, and inspectable attachment text. Extracted
criterion prose, filenames, goal evidence, tool results, and uncited sources do
not qualify. A request for fresh, reliable information alone qualifies for neither
metadata parser. A URL request does not itself request a retrieval timestamp;
a source measurement timestamp is not a retrieval timestamp.

During preparation, unsupported mechanical kinds become `model`. The criterion
ID, source IDs, text, scope, and exclusion state remain intact. Unknown kinds and
invalid source IDs still fail normal validation. Existing persisted criteria
remain immutable: review applies the same eligibility gate without modifying
work, its original contract, or its digests. Legacy omitted kinds remain model
assessment. Future-task definitions retain their existing scope behavior.

This is a conservative parser-eligibility guard, **not a natural-language proof
that a requirement exists**. Vocabulary recognition cannot settle negation,
quotations, applicability, all languages, or every paraphrase. Preparation and
independent review must still interpret original sources and explain extraction
mismatches, rather than demand repairs solely for invented fields. Wording outside
the recognized vocabulary remains subject to model assessment; it is not dropped
or automatically satisfied. There is no new schema field or mandatory quotation
migration for persisted criteria.

## Evidence remains strict

The gate never changes a failed/unverified model verdict to approval. Explicit
eligible fields still use the unchanged mechanical checks: full observed URLs,
observed fetch minute, source/time association, published content rather than
private tool bodies, and no fabrication of observations. A successful metadata
check still cannot override a semantic failure. Freshness and truthfulness remain
independently assessed even when no printed metadata is required.

## Regression coverage

`bot_runner_work_grounding_test.go` exercises broad latest-news preparation,
resumed criteria, a complete work loop with zero metadata repairs, exact source
boundaries, independent field selection, negative semantic verdict preservation,
and explicit requirements in assignment, charter, goals, and attachments. Existing
mechanical fixtures explicitly request their fields in the original contract.
The saved scheduled-report fixture continues to exercise observed evidence and
repair without refetching.

Run `go test ./... -run '^TestBotWork' -count=1`.

These deterministic tests do not prove live-model extraction/review behavior or
recovery of the owner's currently running conversation. No daemon restart or live
session write is part of this change. Scheduling, publication, and UI are unchanged.
