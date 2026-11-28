# Studio adaptive schedule conversion

`BotDraftDialog` offers **Convert adaptive schedule** for non-Chat intake. Users enter an existing adaptive job ID; no new jobs-list service is required. `BotAdaptiveConversion` calls the existing authorized `GET /api/studio/bots/convert-adaptive/{jobID}?scope=...` endpoint and displays the exact cron, timezone, instructions and source `updated_at` review token. Failed lookups and source changes clear the preview and block conversion.

The dialog persists `mode: adaptive` and `source_id` through the existing unfinished-intake writer. Name and charter remain editable. Back preserves these edits but clears consent; Escape saves unfinished intake only. Restoring an intake requires a fresh preview and new explicit consent. Consent and preview tokens are deliberately not durable authorization.

After explicit review, `botAdaptive.ts` posts the existing `BotConversionRequest` DTO with `expected_updated_at`, name, charter, idempotency key, `reviewed` and `reviewed_for_team`. The token is kept as a string to preserve database timestamp precision. The DTO does not expose an action digest: `expected_updated_at` is the review/CAS binding. Identical retries keep their key; changed source review or content produces a new request identity. A conflict returns the user to editing and requires a new preview.

Conversion relies on the existing backend transaction to create a supervised draft with a disabled copied schedule and pause the original as its final write. The source job is retained, not deleted; transaction rollback retains the original unchanged. Studio never independently pauses or deletes the source and never calls activate or enables a trigger during conversion. Activation and schedule enablement remain separate reviewed actions in the Bot detail workflow. An ambiguous transport error is not represented as proof that the transaction failed; the UI offers an idempotent retry.

Credentials, delivery settings, parameters, execution history and owner/team bindings are not copied. Only the reviewed schedule/instructions and explicit name/charter cross the conversion boundary; unsupported persona controls are hidden in adaptive mode.

## Verification

- `cd web && npx vitest run src/api/__tests__/botAdaptive.test.ts src/components/bots/__tests__/BotAdaptiveConversion.test.tsx src/components/bots/__tests__/BotDraftDialog.test.tsx`
- `cd web && npm run typecheck`
- Backend rollback contract: `go test ./pkg/store/entstore -run TestBotAdaptiveConversionRollback -count=1`

The Studio tests render the dialog against controlled API responses. They are not evidence of a live browser/daemon deployment.
