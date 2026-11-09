# Studio Bot memory and persona review

`web/src/components/bots/BotMemoryCharterPanel.tsx` is a standalone panel, exported
from `BotPanels.tsx`, with props `{ bot: Bot; readOnly: boolean; onChange?: () => void }`.
The workspace must derive `readOnly` from authenticated ownership/administrative
access, not persona prose. Keep the panel mounted while switching workspace tabs
if unsaved drafts should survive tab navigation. A change of Bot ID or scope
intentionally resets its local state; a revision refresh does not.

## Existing transport contracts

All requests use `teamFetch` and an explicit `scope` query parameter through the
new `web/src/api/botMemory.ts` client. No generic cross-scope memory endpoint is used.

- `GET /api/studio/bots/{id}/documents`: current projected persona, charter,
  goals, memory, people/group indexes and notes, with source mappings.
- `PUT /api/studio/bots/{id}/documents/{document}`: allowlisted SOUL.md,
  IDENTITY.md, USER.md and CHARTER.md edits with expected Bot revision, reason,
  and explicit confirmation.
- `GET /api/studio/bots/{id}/memory-health`: current bounded source records,
  provenance, fingerprints and review-only recommendations.
- `POST /api/studio/bots/{id}/documents/{document}/ingest`: explicitly reviewed
  record mappings; not raw Markdown import.
- `POST /api/studio/bots/{id}/memory-health/consolidate`: separately reviewed
  replacement record with at least two fingerprint-bound sources.
- `POST /api/studio/bots/{id}/corrections`: owner evidence, source identity,
  replacement, expected Bot revision and stable request UUID. HTTP 202 is only
  recorded intent, never evidence of applied memory.
- `POST /api/studio/bots/{id}/corrections/{correctionID}/reconcile`: bodyless,
  separate application of the saved intent. Success is claimed only when
  `memory_applied` is true.

## Review and conflict invariants

Edits move from editing to review to saving. Any content/evidence change invalidates
review. Non-conflict errors retain the reviewed request for retry, including its
UUID and observation timestamp. A 409 retains all local text and blocks re-submit.
Reload fetches comparison data separately: it never replaces the draft. The user
must explicitly accept the current revision/source fingerprints, then review again.
Mapped-source rebase fails closed if any source is absent from the bounded snapshot.

Read-only projected Markdown is never submitted as authority. Reviewed mapping
writes create attributed replacements and retain original evidence and the receipt.
Persona content cannot assign rights, verified evidence, actors, or tool policy.
Recommendations are never executed automatically. The UI renders raw provenance
as text/JSON, not executable HTML.

Document and health loading/errors are independent. GET requests abort on unmount
and identity changes. Read-only controls, empty states, retry, saving, conflict and
receipt states are covered by focused component/API tests.

## Current boundaries

The panel has no correction-history listing endpoint; accepted correction receipts
are retained in the current mounted editor only. It does not fetch historical
persona versions, paginate beyond the backend bounded snapshot, create source-less
memory records, or delete raw evidence. Mapped edits propose one replacement record
per explicit operation, using the first source's session as the displayed target.
Goal-linked correction selection is not yet exposed (`goal_ids` is empty).

Verification is focused Vitest interaction/transport testing and TypeScript checks.
A live authenticated Studio/backend acceptance run remains required at integration.
