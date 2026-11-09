# Bot-home model selection adapter

The existing session model URLs have a narrow Bot-home adapter:

- `GET /api/studio/sessions/{home_session_id}/model-status` uses scoped Bot read authorization. Team viewers can inspect the team's home pin, not their own personal defaults.
- `PATCH /api/studio/sessions/{home_session_id}/model` accepts a provider/model pair (or both empty to clear). The scoped repository revalidates live owner/admin configuration authority. Personal scope remains owner-only. Draft, paused and stopped Bots without an active run/lease may be edited; active/deleted Bots conflict. Run conversations are never mutable model targets through these routes.
- Optional `scope` must match the server-selected session repository. Session linkage, retained-home ID, owner and Bot scope must agree before pins are read. Foreign or mismatched requests receive a non-disclosing 404.
- Pins persist using the existing personal/team `SessionPin` services. An explicit complete home pin wins. Otherwise `store.ResolveBotModelSelection` inherits platform → organization → team → owner personal defaults (personal Bots only). Worker execution, activation readiness and model status share this resolver; failed settings reads fail closed and partial pins are invalid. No implicit pin is written, no viewer personal defaults are consumed, and the ordinary Chat singleton is not swapped.

Ordinary session ownership, generic Bot Chat execution denial, and transcript access are unchanged. No UI or terminal rendering change is required; existing callers use these URLs.

`TestBotStorageHTTPAcceptance` covers both home scopes, reloads, team admin edits, viewer read-only access and foreign scope/user denial. `TestBotRuntimeHTTPAcceptance` selects the model through HTTP, verifies worker resolution, denies active home edits and run model routes. `TestAuthorizeBotModelConfiguration` covers central authorization and active-run rejection.

`TestResolveBotModelSelection` covers precedence, partial pins and read failures at every settings layer. `TestBotActivationConfiguredRuntime` checks inherited readiness/execution parity for both scopes without activating the draft. `TestBotChatFirstConversationHTTPAcceptance` compares inherited HTTP status with the real worker's selected model across SQLite reopen, without a physical home or preparation callback.

These are controlled HTTP/SQLite tests, not a completed live daemon/provider smoke. Pin authorization and persistence are separate operations, not a distributed serializable membership/lifecycle transaction; simultaneous lifecycle changes are not proven here.
