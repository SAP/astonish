# PR #511 Review Round 2 — Validation

**Reviewed branch:** `feat/oauth-mcp-authorization`  
**Scope:** The two medium findings, stated test gaps, and the four minor notes from the supplied content-only review.  
**Method:** Current-branch source inspection only. This report does not replace the previously run build/test verification.

## Verdict

The two medium findings are valid, but they have different urgency:

1. **Service-principal personal-memory handling is confirmed and should be fixed before merge.** The current guard blocks service principals from obtaining `memory:read` or `memory:write`, but the chat handler constructs personal-memory stores with an empty user key before a tool-level authorization decision occurs. The handler also injects an empty-key personal store into the selectable memory-store map even when the request did not explicitly select personal mode.
2. **A2A ownership is only agent-ID-qualified and the defense-in-depth observation is correct.** There is no demonstrated current cross-tenant break because the authenticated principal produces the ownership ID and task IDs are server-generated. Nevertheless, tenant qualification is absent from both persisted task ownership and every read/cancel/push predicate. It should be addressed as a defense-in-depth hardening item, preferably before merge if the task model can be changed without migration risk.

The review's suggested extra tests are warranted. The DNS-rebinding test is also a useful hardening regression test, but its absence does not negate the already implemented dial-time validation mechanism.

## Confirmed Medium Finding 1 — Service principals can reach `ForUser("")`

**Status: confirmed.**

### Evidence

- `pkg/api/mcp_routes.go:88-99` correctly substitutes `principal.ClientID` for `TenantContext.UserID` when the OAuth client-credentials principal has no subject.
- `pkg/api/chat_handlers.go:633-644` then reads `principal.Subject` directly into `userID`; this remains empty for a service principal.
- `pkg/api/chat_handlers.go:1154-1171` accepts `memoryScope: "personal"` and calls `orgStore.ForUser(principal.Subject).Memories()`, which becomes `ForUser("")`.
- More broadly, `pkg/api/chat_handlers.go:1173-1179` unconditionally injects `Personal: orgStore.ForUser(principal.Subject).Memories()` into the runner's memory-store map. Thus a service principal reaches `ForUser("")` even with the default team memory scope.
- `pkg/execution/authorizer.go:31-41` rejects service principals for `memory:read` and `memory:write`; that prevents a demonstrated data read/write through the memory tools, but it is downstream of construction and injection of the invalid owner-key store.

### Required change

Treat a service principal as ineligible for personal memory at the **handler boundary**. The safe design is:

- only construct or inject `Personal` memory stores when `principal.Kind != execution.PrincipalKindService` and `principal.Subject != ""`;
- reject an explicit `memoryScope: "personal"` request from a service principal with a clear forbidden response, rather than silently falling back to team scope;
- preserve the existing client-ID fallback only for the tenant routing context—it must not become an implicit personal-memory identity unless product policy explicitly defines a separate service-owned memory namespace.

### Required regression coverage

Add a focused API test that invokes the MCP-to-chat path (or `StudioChatHandler` with a service OAuth principal), requests `memoryScope: "personal"`, and proves that:

- the request is rejected before `TenantRouter.ForOrg(...).ForUser("")` is called;
- default service-principal chat does not inject a personal memory store;
- a normal user principal retains the existing personal-memory behavior.

## Confirmed Medium Finding 2 — A2A task ownership is not tenant-qualified

**Status: confirmed as defense-in-depth; no current exploit demonstrated.**

### Evidence

- `pkg/api/a2a_routes.go:197-205` creates the ownership key from `Actor:Subject`, `Subject`, or `ClientID`, with no organization or team component.
- `pkg/api/a2a_routes.go:91`, `107`, `119`, and `214-233` pass only that key to send, get, cancel, and push-notification ownership checks.
- `pkg/a2aserver/service.go:101` persists only `identity.AgentID` in the task store.
- `pkg/a2aserver/service.go:217-235` authorizes get/cancel using only `task.AgentID == agentID`.
- `pkg/a2aserver/service_test.go:56-77` and `pkg/api/a2a_routes_test.go:105-142` prove cross-principal denial, but neither constructs two tenant contexts with the same ownership key.

### Security assessment

The currently authenticated caller does not supply the `agentID`, and task IDs are server-generated UUIDs. Those facts make this an unlikely present-day tenant escape. Still, the authorization predicate relies on a global-ID uniqueness invariant outside the task model and does not encode the tenancy boundary in depth.

### Recommended change

Represent A2A ownership as a typed, tenant-qualified identity rather than as a bare string. At minimum, include both resolved `OrgSlug` and `TeamSlug` in the stored task owner and compare all three fields for get, cancel, and push configuration operations. Use the same qualified key for active-task quotas and session continuity where appropriate, preventing cross-tenant coupling if an identifier collision ever becomes possible.

If changing the persisted task shape is unsuitable for this PR, a short-term alternative is a canonical escaped/composite ownership key built from org, team, and current agent identity. The typed model is preferable because it avoids delimiter ambiguity and makes the security boundary explicit.

### Required regression coverage

Add a task created under `(org-a, team-a, same-agent-id)` and prove a principal under `(org-b, team-b, same-agent-id)` cannot get, cancel, or set/get/delete its push configuration. This must test the service/route authorization path, not only a helper that produces a different key.

## Minor Notes

| Review note | Validation | Recommendation |
|---|---|---|
| `A2AStreamHandler` ignores `ReturnImmediately` | **Confirmed.** `pkg/api/a2a_routes.go:156` delegates to `Service.SendMessage`; `Service.SendMessage` waits synchronously unless `params.Configuration.ReturnImmediately` is already true (`pkg/a2aserver/service.go:112-133`). The SSE route emits only after this call returns. | Product/behavior decision, not an isolation defect. Either document that stream waits for the first completed task result or make the stream route force asynchronous dispatch and stream task events. |
| Hand-rolled IPv6 ULA check | **Not independently revalidated in this pass.** | Prefer `net/netip`/standard-library predicates where their semantic coverage matches the policy; retain explicit deny rules for ranges not covered by the standard predicate. |
| Empty-scope tokens | **Likely intentional but needs policy confirmation.** Endpoints still demand exact scopes, so this is not privilege escalation. | Either reject empty-scope issuance or document such a token as an authenticated identity token with no callable protected-resource capabilities. |
| Default `http://localhost:9393` A2A base URL | **Configuration concern.** | Ensure production startup requires/derives a public external URL, and add a production-config validation test if one does not already exist. |

## Additional Test-Gap Assessment

- **DNS rebinding at dial time:** Add a deterministic resolver/dialer-injection test that returns a public address during URL validation and a blocked address during dial-time resolution, asserting no connection occurs. This verifies the protection promised by the implementation instead of only checking static URL rejection.
- **Cross-org foreign-team resolver:** Add a resolver-layer test proving an organization cannot resolve a team ID owned by another organization. This is valuable because it protects the tenant boundary before the tenant middleware constructs scoped stores.

## Suggested Merge Decision

Do not treat the review as identifying a current cross-tenant breach. However, the service-principal `ForUser("")` behavior is a real invariant violation and should be fixed with regression coverage before merge. Tenant-qualified A2A ownership is a sound defense-in-depth follow-up; it should be included now if the task ownership model can be extended cleanly, otherwise track it explicitly with a near-term security hardening issue and test plan.
