# Interactive Bot clarification contract

## Employee-operation evidence boundary — 2026-11-08

Clarification questions remain distinct from operation authorization. Studio and terminal employee-operation controls do not turn assistant prose into a question or grant authority by display. Current UI implementation is not proof of a live provider decision: **LIVE acceptance remains pending**. Historical nine-stage smoke receipts do not validate this newer contract; controlled daemon HTTP and isolated PostgreSQL evidence must be reported separately.

Interactive Bot Chat uses the shared `ask_user` tool for clarification that genuinely blocks the current request. It does useful clear work first, rather than starting an onboarding questionnaire. Open-ended missing input uses `kind=text`; meaningful choices use `kind=select` with `allow_custom_answer=true`. The user need not request a tool explicitly.

The instruction is confined to interactive Bot Chat in `pkg/agent/bot_prompt.go`. Background runs and ordinary Chat retain their existing behavior. No renderer converts assistant prose into a question card; only a genuine tool result follows the existing shared question/checkpoint pipeline.

`TestInteractiveBotClarificationProviderContract` checks the actual production-factory provider request (full system instruction and actual function declarations), then emits controlled text/select tool calls and verifies typed question capture. This is deterministic wiring coverage, not proof of a live model's choice. Fresh real-provider browser acceptance remains necessary.

## Home-chat delivery and canonical questions

Scheduled updates default to the Bot's own home chat with the existing typed delivery policy and no external bindings. Ask about recipients only when the owner explicitly requests an external destination; external changes still require review.

Both Bot transcript stream endpoints project redundant question markers using the existing Chat marker parser and persisted approval identity. The exact runtime waiting publication is redundant only alongside that saved question review; other failure reports, arbitrary prose, and later human input remain visible. Projection never deletes stored rows or changes cursor positions; run diagnostics retain the original history. Studio renders the pending QuestionCard once without duplicated heading or permission-style Deny/pending chrome. Remote terminal clients receive the same projected transport.

Boston regression fixtures preserve the read-only reported session payload in `pkg/api/testdata/boston-question-history.sse`. `TestBostonQuestionProjection`, `TestQuestionWaitingEvidenceOnlyPersistedCapture`, and `TestBostonScheduleNeedsNoDeliveryBinding` cover projection, narrow uncertainty handling, and schedule default compatibility.

`TestBostonQuestionCleanBrowserAcceptance` (opt in with `ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1` after `cd web && npm run build`) loads the recorded question payload in disposable SQLite through real scoped HTTP/SSE and the built Studio SPA. It checks live/reloaded single question rendering, retained Details diagnostics, later assistant messages, and preservation of quoted `[chat_question]` in ordinary Chat. It never submits the owner's actual Boston approval. `TestBotAmbiguousTitleQuestionBrowserAcceptance` separately exercises actual typed-answer submission and worker continuation with a controlled model.
