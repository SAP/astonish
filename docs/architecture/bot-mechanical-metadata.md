# Mechanical report metadata

`BotWorkCriterion.verification_kind` is an optional closed enum: `model`,
`source_url`, or `retrieval_time`. Omission means legacy model assessment.
Preparation emits explicit defaults and assigns mechanical kinds only to actual
current-turn field requirements, splitting source and time into separate criteria.
Criteria, including kind and fulfillment scope, are immutable after preparation.
Saving future task standards remains model-assessed definition work, not a demand
to fetch a source now. No requirement is inferred by matching its prose.

After validating model identity, coverage, evidence references and model-only
methods, runtime independently checks typed current-turn fields. Model outputs
cannot mint mechanical attestations. A mechanical failure/unverified finding
replaces approval and enters the existing bounded correction flow. A mechanical
pass cannot erase a negative semantic judgment.

Only published candidate text and captured artifact bodies satisfy report fields;
private drafts, tool responses and assessment state cannot. Source citations must
match a recorded HTTP(S) observation URL, including its full query. The citation
may use a relevant subset of observations, not every incidental fetch.

Retrieval fields use `Retrieval time`, `Retrieval timestamp`, `Retrieved at`,
`Fetched at`, or `Fetch time` labels with a full dated timestamp. Supported forms
include `2026-11-10T15:55Z`, RFC3339 seconds/fractions/offsets, and
`2026-11-10 15:55 UTC`. Markdown field/value rows are supported. Times compare in
UTC at minute precision to `BotEvidence.ObservedAt`, never a measurement timestamp.
Associate the field with the source on the same line or preceding source field;
a document citing exactly one distinct URL can place that URL afterward.
Every timestamp in an explicit retrieval field must match that source's recorded
fetch minute; a correct timestamp elsewhere cannot excuse an incorrect field.
Fields require a colon or Markdown label/value cell boundary. A value ends at
the next cell boundary, so a neighboring measurement column is not interpreted
as another retrieval timestamp. `(UTC)` in a field label supplies the timezone
when the dated value does not repeat it. Retrieval keywords in explanatory prose
are not fields; narrative correctness remains subject to independent semantic
review, not a mechanical pass. Labels must start a line/list item or Markdown
cell: `Measurement vs. fetch time:` is a measurement heading, not a retrieval
field. Multiple observations of the same complete URL may accumulate during
repairs; any matching same-run fetch minute is valid, not only the latest fetch.
Corrections can reuse captured evidence without another network request. No
candidate content is removed or rewritten.
Unsupported explicit retrieval fields fail with formatting guidance. Missing or
ambiguous source evidence is unverified rather than inferred from model prose.

The existing observation ledger and its trust boundary are unchanged. No source
URL requirement is automatically imposed on legacy work or unrelated contexts.
These checks establish metadata correspondence, not factual correctness or
independent verification of the source's claims.
