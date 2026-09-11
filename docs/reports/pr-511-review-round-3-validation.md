# PR #511 Review Round 3 Validation

## Scope

Validated the two medium-severity findings from the latest security review against `feat/oauth-mcp-authorization` after commit `b9dbf7d4`.

## Finding 1: Membership changes after OAuth token issuance

**Conclusion: accepted bounded-lifetime behavior; documented, no runtime change.**

The A2A and MCP tenant resolvers in `pkg/launcher/studio.go` resolve the signed token's organization and team IDs to canonical slugs, but intentionally do not query current user membership. The bearer middleware validates the Astonish-signed token, exact capability, and tenant claims before dispatch. The OAuth refresh flow similarly rotates and reissues from the stored refresh-token family without re-checking membership.

This creates a bounded revocation-latency window: a user removed from a team can use an already-issued access token until `AccessTokenTTL` expires. A refresh-token family that includes `offline_access` must be revoked during de-provisioning to prevent replacement access tokens. This is not a cross-tenant escalation because the client registration fixes tenant authority and tenant IDs are signed and backend-resolved.

`docs/architecture/oauth-authorization.md` now records the behavior and operational containment requirements: revoke the token family or client for immediate de-provisioning and retire the signing key if necessary.

## Finding 2: Push dial-time resolver bypass

**Conclusion: confirmed and fixed.**

`PushNotifier.ValidatePushURL` used the injected `resolveHost` function, while the HTTPS transport's `DialContext` independently called `defaultResolveHost`. Production behavior was safe because both paths used the system resolver, but custom resolvers could not faithfully test the DNS-rebinding guard at connection time.

`pkg/a2a/push.go` now constructs its transport with a closure that reads the notifier's current resolver. The validation and dial-time checks therefore use the same resolver source, including in tests. `TestSafePushTransportRejectsNonPublicAddressFromResolver` verifies that a resolver answer of `127.0.0.1` is rejected before a connection is opened.

## Verification

- `gofmt -w pkg/a2a/push.go pkg/a2a/push_test.go`
- `go test ./pkg/a2a -run 'Test(PushNotifier|SafePushTransport)'`
- `git diff --check`

All completed successfully.
