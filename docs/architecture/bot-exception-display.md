# Bounded operation exception display

Routine operation escalations render in Studio's `BotOperationResult`, not the
legacy proposal/approval `BotReviewCard`. Both live events and restored snapshots
use the same durable operation ID. These are authorization decisions, never
`ask_user` answers.

## Visible contract

The card displays the action as a human heading (for example, `memory_forget` is
**Forget information**). The transport-only `Change` field is redundant with that
heading and is not raw JSON UI. All remaining allowlisted exception fields retain
their exact caption/value, including resource IDs, recipients and costs when
provided. The saved-action-only scope and exact `expires_at` timestamp must be
visible before either decision. Expired and read-only cards retain the details
but have no decision controls. Missing details do not become permission.

The terminal `/bots exceptions` panel displays the selected operation's complete
allowlisted fields, saved-action-only scope and exact UTC expiry in both browsing
and confirmation states. It never renders the payload digest. Existing owner,
revision, expiry and server-side authorization enforcement remains unchanged.

## Employee acceptance observer

`tests/e2eboot/chat_first_browser/employee_exception_display.go` requires one
visible `article[data-operation-id]` for the saved operation. Durable API readiness
is not a browser render barrier: the observer waits at most 30 seconds (or the
caller's shorter deadline) for that exact visible card, allowing normal SSE,
snapshot polling and React rendering to catch up. It never reloads, requests a
product refresh, or replays the user action. Only an absent exact visible card is
retryable; duplicate cards, browser/evaluation errors, and malformed evidence
remain failures. Once present, it validates the
exact human action heading and visible `dt`/`dd` pairs, not substring matches in
whole-page prose. `Change` is accepted only when it matches the persisted
operation enum and the exact expected human heading is present; every other
field and the expiry is mandatory. Hidden, foreign, duplicate, mismatched or
prose-only evidence cannot satisfy the gate. This fixes the raw-enum-versus-human
label mismatch without dropping authorization scope requirements.

No changes were made to the employee deny/no-repeat or approve/exactly-once
ledger checks, decision coordinates, backend grants, operator ceiling or worker
behavior. A passed DOM/unit test is not fresh live employee acceptance.

## Focused verification

- `go test ./tests/e2eboot/chat_first_browser ./pkg/tui`
- `cd web && npm test -- --run src/components/bots/__tests__/BotOperationExceptionDisplay.test.tsx src/components/bots/__tests__/BotOperationResult.test.tsx`
- Build Studio, then `ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./pkg/api -run '^TestBotEmployeeEscalationBrowserAcceptance/(approve|deny)$' -count=1`
  exercises native-browser Studio plus real production handlers and temporary
  SQLite: exact goal/expiry visibility, deny with zero goal effects, approve with
  one goal, read-only restrictions and canonical reload without another model run.
- `ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./tests/e2eboot/chat_first_browser -run '^TestEmployeeExceptionDOMObserverBrowser$' -count=1`
  exercises the actual observer in native Chromium with controlled DOM; it is
  explicitly not a live-provider or production-store acceptance test.
