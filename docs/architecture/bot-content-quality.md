# Bot content quality before publication

> **Historical protocol boundary:** the mandatory prepublication assessment and
> repair rules below apply to legacy assessment runs. Their test evidence and
> recorded failed acceptance remain historical evidence, not retroactive successes.

## Agent-led content judgment

For `agent_led_v1`, the ordinary agent owns source selection, claim checking,
correction and the final answer. Scoped checks may supply feedback, but a malformed
optional check or the absence of checks does not impose this legacy model-review
publication gate. Origin `agent` communicates an exactly captured agent result,
not a mechanical factual certification or an invented `accepted` assessment.
Partial and blocked declarations remain partial, with limitations in the agent's
answer; absent final output uses a deterministic operational fallback rather than
a narrated/reviewed rejection. Candidate/artifact identity and private work binding
remain mechanically validated. Observed retrieval is not factual support by itself,
and effect authority, permissions, approvals and actual delivery receipts remain
independent of content judgment.

The implementation and library boundary tests are documented in
[`bot-employee-narration.md`](bot-employee-narration.md) and
[`bot-employee-message-boundary.md`](bot-employee-message-boundary.md). Full live
provider/HTTP acceptance is a separate obligation; these protocol changes do not
turn the recorded diagnostics below into acceptance.

The fresh agent acceptance browser harness has a bounded external retained-source
inspection checkpoint before its final semantic evaluator. It does not change
production publication: it saves captured evidence, waits only after three
successful stock diagnostics for a private, non-symlink assessment, and blocks on
timeout without calling a model or granting a receipt. Configuration and truthful
assessment publication are documented under
[retained-source inspection](../../tests/e2eboot/README.bots-acceptance.md#retained-source-inspection-in-the-fresh-agent-invocation).

## Legacy content assessment (retained)

`pkg/api/bot_runner_work_content.go` extends the existing bounded, tool-free work
review, with bounded citation-protocol recovery and no new execution authority. Preparation
and review retain the original assignment, charter, goals and captured attachment
text. Topic relevance follows that owner scope, never a feed's broad category.
Adjacent technology is not automatically relevant; explicit scope expansion and
genuine topic coverage in another industry are permitted.

## Protocol and rejection

The server supplies `content_items` for every nonblank line of candidate text and
published artifact bodies, plus `content_sources` containing only captured body/content
passages. `owner_scope` holds requirements; `execution_evidence` holds observations
and transport receipts. Neither is a factual story source. Line segmentation ensures byte coverage; semantic
claim decomposition within each line remains the assessor's responsibility.
`content_assessments` must cover every item exactly once and report independent
relevance/support verdicts, specific explanations, and exact source quotes with
server-owned refs. Item and source enums are input-bound; the existing candidate
digest binds the complete review. Candidate/artifact prose is not corroboration.

Review guidance requests concise JSON while retaining every item, every criterion
and all required schema fields. Explanations state the scope/evidence relationship
or precise gap without restating the draft or quoted evidence. Quotes are the
shortest sufficient exact contiguous passages, preserving context, numbers and
qualifiers; use separate quotes where needed, never spliced passages or ellipses.
Genuinely nonfactual separators may use a short formatting-only explanation;
factual headings still require full claim assessment and support. This reduces
redundant prose, not coverage, and does not raise the 8192-token output ceiling,
change exact matching or retry interrupted streams.
`TestBotContentConciseReviewContract` preserves 45 item IDs, six criterion IDs and
the existing required fields across normal, pending and blocked schemas.

Missing/duplicate/foreign item coverage, unknown source refs, uncaptured quotes,
invalid content relevance/support verdicts and blank content explanations are typed
protocol failures. They permit **one** tool-free correction of the same
immutable input and strict schema, sharing the JSON format retry's two-call ceiling,
owner model, budget and before/after boundary checks. Candidate digest and criterion
authority/coverage validation runs first and never enables recovery. Invalid content
verdicts are not normalized into a pass: the reviewer must return valid assessments,
and repeated invalidity still blocks publication.
The entire original evidence must be reassessed; guessed quotes are never accepted.
Independently valid negative portions of the first review are retained, including
items after the malformed citation and validator-generated missing-evidence
findings even when the reviewer incorrectly marked those items satisfied. A
protocol correction cannot replace those findings with a headline-only pass;
substantive repair must change the candidate before it can be accepted. Repeated
invalidity fails closed with bounded
reason/index and SHA-256 item/ref diagnostics, never raw quotes or arbitrary IDs.

Citation correction additionally receives a separately labeled **untrusted user-data**
JSON diagnostic, not an instruction or public error/log. It records the original
failing assessment/source indexes, item ID, citation field (`sources` versus
`execution_sources`), original cited ref and quote, and lexicographically sorted
`exact_match_candidate_refs`. Candidates require `strings.Contains` of the entire
nonblank original quote in the corresponding original evidence map; no trimming,
fuzzy matching, normalization or cross-namespace search occurs. Empty or absent
quotes receive no suggestions. These refs establish text existence only, **not
semantic support**. A disclaimer captured from another source cannot establish a
claim about the originally cited source. The reviewer must independently reassess;
the runtime never automatically rebinds across captures. The full original input,
schema and prior response remain intact, with the same two-call ceiling and
negative retention. Coverage/verdict failures do not invent citation suggestions.
`TestBotContentCitationDiagnosticRecovery` checks data isolation, map selection,
exactness, ordering and retained negatives; the opt-in
`TestBotContentCitationDiagnosticRecordedFailure` replays the two original private
reviews without provider calls and confirms repeated wrong-source refs still fail.
The reproduced `.tmp/real-stock-title-live-3207647661` reviews both fail at
assessment 30/source 1: the exact disclaimer exists only in `source_2`, not the
selected `source_1`. Replay now supplies that candidate and still rejects a second
response that repeats the wrong ref.

The unchanged title-only live HTTP diagnostic was run once after this correction
change, with private artifacts in `.tmp/real-stock-title-live-3597493403`.
It **did not achieve accepted publication**: `partial`, quality `blocked`, zero
repairs, `malformed assessment: invalid schema`. The completed first reviewer
response added an unsupported `evidence_refs_note` criterion field, so strict
decoding stopped before citation recovery. Nine model calls, one review and four
native source reads took about 79 seconds. Home contains one failure report, not
an accepted candidate. The diagnostic harness's passing exit status is not an
acceptance assertion; title, task, tools, provider and normal daemon configuration
were not changed. No second live run or broadened schema recovery was attempted.

Source exact matching remains unchanged: no fuzzy match or fabricated evidence is
introduced. At `botContentInput` only, `bot_runner_work_content_sources.go` adds
provenance-bound citation views for the confirmed transport-escaping mismatch.
Captured `json.Marshal(response.Response)` bytes, raw refs and candidate digests
remain unchanged. Only strings within known `body`/`content` envelope fields get
citation views, with short deterministic IDs (`source_1`, `source_2`, ...).
`content_source_origins` privately binds each ID to its raw capture ref, decoding
path and SHA-256; these paths are not citation refs. Embedded JSON-looking strings
are not parsed again. Metadata titles, URLs, headers and raw envelopes are excluded.
Unknown/legacy envelopes provide no content, without mutating their stored bytes.
The schema and exact-quote validator consume the same sources map. There is no
`observation:0` to body alias, even when two calls have the same URL.

Each assessment specifies `claim_type` (factual, nonfactual, execution) and separate
`execution_sources`. Receipt quotes can support own actions/retrieval/pending status,
never external news. Mixed external factual assertions remain factual. Missing
passages downgrade factual support to unverified and reach bounded producer repair;
empty source sets use an empty-array schema rather than invalid empty enums.
Nonfactual formatting can omit quotes with an explanation. Missing legacy claim
classification gets no exemption (it defaults to factual).

A string containing a complete strict RSS/Atom document, or a body/content field
with an explicit XML media type, also gets separate strict XML text-token views.
For example, the origin path may be `#json:/content#xml:/rss[1]/item[1]/text()[1]`,
while its citation ref remains a short source ID. Namespaced elements include
the namespace in their path. Entities are decoded once by the XML parser; CDATA
remains literal. No HTML unescape, DTD/entity resolution, network access, sibling
joining or descendant flattening occurs. Malformed/truncated XML yields no XML
views, while its JSON-decoded field remains available. Source URLs/status/headers
remain metadata, not substantive factual support. Guidance tells the reviewer to
select and quote the exact decoded view rather than copying transport escapes.

For the separately captured NBSP quotation mismatch, each eligible JSON string or
XML text token containing U+00A0 additionally gets a presentation view replacing
**only U+00A0 with U+0020**. Its origin path appends `#nbsp-space` (for example,
`#json:/content#nbsp-space`), retaining the same capture ref and raw SHA-256.
The exact original view, captured bytes and candidate digest remain unchanged;
no duplicate is added when NBSP is absent. Both views use the same `add` count and
byte accounting, including the suffix. The validator still uses exact
`strings.Contains` against the selected source ID, not normalized or fuzzy quotes.
No other whitespace is collapsed or removed, and no fields/nodes are joined.
`TestBotContentNBSP*` covers the captured public excerpts, original/alternate
exactness, adversarial transformations, field/XML-token boundaries, provenance,
determinism and limits.

The confirmed Markdown ampersand mismatch additionally permits **only literal
`\&` → `&`**, with origin suffix `#markdown-ampersand`. No other backslash,
punctuation, entity or newline is decoded. Views are added in deterministic order:
original, NBSP-space if applicable, ampersand if applicable, combined if both apply.
The combined suffix is `#nbsp-space#markdown-ampersand`. Every addition uses the
same count/byte accounting; raw capture bytes and digests are unchanged.

A known content citation whose quote does not occur in its selected view may
rebind only to an **already supplied** exact `strings.Contains` match with identical
nonempty `CaptureRef`, `CaptureDigest` and `BasePath`. Each origin now explicitly
carries private `base_path`: the full JSON field or XML text-token path, without
presentation suffixes. It is recorded at generation, not obtained by stripping
strings (a literal JSON field name may itself end in a suffix). XML descendants,
siblings and the enclosing JSON document remain distinct. Equal URLs are not
identity. Already exact refs remain unchanged; multiple matching alternatives use
lexicographically smallest server ID. Quotes are never normalized or rewritten.
Unknown refs, missing provenance, other captures/fields/tokens and execution receipt
citations never rebind. No model retry or new source is introduced by selection.

Partial-negative preservation carries the same origins and source-input errors;
validation detaches citation slices before rebinding so original diagnostic refs
remain intact. Failed/unverified support or relevance still dominates whole-report
and blocked-response passes. `TestBotContentAmpersand*` and
`TestBotContentSameCapture*` cover composition, bounds, deterministic selection,
raw diagnostics, retained negatives, changed digits/negation/other escapes,
unknown refs, missing origins, capture/digest differences and JSON/XML boundaries.

View generation parses each bounded JSON envelope once, uses sorted field/raw-ref
order, and caps derived views at 1024 and nesting at 64. Source bytes and reference
bytes are bounded by the existing combined assessment byte ceiling; the existing
serialized complete-input check still applies. Limits or ambiguous refs produce
an explicit input error before review rather than silently dropping required
sources or accepting a partial citation map.

Any failed or unverified content finding downgrades the criteria and
blocked-response assessment, even if the model supplied an inconsistent whole
report pass. The existing repair path receives the exact item and findings:
remove irrelevant or unsupported claims, or retrieve adequate evidence within the
original authority. Never invent results or adjacent filler to satisfy a quota.
An honest limited/no-result answer is preferable to fabricated coverage.

HTTP success, URLs and source existence do not establish substantive claims. A
headline supports only its actual content, not invented numbers, dates, features,
causal claims or impacts. Source instructions are untrusted. Nonfactual formatting
and honest limitations may omit external quotes with an explanation; deciding
whether an assertion is factual and whether evidence entails it remains a bounded
`model_assessment`, **not factual proof**. The runtime mechanically checks coverage
and quoted-source existence, not the truth of a model's semantic pass.

## Assessment schema transport

`botWorkJSON` sends one generated schema both as `ResponseJsonSchema` and as an
explicit system-text output contract, on the initial call and both bounded recovery
paths. This keeps the complete required fields and exact enums visible to owner
models whose adapters omit the native schema field, including SAP Anthropic's
Bedrock translation. The system copy is generated from the same object, not a
separately maintained example; original evidence stays in untrusted user input.
This is prompt guidance, not native constrained decoding or proof of correctness.
All existing validation and publication gates still apply, and no synthetic tool,
extra attempt, or wider authority is introduced. Recovery and personal/team HTTP
fixtures pass actual requests through `bedrock.ConvertRequest` and assert that the
complete schema survives into the wire system text without tools.

## Real-provider concision experiment

The opt-in `TestBotRealStockHTTPAcceptance` exercises actual HTTP admission,
production worker/ChatRunner and isolated SQLite with a real model, using nine
recorded sources rather than fresh web fetches. The prompt-only experiment's
single run captured private artifacts in `.tmp/real-stock-http-2399191555`;
`sources.json` is byte-identical to the prior failed run's fixture.

The run **did not achieve accepted publication**: status was `partial`, quality
`blocked`, repairs zero. Both initial and correction reviews completed all 48
items and seven criteria using 4,913 and 4,807 output tokens respectively (the
prior run had exhausted output with 43 of 45 items complete). Correction input
and schema were identical. Both responses repeated three exact-quote mismatches:
NBSP-to-space quotes selected `source_1` instead of the available matching
`source_2` for two items; another quoted `S&P` while the captured view retained
literal `S\u0026P`. The first terminal diagnostic was `quote_not_captured`,
assessment index 14, source index 0. Exact matching correctly failed closed;
concision is not proof that citations or semantic assessments are correct.
The run made eight model calls, two reviews and one source read in about 193
seconds. No retry beyond the existing correction and no harness/Bot configuration
change was introduced.

## Real-provider same-capture binding acceptance

After failing-first regressions, the unchanged opt-in harness was executed **once**:

```sh
ASTONISH_REAL_STOCK_HTTP=1 go test ./pkg/api -run '^TestBotRealStockHTTPAcceptance$' -count=1 -v -timeout 12m
```

Private raw requests/responses, sources, admission, run, execution and Home readbacks
are retained in `.tmp/real-stock-http-3978386690`; console output is
`.tmp/same-capture-real-http.log`. The result was **PASS**, status `succeeded`,
quality `accepted`, zero producer repairs, seven model calls, two reviews, one
source read, 244,951 total tokens and 204.32 seconds. The real model was
`claude-4.8-opus`. The accepted exact summary appeared once in Home, and once after
reconciliation/reload; no report appeared before review acceptance. The harness
Git blob remained `c6f463d6923ccf03e9c8c3eb0d3de5c9356a2813`, and `sources.json`
was byte-identical to the prior experiment's fixture.

The first review was correctly rejected at assessment 12/source 0: its quote
spliced `status`, `truncated` and `url` JSON fields while omitting an intervening
`title`. No supplied view contained that contiguous quote. The existing single
correction used unchanged original input and completed all 42 items/seven criteria.
It still quoted NBSP-space passages against `source_1` at assessment 13/source 1
and assessment 39/source 1. Both were exact in already supplied `source_2` with
the identical capture ref/digest/base path; deterministic rebinding allowed the
review to proceed. This run supplied raw/NBSP views only and therefore proves live
same-field NBSP binding, not live exercise of the additional ampersand view.
Ampersand composition and rejection boundaries are covered by deterministic tests.

`go test ./pkg/api -run 'TestBotContent|TestBotWork' -count=1 -timeout 6m`
and focused race tests passed. Failing-first output is retained in
`.tmp/same-capture-failing-first.log`; final regression/race logs are
`.tmp/same-capture-regressions-final.log` and `.tmp/same-capture-race-final.log`.

This is real-provider HTTP/worker/isolated SQLite acceptance with **recorded**
sources, not fresh-market verification or deployed daemon/browser acceptance.
The separate skill receipt was `rejected`: learning assessment/staging could not
be verified and **no learning was applied**. Accepted report publication is not
proof of successful skill learning or factual truth of the recorded source claims.
No live Bot/config, retry ceiling, harness, prompt or support requirement changed.

## Bounds and unchanged behavior

The review covers at most 256 nonblank lines; oversized input fails closed under
this and the existing byte/output limits. Existing budgets, maximum repairs,
mechanical requirement grounding, exact-action execution bounds, pending-control
scope and accepted-candidate-only publication remain unchanged. Saving future
standards or scheduling does not authorize an unsolicited current report. No
public response schema, store migration, UI event or permission is added.

## Controlled acceptance

`TestBotContentQualityHTTPAcceptance` uses real owner HTTP admission, isolated
personal/team SQLite stores, worker, ChatRunner and Home SSE readback, and an
actual temporary HTTP mixed feed. The model and tool-node transport are controlled.
Assertions verify that review receives the complete original scope, exact draft,
and captured mixed feed (including an untrusted instruction), with no tools. A
broad AI draft contains an unrelated gaming headline and unsupported numeric
claims. Item failures override a forged whole pass, reach repair with exact
findings, and only the repaired supported on-topic answer is published once.
Additional cases accept an explicitly broad technology request and genuine AI
coverage in gaming, rather than imposing keyword bans. A repeated bad-draft case
exhausts exactly the existing two repairs, publishes no rejected story or artifact,
and does not replay the source fetch. Unit coverage also checks compound lines,
private-draft isolation and oversized review rejection. The `escaped-xml` case in
both scopes fetches an actual HTTP RSS entity/backslash fixture and cites its
XML-decoded node, accepts in one review without citation correction, and publishes
once. `TestBotContentDecoded*` exercises production capture through durable JSON
round trip, schema/validator ref parity, multiline quotes, pointer escaping,
single XML decode including CDATA, malformed XML fallback, field/node boundaries,
altered quote rejection, raw digest preservation, deterministic refs and limits.
The strict runtime quality stage requires both `TestBotWorkRuntimeHTTPAcceptance`
and every personal/team `TestBotContentQualityHTTPAcceptance` case, failing if any
is skipped or absent. The `citation-observation` case uses the real `WebFetch`
HTML-to-markdown implementation with a local HTTP fixture and a heading plus eight
AI stories. A first-heading `observation:0` citation is rejected; one correction
uses `source_1` on identical input/schema; the retained unsupported-number finding
then drives exactly one producer repair. Three reviews, two productions, one fetch
and one final Home publication prove correction does not stop at repairs=0. The
correction deliberately forges a support pass for the unsupported numbers; the
original negative finding must survive and drive producer repair. The fixture also
asserts that the adversarial HTML title never becomes story content, while
`observation:0` remains accepted in criterion `evidence_refs` and excluded from
content citation enums. Metadata-only/empty/legacy captures are separately tested
across normal, pending and blocked schemas: zero passages permit only empty source
arrays and unsupported factual passes downgrade to repair findings, not citation
protocol errors.

Both scopes also exercise `citation-verdict` and `citation-explanation`: malformed
review fields formerly aborted at zero repairs. One correction now retains an
independently valid unsupported-number finding even when the correction forges a
pass, then one content repair publishes only the corrected result. Candidate and
schema stay identical during review correction; no task fetch is replayed.

`TestBotContentReviewRequiresCoverage` rejects the historical whole-pass shape;
`TestBotContentReviewBindingAndDowngrades` covers missing/duplicate/foreign items,
invented quotes, self-corroboration, invalid verdicts, and inconsistent passes.
Existing boundary-only provider fixtures supply neutral content assessments solely
to isolate their original contracts; they are not semantic content evidence.

Run:

```sh
go test ./pkg/api -run TestBotContent -count=1
python3 tests/e2eboot/employee_http_acceptance.py --quality-stage runtime
go test ./pkg/api ./pkg/bot ./pkg/store ./pkg/daemon ./pkg/launcher -count=1
```

These checks do not verify a real provider's semantic judgment or a deployed
Bot/daemon. No existing owner Bot is mutated.
