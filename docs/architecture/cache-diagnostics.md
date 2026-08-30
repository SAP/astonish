# Model Cache Diagnostics

Studio can record cache diagnostics for model calls without changing model-visible requests. The feature is available only in platform mode to users with the platform `superadmin` role.

## Request stability

The main agent exposes exactly three provider-visible declarations in stable order: `search_tools`, `describe_tools`, and `execute_tool`. Domain tools remain behind `execute_tool`; searching the catalog never injects declarations. The system instruction is frozen for a session, and changing per-turn context is persisted as user-role history. Tool schemas and JSON are canonicalized recursively before provider conversion.

Diagnostics are request-scoped. Enabling the browser-local Debug toggle adds a recorder through `context.Context`; it does not mutate the shared agent or alter the request sent to the model.

## Capture semantics

Each model round records:

- the ADK model request's canonical SHA-256 and ordered element hashes;
- the estimated reusable prefix in elements and bytes, plus the first divergent path;
- provider/model identity, streaming mode, response count, first-response latency, and total duration;
- normalized token usage and provider-reported cached-token count when available;
- a bounded, sanitized representation of the canonical ADK request.

`captureLevel: canonical-adk` means the payload is captured immediately before the provider adapter receives it. It must not be described as exact HTTP wire JSON. Provider SDKs may still transform the request. A cache hit is provider-confirmed only when usage metadata reports cached tokens. Prefix counts are Astonish estimates and are displayed separately.

## Secret and binary handling

Diagnostics never store transport headers. Before persistence, known credential values and values under sensitive field names are replaced with `[REDACTED]`. Large base64 and data-URL values are replaced by deterministic length/hash markers. Stored payload JSON is capped at 128 KiB; an oversized payload is replaced by a valid JSON truncation manifest containing its sanitized size and digest. Error strings pass through the same credential redactor.

## Persistence and access

Diagnostics are separate from ADK transcript events so they cannot affect future model history. Personal and team session stores retain the latest 100 rounds per session, and session deletion cascades to diagnostics. Rows are associated by ADK invocation ID and model-call number; the UI never infers turns from message position.

`GET /api/studio/sessions/{id}/cache-diagnostics?invocationId=...` resolves the session through the authenticated request's tenant-scoped stores and requires platform-superadmin authorization. The cross-organization administrative endpoint under `/api/platform/admin/` additionally requires explicit scope identifiers. The Studio panel is available only to platform superadmins and shows sanitized payloads, truncation/elision notices, timing, usage, provider cache status, and the estimated stable prefix.
