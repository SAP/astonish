# Bot client-local employee feedback

Bot client acknowledgements are deterministic, not another model turn. Studio's
`chat/botEmployeeFeedback.ts` accepts typed decision/continuation facts and supplies
one employee voice for `BotReviewCard` and `BotContinueInteractive`. These messages
use `BotEmployeeMessage` beside the existing exact controls. A saved answer is
acknowledged as an answer (including when paused), not mislabeled as an approval.
Queued work is not reported as completed.

Continuation and decision transport failures go through `botLocalFailure`, which
reads only status classification, never response body, message, or stack. Unknown
outcomes remain unknown. These local messages are not execution evidence and are
not persisted as assessed results.

Employee-origin saved question prompts/options remain verbatim in `QuestionCard`.
Authorization still uses the saved digest, run/question identity, revision, and
stable idempotency key. Fresh continuation requires its separate explicit click;
read-only, expiry, pause, stop, and deletion restrictions are unchanged. No API or
backend execution contracts change.

The terminal's `bot_employee_feedback.go` maps typed `BotContinuationState` receipts
to completed `ItemAgent` entries. It does not emit `NewSystem`, synthesize a model
stream, complete a running turn, or alter the pending approval. Ordinary Chat
system/error rendering remains unchanged.

Coverage: `BotContinueInteractive.test.tsx`, `BotReviewCard.test.tsx`,
`BotReviewFeedback.test.tsx`, the Bot intent scenarios, and
`pkg/tui/bot_employee_receipt_test.go` exercise the safe feedback, exact controls,
question preservation, and ordinary Chat boundary. These are component/model
tests, not live browser/provider verification.
