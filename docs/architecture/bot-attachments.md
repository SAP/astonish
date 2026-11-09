# Bot attachments

Bot Thread uses StudioChat's native file picker, paste and drop handlers and the same base64 AttachmentPayload as ordinary Chat. Failed submissions retain the draft files; successful admission clears them. Read-only threads reject file input.

POST runs validates attachments after authenticated Bot scope lookup. The scoped admission transaction stores ChatFile values in the existing BotRun JSON data alongside instructions and includes them in its idempotency hash. No schema or separate file store is added. Worker LoadRunInput and supervisor forward only the persisted bytes. The Bot Chat adapter reuses studioUserContent and InjectChatFiles so multimodal model input and sandbox file resolution follow ordinary Chat infrastructure. Resume uses the persisted transcript rather than duplicating the original user attachments. No host fallback is introduced.

## Private output attachments

Successful `load_file` uses ChatAgent's existing artifact capture and ChatRunner's `artifact` event, not a second node-side publication callback. The durable sink records metadata under `Actions.StateDelta.bot_runtime_event`; no file body is added to model content. Home-facing runs project canonical artifact envelopes to the Bot home while retaining the run copy, with deterministic event IDs and the same atomic lease and Bot/session checks. Other runtime state remains run-local. The existing Bot transcript mapper replays this event into StudioChat's private download card; report markers, public sharing, and authenticated artifact download authorization are unchanged.

Regression coverage: `TestBotBuiltinFileOutcomesThroughRunner` proves exactly one artifact from the real built-in node dispatcher through ChatRunner. `TestBotHomeArtifactProjection` proves live cursor/reload equality, retry deduplication and cross-Bot isolation in personal/team stores; `TestBotHomeArtifactEventBoundary` rejects unrelated or mismatched envelopes.
