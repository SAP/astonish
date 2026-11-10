# Bot owner-account email delivery

Bot owner email discovery is supplied by the daemon's per-run captured owner resolver, not the current browser viewer or model-supplied identifiers. It rechecks active account, organization and team membership before exposing the canonical account email and delivery readiness to owner-bound control context. Profile and destination strings are data, not instructions or grants.

A known account email is not automatically verified. Native Bot notifications may use an enabled, verified owner email channel link only when it exactly matches the owner's current account email. A changed account address, revoked link, foreign link, inactive account/membership or altered recipient fails authorization. Existing private Telegram checks remain. Discovery provides an exact binding only when the email transport is connected; enabling Bot delivery still uses reviewed configuration, not discovery alone.

States distinguish missing account email, verification required, transport unavailable, unavailable discovery and ready for review. Guidance avoids asking for an address already on the account and directs actual setup to Connected Channels or the administrator. The existing authenticated email-link code API can resolve an omitted address from the live account; verification codes are still required. No mailbox credentials are exposed and Bot mailbox tools are not a fallback for notification delivery.

At send time, the existing delivery authorizer revalidates the persisted reviewed binding and current owner/link. The channel manager requires connected email transport and removes reply/thread context so mailbox message content cannot substitute another recipient. A transport send is not an inbox-delivery receipt; existing uncertain-send accounting remains in force.

## Verification

`TestBotOwnerEmailIdentity`, `TestResolveBotOwnerEmailDelivery`, `TestBotOwnerEmailControlContext`, `TestBotOwnerEmailResolverCapturedScopeAndReload`, `TestChannelLinkEmailLiveAccount`, `TestChannelLinkEmailHTTPRejectsMissingAccount`, and `TestBotNotificationsScopedConfigurationAndRevokedDeferredSend` cover identity, scope, discovery, reviewed configuration and revocation. `TestBotOwnerEmailNativeTransport` runs the native channel manager and email channel against an isolated email client, checking the exact recipient and denial before transport. No test sends actual email to a user's mailbox.
