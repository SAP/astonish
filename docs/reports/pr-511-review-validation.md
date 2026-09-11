# PR #511 review validation

**Validated against:** `204ad60152bc451752e63bd97a0526145f0c70d0` on `feat/oauth-mcp-authorization`  
**Date:** 2026-09-11  
**Scope:** Static validation of the supplied review against the current PR head and GitHub status. This report does not apply changes.

## Bottom line

The review's conclusion about **tenant/token isolation is supported by the current code**: no cross-user or cross-tenant execution path was found in the reviewed A2A flow. The two implementation changes that should be made before merging are:

1. **Block SSRF through A2A task push-notification URLs.** This is a concrete, externally reachable abuse path for any valid `a2a` token.
2. **Restore a bounded-resource policy for A2A requests/tasks.** The new endpoint removed the former per-agent rate/task limiter while retaining a five-minute synchronous wait and a 72-hour task lifetime.

The OAuth-client creator-role finding is real as an implementation fact, but whether it is a bug is a **product/security-policy decision** that must be explicitly made. The present documentation/UI terminology calls this administration, which points toward restricting capability-granting client creation to an admin/owner role.

## Finding-by-finding validation

| Review finding | Validation | Classification | Required action |
|---|---|---|---|
| Push-notification SSRF | **Confirmed.** `pkg/api/a2a_routes.go` permits an owner of a task to set `PushNotificationConfig.URL`; `pkg/a2aserver/service.go` asynchronously invokes `PushNotifier` on completion; `pkg/a2a/push.go` creates a request directly from that caller-controlled URL. There is no target validation or redirect denial in this path. | Merge-blocking security fix | Validate at configuration time and immediately before delivery; allow only intended schemes/hosts, reject loopback/private/link-local/ULA/metadata destinations after DNS resolution, pin/validate the dial target to prevent DNS rebinding, and deny redirects. Add rejection and redirect tests. |
| Any tenant member can mint privileged OAuth clients | **Confirmed as described.** `canUseOAuthContext` in `pkg/api/oauth_admin_handlers.go` verifies that the user has an organization membership and a team membership, but does not use the returned organization role to require an administrative role. Client scopes include `a2a`, `tool:execute`, and `chat`. | Policy decision with likely authorization fix | Decide and document the model. If OAuth credentials represent tenant integrations—as the “OAuth administration” UI and docs imply—require an explicit permitted role (`owner`/`admin`, with the exact domain roles confirmed from existing authorization code) for create and tenant-changing operations. Add positive and negative role tests. If self-service is intentional, state that ordinary members may mint service credentials with these capabilities and accept that trust boundary. |
| Rate/task limits disappeared during A2A migration | **Confirmed.** The prior daemon A2A setup constructed an `AgentRateLimiter` and installed agent-specific rate and max-task limits. `pkg/daemon/run.go` now creates `a2aserver.Service` with an in-memory task store, no limiter, a fixed 72-hour TTL, and the service holds synchronous callers for up to five minutes. The service has no per-principal task/concurrency cap. | Merge-blocking availability fix, unless risk is explicitly accepted with a near-term tracked exception | Add bounded concurrency/task-count admission keyed to the authenticated A2A principal, apply it to synchronous and `returnImmediately` paths, release capacity on terminal state/expiry, and rate-limit route entry. Make the synchronous deadline configurable. Test limit rejection and release after completion/cancellation. |
| Agent ownership key lacks tenant component | **Confirmed implementation detail; not currently exploitable through the reviewed path.** Ownership is based on the principal-derived agent identifier; task IDs are generated UUIDs. Validated OAuth claims bind a token to a tenant and the tenant resolver obtains the team from that organization’s scoped store. | Defensive follow-up | Include immutable tenant identity in the task owner key or store it separately and enforce it in `GetTask`/`CancelTask`. This reduces reliance on global subject/client-ID uniqueness and UUID secrecy. |
| Empty-org client guard is redundant | **Confirmed but harmless.** Input validation requires tenant binding before a persisted OAuth client exists. | Cleanup | Optional comment or simplify the check; no merge blocker. |
| Unscoped OAuth-client lister | **Not a currently exposed vulnerability.** The reviewed admin route lists clients by owner. The unscoped store/server method remains an API footgun. | Defensive follow-up | Remove it if unused, or make its administrative/internal use explicit and ensure it cannot be selected by a future tenant-facing handler. |
| MCP chat via in-process `httptest` request | **Not disproven; no direct defect found in this review.** It depends on the target handler continuing to honor the injected principal and tenant context. | Regression-test follow-up | Add a focused MCP-to-chat context propagation test; no required production change identified here. |
| Push retry sleeps in completion goroutine | **Confirmed.** It compounds the missing admission/rate limits but is not independently a tenant-isolation issue. | Included in availability fix | Bound notifier delivery with a context/deadline and consider a bounded worker queue rather than per-completion retry goroutines. |

## Token and tenant isolation result

The supplied review’s main security conclusion is supported:

- OAuth client tenant binding is persisted and immutable after creation.
- Bearer validation establishes a canonical `execution.Principal`; A2A middleware requires the exact `a2a` capability before dispatch.
- Tenant context is derived from the validated principal, not request JSON.
- The A2A resolver loads the organization, opens that organization’s scoped store, then resolves the team from that store. A team from another organization cannot satisfy that lookup.
- A2A task reads/cancellation compare the stored owner key with the caller-derived owner key.

No code change is required specifically for cross-tenant token confusion based on this review. The recommended tenant-qualified task-owner key is defense in depth, not remediation for a demonstrated bypass.

## Test gaps that should be closed

These are valid test additions. The first two should accompany the relevant production fixes.

1. **Push authorization at the HTTP route:** create a task under principal A; verify principal B receives task-not-found for `pushNotification/set`, `get`, and `delete`.
2. **SSRF target validation:** reject loopback, RFC1918, link-local, ULA, metadata, unsupported schemes, and redirect targets; accept a permitted public HTTPS target using a controlled transport/resolver.
3. **OAuth-context authorization:** verify non-members are denied; if role gating is adopted, verify ordinary members are denied and the selected administrative roles are allowed.
4. **Tenant resolver:** explicitly assert a team identifier belonging to another organization is rejected. The scoped lookup already provides this behavior; the test must lock it in.
5. **A2A resource limits:** assert principal-level admission denial at the cap, successful release on terminal/canceled/expired tasks, and a bounded synchronous timeout.
6. **MCP-to-chat context:** assert the handler cannot fall back to an ambient/default tenant when invoked through the MCP bridge.

## Review statements corrected by current evidence

The supplied review said only the CLA status was reported and build/test remained unverified. That is no longer true for the current PR head: GitHub now reports successful **Build**, **Build Extension**, **golangci-lint**, and **ent-generated-code-check** runs, in addition to `license/cla`. This confirms CI build/lint/generated-code checks, though it does not replace focused security behavior tests above.

## Recommended merge disposition

**Do not merge unchanged.** Address SSRF protection and A2A admission/resource bounding first. Resolve the OAuth-client creator authority model explicitly; the recommended default is admin/owner-only credential issuance. Add the four boundary tests most relevant to those decisions (push ownership, SSRF, role policy, cross-org team rejection), with resource-limit tests alongside the limiter implementation.
