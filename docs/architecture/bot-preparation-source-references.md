# Preparation source references

Preparation uses server-owned short transport labels, not model-copied UUIDs.
`prepareBotWorkContract` supplies the complete unchanged pinned contract with an
additional top-level `available_source_ids` dictionary:

```json
{
  "assignment": {"source_id": "assignment", "contract_path": "instructions"},
  "charter": {"source_id": "charter", "contract_path": "charter"},
  "goal_1": {"source_id": "goal:<exact pinned UUID>", "contract_path": "goals[0]"},
  "attachment_1": {"source_id": "attachment:<exact pinned identity>", "contract_path": "attachments[0]"}
}
```

`botWorkSourceReferences` derives goal and attachment aliases from their immutable
contract order. Records point to the bounded original source, avoiding duplicate
source text or summaries. Nothing is fetched, and no credentials or new authority
are introduced. The input retains existing assessment size limits.

Preparation's `source_ids` schema enum contains only these short aliases. The
model returns e.g. `assignment`, `charter`, `goal_1`; it never needs to copy a UUID
or digest. Runtime resolves exact dictionary keys before `BotWorkState.Validate`.
The mapping is held locally and derived only from the contract, not from model
responses or source content. Lookalike labels and embedded mapping instructions
have no authority. There is no prefix, substring, regex or fuzzy matching.

Persisted criteria still contain the exact original source IDs. Legacy injected
models returning exact pinned source IDs remain valid at the validation boundary;
these legacy identifiers are deliberately not advertised in the model enum.
Unknown aliases, foreign IDs, omitted source coverage and forbidden exclusions
still fail. Only goals can be excluded with a reason; assignment, charter and
attachments cannot. Goal ownership validation is unchanged. Semantic failures
have no retry. Invalid JSON retains only the existing single format correction.

Review evidence refs and candidate digests retain their existing protocol and
schema enums. This change does not imply acceptance, publication or successful
execution of any work.

## Offline verification

`TestBotWorkShortSourceReferences` exercises preparation with two UUID goals and
an attachment, exact persisted identity, the actual model-input dictionary,
dynamic schema, legacy compatibility, foreign ownership, wrong references,
coverage and exclusion denial. It uses one scripted call (two only for malformed
JSON recovery); no production database or provider is contacted.

```sh
go test ./pkg/api -run 'TestBotWork(ShortSource|Identity|VerificationSchema|AttachmentAssessment|StructuredAssessmentWire)' -count=1
go test ./pkg/api ./pkg/store -run 'TestBotWork' -skip 'Live|ModelReproduction|ModelOnly|HomeModel|CitationModel' -count=1
```

Live model and scheduler behavior require separate verification; these offline
checks do not establish production readiness.
