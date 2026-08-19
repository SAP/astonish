# web/src/api — AGENTS.md

API-specific guidance under the parent [`web/src` guide](../AGENTS.md). Read it first. For streaming chat contracts also read [`docs/architecture/chat-rendering-pipeline.md`](../../../docs/architecture/chat-rendering-pipeline.md); for tenant/auth behavior read [`docs/architecture/multi-tenant-platform.md`](../../../docs/architecture/multi-tenant-platform.md). Scenario-test conventions live in [`docs/architecture/testing-chat-scenarios.md`](../../../docs/architecture/testing-chat-scenarios.md), with executable scenarios under [`web/src/test/scenarios`](../test/scenarios/) and fixtures under [`web/src/test/fixtures/scenarios`](../test/fixtures/scenarios/).

## Ownership and typed REST wrappers

- Keep browser transport in this directory. Components should call exported wrappers rather than constructing `/api/*` requests themselves.
- Define request, response, and callback types beside the wrapper. Return domain values (`Promise<Foo>`, `Promise<void>`, `Blob`, or an `AbortController` for a stream), not untyped JSON or `any`.
- Encode path segments with `encodeURIComponent`, build query strings with `URLSearchParams`, and set `Content-Type: application/json` only for JSON bodies.
- Match names, optionality, casing, status codes, and endpoint paths to the Go handler DTOs. Do not “fix” a backend mismatch with a permissive frontend type. Inspect the relevant handler in `pkg/api/` and update Go, wrapper, consumers, and tests together when a contract changes.

## Auth and team context

- Use `teamFetch` from `teamContext.ts` for authenticated/team-scoped API calls. It owns `X-Astonish-Team`, `X-Requested-With`, single-flight 401 refresh/retry, auth-expiry notification, and rejected-team handling.
- Do not duplicate token refresh, read auth storage directly, or use bare `fetch` to bypass team context. Use `explicitTeam` only when the operation intentionally targets a team other than the globally active team.
- Preserve headers supplied by callers. Team/auth behavior belongs in `teamContext.ts`; endpoint wrappers own only endpoint-specific headers and payloads.

## Response ownership and errors

- The wrapper that receives a `Response` owns consuming its body exactly once. Parse success as the declared type; for non-OK responses, consume useful server text/JSON where appropriate and throw an `Error` with endpoint context.
- Callers own UI presentation, retries beyond the centralized auth retry, and cancellation lifecycle. Do not swallow failures or return empty values unless the existing API explicitly defines best-effort/fallback behavior.
- Clone a response only when middleware must inspect it while preserving the original for its caller (as in team rejection handling).
- Keep text, JSON, and binary paths distinct. Artifact media must use `response.blob()`; never decode video or other binary data as text.

## SSE transport

- Studio streams use `fetch` + `ReadableStream`, not `EventSource`, because the primary stream is a POST. Buffer decoded chunks across reads, split complete SSE records only at blank-line delimiters, parse `event:` and `data:` fields, then JSON-decode the payload before dispatch.
- Preserve backend event names verbatim. Malformed payloads must not corrupt the remaining buffer; transport/HTTP failures go to `onError`, normal EOF to `onDone`.
- `connectChat` and `connectChatStream` must parse and expose the same contract. Any parser correction applies to both paths and needs chunk-boundary, malformed-data, abort, HTTP-error, and EOF coverage.
- Return an `AbortController`; consumers must abort on unmount, session switch, or superseding connection. Treat `AbortError` as intentional completion, release/cancel readers when refactoring, and never leave a background read loop updating stale React state.

## Verification

From `web/`:

```bash
npm test -- src/api/__tests__/studioChat.test.ts
npm test -- src/api/__tests__/teamContext.test.ts
npm test -- src/api/__tests__/<changed-wrapper>.test.ts
npm run typecheck
```

Add focused Vitest coverage for URL/body/header construction, typed success parsing, non-OK responses, auth/team behavior, and stream framing/cleanup. Run the full `npm test` when changing shared transport.
