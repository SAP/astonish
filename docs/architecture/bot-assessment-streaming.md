# Bot assessment streaming

`pkg/api/bot_runner_work.go:botWorkJSON` requests internal provider streaming for preparation, candidate review and pending-control review. This avoids requiring a provider to finish the entire JSON assessment before sending HTTP response headers. It does not publish partial assessments to Studio, the terminal or Bot state, change producer execution, or change provider transport timeouts.

## Completion and failure invariants

- ADK `Partial` content is accumulated as deltas. Nonpartial content replaces that accumulator with the authoritative full response (the OpenAI and SAP adapter convention). Providers that ignore streaming and emit final-only ADK responses remain supported.
- A nonpartial content response is required before decoding. A usage-only frame does not certify partial content as complete. The iterator must terminate without error; a later interruption, provider error or tool call invalidates even syntactically valid JSON received earlier.
- `Interrupted`, `ErrorCode`, `ErrorMessage`, iterator failures and function calls fail closed without format correction. No assessment failure replays the producer or tools.
- Only completed malformed JSON permits the existing one format correction (two calls maximum). Full original input, pinned response schema, strict decoding, coverage/identity validation, response-size bounds and authorization/budget checks remain unchanged. A rejected response does not mutate the caller's output destination.

## Usage

Each model call contributes the maximum observed cumulative prompt and candidate token counts, not a sum of chunk snapshots. Increases are accounted before checking errors, preserving usage from a failing call. A format correction starts new per-call maxima and adds its usage to previous attempts. Existing usage and tool counts remain intact.

## Verification

`bot_runner_work_stream_test.go` covers split partials plus cumulative final content, an authoritative final differing from deltas, final-only compatibility, repeated/decreasing usage, incomplete and interrupted JSON, provider/iterator failures, tool calls, and format-retry accounting including a failing second call. Its local HTTP/SSE harness uses the actual OpenAI-compatible adapter and checks successful and truncated streams. `TestBotWorkStructuredAssessmentWire` checks streaming plus strict schemas and retry accounting for all three assessment prompts.

```sh
go test ./pkg/api -run 'TestBotWork|TestBot.*Assessment|Test.*Pending' -count=1
```

These are package and local HTTP fixtures, not a claim that a live SAP deployment or its approximately 120-second HTTP/2 header timeout has been reproduced or resolved end-to-end.
