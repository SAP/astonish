# Employee narration from safe outcome facts

`pkg/bot/employee_message.go` owns composition, not execution or assessment authority.

## Exact assessed communication

A validated accepted candidate, or a validated blocked response on a partial run,
is preserved byte-for-byte with its candidate digest. No writer or reviewer is
called for that path. Unassessed success is never narrated by a model.

## Safe facts and fallback

For failed/partial work the writer receives JSON containing:

- `source_digest`: binding to the report and status, excluding the envelope;
- `outcome`: always `incomplete`;
- `run_status`: `failed` or `partial`;
- `failure_kind`: native failure classification;
- `reason`: server-selected typed failure reason;
- `reason_explanation` and `required_action`: server-owned descriptions of that reason;
- `completion_claims_allowed`: always false.

The reason whitelist distinguishes unknown operational failure, unverified result,
setup failure, budget restriction, unknown model cost, input exceeding budget,
usage overrun, rejected model response, multiple tool calls, response size limit,
interruption, authorization denial, unsupported capability, expiration, missing
resource, invalid request, and revision/lease conflict. Specificity comes only from
native typed errors or identities, never from parsing provider error prose.
Generic execution-format failures do not establish that tool requests were never
executed; the specific native checkpoint errors do establish that fact for the
rejected response only.

Raw errors, report summaries, technical details, criterion prose, source bodies,
endpoints and tool arguments are excluded. `Unverified` does not imply unknown
external effects. Unknown safe reasons remain explicitly unknown rather than
inventing the cause. Fallback text explains the recognized reason and user action;
it allocates **no new budget**, calls no model and retries no work.

## Bounded generation and review

The writer contract is `BOT_EMPLOYEE_MESSAGE_V2`:

```json
{"source_digest":"supplied digest","outcome":"incomplete","text":"employee explanation"}
```

The reviewer contract is `BOT_EMPLOYEE_MESSAGE_REVIEW_V1`. It receives the same
facts, `proposed_text` as untrusted data, and its SHA-256 `text_digest`, and returns:

```json
{"source_digest":"supplied digest","text_digest":"supplied text digest","outcome":"incomplete","approved":true}
```

Only the exact reviewed text can be published. Each invocation is tool-free,
limited to 512 output tokens and 4096 accumulated JSON bytes. The explanation is
limited to 1600 bytes; URI schemes, common URL prefixes and unsafe control bytes
are rejected. These checks are not a general semantic or secret detector. The
review explicitly checks incomplete-work communication, safe reason/action fidelity,
and absence of unsupported success/effect/permission claims or private diagnostics.

There are at most **two model calls**: one writer and one reviewer, with no repair
loop. Both use the existing owner LLM and assessment-purpose context. The existing
reservation/settlement wrapper must still reserve each call; live boundary checks
run before and after each call. A denied review discards the draft. Usage from both
calls, including failed calls with usage metadata, is counted. Format errors,
unknown JSON fields, missing/mismatched digests, wrong outcomes, rejected review,
interruption, tool output or capacity failure return the deterministic fallback.
Budget, setup and interrupted failures use fallback directly.

## Downstream schema and validation boundary

No exported Go signature or persisted `BotEmployeeMessage` schema changes here.
`Origin=model` now means generated text that passed this bounded review, not a
choice among fixed messages. Source digests remain integrity bindings, not
attestations. Current store validation does not reproduce the semantic review or
persist its outcome; it must not treat arbitrary caller-supplied model envelopes
as reviewed proof. Model review is fallible, not a guarantee against every false
claim. This implementation does not add success authority.

A downstream persistence hardening change should restrict model envelopes to
failed/partial status and persist server-owned typed `outcome=incomplete` plus
review provenance bound to source/text digests (and validate it at the trusted
commit boundary). Merely adding a model-supplied `approved` field is not an
attestation. That store/API migration is outside this file's ownership and is not
implemented here.

## Verification

`go test ./pkg/bot -count=1` covers actual safe fact requests (not choices), novel
reason-specific prose, false completion rejected by review, a wrong typed outcome,
invalid/partial/tool responses, stale review binding, review refusal, budget denial
before review without retries, two-call usage accounting, typed fallbacks, and
unassessed-success rejection. These are controlled-model library tests, not live
provider or end-to-end runtime proof.
