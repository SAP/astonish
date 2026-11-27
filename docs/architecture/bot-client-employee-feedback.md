# Bot client-local status and Employee speech

Bot Home conversation consists of user input and trusted Employee-authored text
and questions. The server Home projection establishes speech eligibility before
stripping private metadata: private worker/tool prose, fallback publications and
operation summaries are not `text` events. Identical words with different origins
must not be classified by phrases, lifecycle status or client heuristics.

`chat/botOperationProjection.ts` retains exact authorization cards only. Operation
heads continue to drive busy/authority state in StudioChat, including on snapshot
load and replay; no pending, applied, failed, blocked or legacy summary becomes an
agent bubble. Resolving a decision removes the card, not Employee text or files.

`chat/botEmployeeFeedback.ts` maps typed decision/continuation facts to neutral
product status beside the existing controls in `BotReviewCard` and
`BotContinueInteractive`. These acknowledgements are not model speech. Saved
answers remain verbatim; queued continuation is not completed work.
`BotOperationResult` preserves exact authorization scope, expiry and approve/deny
controls without manufacturing first-person explanations.

StudioChat Bot send, SSE error/error_info and reconnect failures use neutral alerts
outside the message area, avatar and assistant controls. Local failures read only
status classification, never response body, message or stack. Unknown outcomes
remain unknown; unsent composer text and attachments remain retryable. Returning
live clears the connection alert, not a rejected-send alert. Ordinary Chat error
and assistant rendering are unchanged.

Empty Home remains empty. Working is neutral durable state, not speech. Stopped,
deleted, paused/tool-free and read-only restrictions remain intact. Verified file
publication events are handled independently of speech eligibility and remain
accessible even when Employee presentation is unavailable.

The terminal uses the same server projection and neutral system state rather than
runtime/client `ItemAgent` entries. Studio and terminal must never reconcile or
execute operations on reads.

Terminal partial/failed publications with no eligible Employee speech persist
`terminal_reason: no_eligible_answer` in the existing `bot_run_state` projection.
The publication transaction binds run/source session and its newly committed
activity sequence, persists the run for snapshot parity, and appends one state-only
Home row with the normal replay-safe run-state identity. Artifact publication is
independent; approved Employee blocker prose remains speech and gets no notice.
Successful, empty and waiting-for-approval runs get no terminal notice. Neither
client renders private report summaries or diagnoses. `BotTerminalStatus` is
mounted outside assistant bubbles; TUI uses the same closed neutral status label.


Focused coverage: `StudioChat.test.tsx` and `botOperationProjection.test.ts` cover
live/restore ordering, same-text different-origin, state-only operations, neutral
errors, controls and artifact preservation. Component tests are not live browser
or real-model verification; the parent acceptance gate owns browser/PTY proof.
