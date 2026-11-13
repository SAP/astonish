# Agent candidate journey (diagnostic, not release acceptance)

`local_bot_smoke_launcher.py --verify-bot-agent --config /absolute/private/config.json`
now executes a bounded fresh native-browser journey rather than always stopping
before startup. **With a valid configuration this starts an owned candidate and
can make paid model calls.** No live calls were made while implementing this
harness. Never supply the normal daemon, production state, or copied live-session
secrets.

The existing version-1 private candidate configuration is mandatory (absolute
non-symlink file, mode 0600). Its declared provider must exactly equal
`New AI Core Provider`, type `sap_ai_core`, and model
`anthropic--claude-4.6-sonnet`. Supply the provider configuration explicitly;
ambient acceptance-mode flags are removed before selecting the new journey.
The launcher rejects `--state` and other acceptance modes. Existing candidate
staging, random-port ownership checks, private config/evidence permissions, and
production provider settings provisioning remain in use.

## Implemented observation sequence

1. Create three distinct Bots through native Studio **New Bot**, each with only
   the unchanged title `Stock Market News`; do not supply URLs or follow-up
   instructions. Record the attempt before clicking Start conversation.
2. Read each Home's production `/model-status` response and require intended
   effective provider/model plus available credentials. Poll the actual worker
   run, retain all outcomes, and require exactly one successful identified run
   with an agent-led finished projection and no remaining obligations for a
   positive diagnostic observation. Legacy accepted review is not substituted.
3. Compare Home resolution to persisted worker provider/model. Capture Bot/run,
   Home history, and finite worker tool stream in private files with SHA256
   digests. Require a correlated successful producer-selected retrieval.
4. Render the actual Home answer, reload the page, and compare visible text,
   ordered persisted messages, final report and work projection. A failed sample
   is retained and does not suppress the next fresh sample.

Files are `agent-stock-N-*` plus `browser-evidence.json` under the owned candidate
root printed as `STATE=...`. Candidate/browser hashes and a fresh nonce bind the
invocation. Sample status `diagnostic-observed` is deliberately **not** PASS.
The browser evidence and launcher remain blocked, exit 1, after collecting the
bounded journey. A run succeeding and retrieving data does not prove truth.

## Offline source-grounding assessment (implemented interface)

`agent_grounding_contract.py` is an **acceptance-only** interface, not a runtime
reviewer, finish ritual, model call or guarantee of truth. The old unconditional
`source-grounding` and `stock-N` blockers are replaced by retained-record checks.
No real assessment or live PASS was produced by the interface tests.

### Workflow

1. In the fresh invocation's owned root, inspect **all three** full
   `agent-stock-N-runs.json` employee messages, both Home histories and the
   `agent-stock-N-worker-events.txt` persisted tool streams. Do not assess just
   `report.summary`, titles, snippets, generated check prose, or claimed URLs.
   Inspect actual fetched `content`, `text` or `markdown`, publication dates,
   observation periods, units, denominators, causal statements and limitations.
2. Inventory every material assertion separately (including title/date assertions,
   table cells, calculations, comparisons and conclusions). Explain which precise
   retained passages support it. Mark unsupported or unresolved assertions
   `unsupported` or `unverified`, even when the answer sounds plausible. Do not
   downgrade a factual assertion to a nonclaim to get a pass. Any such verdict
   prevents that sample and the aggregate grounding gate from passing.
3. Only **after inspection**, author `agent-grounding-assessment.json` in that same
   root, mode **0600**, no symlink. Use the exact schema below. All hashes are
   lowercase SHA256. This file is private evaluator evidence, not model output.
   Missing file, wrong identity/digest, empty inventory, missing quotations,
   uncovered answer content, stale current claims and future timestamps fail.
4. Re-evaluate without model calls: `agent_live_contract.py` reads a JSON object
   `{"state": <candidate state>, "evidence": <browser evidence agent object>}`
   from stdin and emits independently evaluated gate results. It does **not**
   alter producer evidence or mark a historical launcher invocation successful.
   `source_grounding(state, evidence)` can validate only the grounding portion;
   the strict `validate_agent_evidence` additionally requires all other gates,
   native adapter evidence (separate from resolver metadata), and matching current-invocation receipts.

Native adapter capture reads the owned `logs/daemon.log.3`, `.2`, `.1`, and
`daemon.log` in chronological rotation order. Missing backups are allowed;
symlinks, nonregular files and rotation during capture fail closed. Both raw
standard-log messages and quoted `msg=` envelopes are decoded before applying
the closed payload-free schema. Resolution-only records remain diagnostics, not
proof that generation occurred. Each observation's owner must match its retained
Bot, and its worker must match the retained run lease when a worker ID is present.
Learning captures therefore also retain the Bot returned by the scoped API.
PID is producer-reported process-namespace metadata (often `1` inside a worker),
not independently authenticated host/container identity; upstream wire identity
also remains unverified.

The existing Go journey waits at a bounded capture/inspection checkpoint before
running structural validation at the end; see `README.bots-acceptance.md` for the
checkpoint signal and timeout. The accountable human/agent supplies the semantic
assessment; the validator checks its structure and evidence bindings, not its
truth. A late offline assessment after the deadline is diagnostic re-evaluation,
**not** a retroactive release pass.
Do not edit `passed`, `receipts` or `status` by hand. Do not reuse state/nonces,
start more paid runs, or change a failed answer just to make the assessment pass.

### Exact schema

Top-level fields (unknown fields are rejected):

- `contract`: `bot-agent-grounding-v2` (v1 assessments no longer satisfy this
  acceptance contract; historical evidence is not rewritten)
- `nonce`, `candidate`, `browser_candidate`: exact state `agent_nonce`,
  `binary_sha256`, `browser_sha256`
- `evaluator`: `{ "identity": "accountable evaluator identity", "kind": "human"
  or "agent", "method": "how the full retained evidence was inspected" }`
- `inspection_started_at`, `assessed_at`: timezone-bearing ISO timestamps,
  after all assessed runs finished and not in the future
- `coverage_policy`: `all-answer-content-v1`
- `samples`: exactly three objects, with `sample` equal to `stock-1`, `stock-2`,
  and `stock-3`; each object has exactly:
  - `sample`, `bot_id`, `run_id`, `session_id`: retained identities
  - `answer_sha256`: SHA256 of UTF-8 **exact** `employee_message.text`, no added
    newline (Home publication is separately checked with its final newline)
  - `sources`: the complete exact `sample.sources` filename-to-digest map
  - `coverage_statement`: evaluator explanation of complete claim inventory
  - `task_adequacy`: exactly `task`, `useful_full_result`, `rationale`, and
    `limitations`. `task` is the exact original `Stock Market News` prompt;
    `useful_full_result` must be JSON boolean `true` for acceptance. The nonblank
    `rationale` must explain why this whole answer usefully fulfills that title
    task, rather than merely offering supported peripheral facts. The nonblank
    `limitations` must assess omissions, freshness, scope and disclosed caveats
    against that task (explicitly explain if no material limitations were found).
    This is the named evaluator's semantic judgment, not something the validator
    infers from keywords, recommendation counts or topic counts. False or missing
    adequacy fails even if all claims are supported. An honest partial response
    may be valid product behavior but cannot satisfy this full-result gate.
  - `claims`: nonempty list of the claim objects below
  - `nonclaims`: list of `{ "start": 0, "end": 3, "quote": "...",
    "reason": "why these exact characters are formatting/nonfactual" }`

Each claim has exactly these fields:

```json
{
  "id": "unique-within-sample",
  "start": 0,
  "end": 35,
  "quote": "At 2026-01-02 the index stood at 100.",
  "verdict": "supported",
  "rationale": "Specific claim-to-evidence reasoning, not blanket approval",
  "evidence": [{
    "call_id": "retained-fetch-call-id",
    "url": "https://example.test/market",
    "payload_sha256": "canonical-payload-digest",
    "field": "content",
    "quote": "the index stood at 100.",
    "rationale": "Why this passage supports the precise value and context"
  }],
  "temporal": {
    "kind": "current",
    "verdict": "aligned",
    "as_of": "2026-01-02",
    "date_quote": "2026-01-02",
    "rationale": "Why the source observation period matches the answer"
  }
}
```

This is a **synthetic schema illustration**, not a valid live assessment. Offsets
are Python Unicode character offsets, half-open `[start,end)`, into the full
answer; exact claim/nonclaim spans must together cover every non-whitespace
character. Overlapping claims are permitted for compound assertions; split their
reasoning rather than hiding several assertions in a blanket summary.
`evidence.quote` must occur literally in the selected fetched body field, with
matching fetch call, URL and payload digest. Search snippets alone never qualify.
For calculations, cite every necessary operand and show the arithmetic in the
rationale; for conclusions, explain the inference and its limits.

Payload hashes use `payload_sha(value)` from `agent_grounding_contract`:
SHA256 of UTF-8 JSON serialized with sorted keys, `ensure_ascii=False`, separators
`(',', ':')`, `allow_nan=False`. These **differ** from Go's raw `source_payloads`
hashes when escaping/spacing differs. `fetched_sources(stream)` exposes the exact
canonical hashes and retained bodies; `stock_capture(state,evidence,key)` returns
`(sample, run, full_answer, fetched_sources)` after verifying capture identity.
Those helpers extract evidence only and never author an assessment.

Temporal `kind` is `current`, `historical`, or `not-time-sensitive`.
`verdict` must be `aligned` to pass; `stale`, `unsupported` or unknown judgments
fail. `as_of` is an ISO date, never later than the run's UTC/local-offset start
date; current stock observations older than four calendar days fail (weekend
allowance, **not** proof of market freshness). Historical assertions require an
explicit matching ISO as-of date in the answer span. Non-time-sensitive assertions
use null `as_of` and null `date_quote`. For dated claims, `date_quote` must occur
in a cited body. The evaluator must interpret natural-language source dates and
periods accurately; literal quotation checks cannot validate that interpretation.
Do not call a time-sensitive assertion timeless, treat fetch time as publication
time, or call old observations current to bypass these checks.

**Trust boundary:** hashes establish byte binding, and spans establish declared
coverage. They cannot prove that every assertion was honestly inventoried, that
a quoted passage entails a claim, or that a source/evaluator is truthful. This
is explicit accountable judgment with auditable decisive quotations, not a
universal mechanical truth certificate. Native adapter provenance and successful
publication remain independent gates. See `agent_grounding_contract_test.py` for
synthetic tampering, incomplete coverage, stale-date and unsupported regressions.

## Evidence checkpoint — 2026-11-13 (historical snapshot)

The full controlled continuity phase verification passed, including owned,
guarded disposable PostgreSQL. Whole-repository Go tests and `make lint` passed
(`.tmp/agent-all-go-verified.log`, `.tmp/agent-lint-final.log`). `make ent-generate`
and the native candidate build passed (`.tmp/agent-generation.log`,
`.tmp/agent-build.log`); `.tmp/astonish-agent-candidate --version` ran successfully.
**No generation-drift comparison has been performed.** These are recorded session
outcomes, not a live-provider journey or rollout.

The latest Python suite is **219 tests, OK (skipped=1)** in
`.tmp/agent-python-validated.log`; the live journey harness Go package
`tests/e2eboot/chat_first_browser` passed its package tests. Those harness tests
are not evidence that the real-model journey ran. Focused launcher and entstore
race selections passed in `.tmp/agent-final-race.log`; the API selection timed
out at 10 minutes without a data-race report. Its `-timeout=30m` rerun was ongoing
at this checkpoint (session `c89e6d52`, `.tmp/agent-api-race-30m.log`), so the
combined race gate is not passed.

The required isolated real-provider configuration remains absent. Full release
acceptance is **blocked** on that prerequisite and the adapter provenance,
source-grounding, terminal parity and broader journey work below. Supplying
configuration alone does not close implementation gaps. No normal `:9393` daemon
restart, deployment or rollout is claimed. See the
[release checklist](../../docs/architecture/bot-release-checklist.md) for the
strict current-invocation gate and historical evidence boundaries.

## Exact remaining full-acceptance work

- Bind actual `provider.GetProvider` adapter request/response metadata to worker
  resolver inputs. Home/run names are resolver parity, not adapter provenance.
- Capture source payloads/digests and assess each material claim against them;
  successful web tools are insufficient.
- Edit an existing saved document and validate its published revision.
- Compare multiple conflicting/stale sources against captured payloads.
- Apply owner feedback, restart the owned daemon, prove supersession and recall.
- Use the same saved skill manually and for three fresh scheduler occurrences.
- Delete that schedule and manually request the work again.
- Inspect ordered live/restored progress/result/learning/delivery receipts in
  built Studio and a real terminal PTY.
- Implement semantic validators for these receipts; enumerating gate names,
  historical receipts, a model assertion, or diagnostic evidence must not pass.

The parent strict driver additionally requires its controlled HTTP, browser/PTY,
disposable PostgreSQL and current-invocation release checks. This diagnostic does
not waive any of those obligations or certify Kubernetes.
