# pkg/memory — AGENTS.md

Memory ingestion, chunking, embedding, retrieval support, scenario-card parsing, and conservative scenario identity matching.

## Invariants
- Chunking must be deterministic for identical path/content/options. Preserve stable category/path metadata and content hashes; do not create empty chunks.
- Embedding implementations obey the same dimensionality/error contract. Do not silently mix vector dimensions or treat failed embeddings as valid zero vectors.
- Scenario cards are durable structured records. Keep parser/render round trips and backward-compatible fields stable when extending the format.
- Scenario identity matching is deliberately conservative. Auto-merge requires the configured high threshold (`DefaultScenarioAutoMergeThreshold`) and no negative signals; plausible lower scores are review-only.
- Distinct environments, resource types, operations, endpoints, and credentials are safety signals. Prefer duplicate review over a false automatic merge that destroys operational distinctions.
- Candidate selection must remain deterministic, and near-tied merge candidates must fail closed rather than selecting arbitrarily.
- Memory scope follows store scope. Never combine another user/team/org corpus merely to improve recall.

## When editing
- Changes to scenario extraction/scoring need positive, negative, ambiguity, and corpus-rarity tests.
- Changes to chunking/embedding require ingestion and retrieval compatibility checks.
- API health/map behavior and agent memory injection may depend on these structures; trace those consumers before renaming fields.

## Verification

```bash
go test ./pkg/memory/...
go test ./pkg/memory -run 'Test.*(Chunk|Scenario|Identity|Embed)'
```

Include `./pkg/api/... ./pkg/agent/...` for wire- or prompt-visible changes.

## References
- [`pkg/store/AGENTS.md`](../store/AGENTS.md)
- [`pkg/agent/AGENTS.md`](../agent/AGENTS.md)
