# Bounded immutable Markdown repair

The employee review loop may repair one retained Markdown report only through the
existing authorized `RepairFileDelivery` callback. Artifact-only
`ARTIFACT_EXACT_EDITS_V1` returns 1–8 `{old,new}` edits instead of echoing a report
which can exceed the shared model response ceiling. Ordinary chat repair retains
its existing full-text protocol.

All edits target the same complete, digest-bound original artifact. Each `old`
must occur exactly once, including overlapping occurrences. Missing/ambiguous
matches, overlapping edits, no-ops, invalid UTF-8, blank results, excess count,
and aggregate edit or reconstructed candidate sizes beyond existing bounds fail
closed before the callback. Application is local and simultaneous; untouched
bytes remain identical. Neither the candidate nor the original immutable snapshot
is modified by validation.

The callback creates a new artifact identity. Trusted hydration verifies its
complete bytes against the reconstructed text and receipt. The ordinary next
review examines the **full revised artifact**, not a patch or a model assurance;
only its independent accepted verdict permits delivery. Negative reviews remain
negative and retain the existing repair ceiling. Candidate bodies/review items
remain file-backed content, not SQL metadata. No model output, token, retry,
authorization, publication, or retention limits are increased.

`TestBotArtifactTextPatchSafety` covers exact application to a report larger than
the response ceiling, original immutability, ambiguity (including overlapping
matches), overlap, sequential targeting, and bounded rejection.
`TestBotArtifactTextRepairReviewedAgain` exercises the repair callback and the
subsequent positive/negative independent review. An opt-in configured-provider
capture can run a fresh uniquely identified diagnostic repair and separate normal
review against retained evidence. Such diagnostic bytes and synthetic diagnostic
receipts are **not native publication evidence**; an exhausted production repair
reservation is never reset or silently retried.
