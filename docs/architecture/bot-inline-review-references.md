# Canonical inline Bot review references

Proposal creation and pending approval creation append a `bot_runtime_event`
with `type: bot_review` and payload `{kind: proposal|approval, review_id: UUID}`
to the Bot home transcript in the same transaction as the authoritative record.
The existing home publication adapter performs the scoped owner/home binding
check; there is no parallel transcript or event outbox. Proposal references
commit before the capture tool returns. Approval references use the common
pending-creation path, including background runs, rather than UI callbacks.

The reference has a stable event ID and a separate invocation ID, so it does not
suppress canonical model content or compete with the runtime turn sequence.
Creation replay is handled by existing proposal/approval idempotency; decisions
never append another review card. Previously saved transcripts are not backfilled:
clients can use the review inbox as a recovery path for those records.

Clients must hydrate the reference from proposal snapshots/listings and approval
records, normalize the wire `review_id`, and deduplicate by review kind and ID.
A reference is not consent or a grant; existing digest, actor, version, expiry,
lease, and lifecycle guards remain unchanged.

Minimal HTTP Bot creation permits an omitted/blank charter and supplies a
conservative discussion-and-review charter. All other version defaults are
unchanged; creation does not activate the Bot, provision its physical home, or
grant permissions. Explicit charters remain unchanged.

Verification: `TestBotConversationalControlHTTPAcceptance` uses real HTTP,
SQLite and production ChatRunner for both scopes, checking review references
precede tool output and replay through the home history endpoint.
`TestBotReviewReferencesPersistOnce` covers proposal replay and approved-record
hydration without extra cards for both scopes. `TestBotMinimalCreateDefaults`
covers missing/blank/explicit charters, and
`TestBotChatFirstConversationHTTPAcceptance` exercises minimal HTTP creation and
real worker conversation across SQLite reopen. These are controlled fixtures,
not live external-provider or Studio browser proof.
