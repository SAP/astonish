# Request-scoped Bot email

Bot production preparation registers the eight existing Chat email schemas through
`launcher.BotEmailTools`. It does not read mailbox credentials, connect to mail,
or use `tools.SetEmailClient` / the daemon's channel client. Registration alone
therefore works without email configuration.

## Configuration and boundaries

Set `bot_email` on the Bot owner's personal settings for a personal Bot, or on
team settings for a team Bot. This is independent of platform/channel email:

```json
{
  "bot_email": {
    "provider": "imap",
    "address": "owner@example.com",
    "imap_server": "imap.example.com:993",
    "smtp_server": "smtp.example.com:587",
    "credential": "mailbox",
    "folder": "INBOX"
  }
}
```

The named credential must exist in that same scope. IMAP/Gmail use a password or
basic credential with a password (optional username defaults to the address).
Microsoft Graph uses `provider: "msgraph"`, address and a named bearer-capable
credential; token resolution happens inside the node. A personal mailbox never
inherits team credentials; a team mailbox never uses a personal viewer's settings
or credentials. Missing configuration or credentials produces an explicit error.

## Execution

1. The factory binds the owner, Bot/run/version and tenant identity, live lease,
   policy and budget to each tool.
2. The email classifier fingerprints the account configuration and binds every
   argument to exact-action approval. This includes recipients, CC, subject,
   body/HTML, message IDs, filters and mutation flags. Read/list/search/wait also
   require approval because message bodies and attachment metadata are sensitive.
3. Only after authorization and budget reservation is the same-scope credential
   vault synchronized to the run's sandbox session. Account identity is checked
   again before dispatch.
4. The private `__astonish_email` node operation creates an explicit per-operation
   `pkg/email` client, invokes the existing Chat handler, closes the client and
   redacts credential values from results/errors. No second mail engine is used.
   Normal daemon-side dispatch rejects this operation outside node network mode.
5. Read-only IMAP clients request `BODY.PEEK[]` so reads do not mark messages seen.
   Channel clients using the default mark-read configuration retain their behavior.

`email_reply` keeps the original `{id, body, html?, reply_all?}` schema. A
separately approved `email_read` captures internal source evidence in the owner's
run catalogue. The reply approval binds the account, original ID/content digest,
resolved To/CC, subject, text/HTML and thread headers. Reply-To is honored, reply-all
excludes the owner and deduplicates CC, and outgoing attachments are always empty.
Ambiguous or injected headers fail closed. A matching durable approval checkpoint
can restore the exact snapshot after restart; model history cannot supply it.

Before sending, the node reads the original without marking it seen and rebuilds
its snapshot. Any changed source, recipient or content denies the send. The source
digest covers complete parsed text/HTML, not only the displayed body prefix; the
normal `email_read` display limit remains unchanged. Graph sends explicit MIME via
`sendMail`, never provider-resolved `reply`/`replyAll`; SMTP uses the same reviewed
recipients and thread headers. Exact grants are consumed once even when dispatch
fails, and child policies cannot send. After a blocking vault push, the launcher
rechecks live lease/isolation, account and cancellation before any node dispatch.

## Verification and limitations

`go test ./pkg/launcher -run '^TestBotEmail' -count=1 -timeout=120s` exercises guarded production
adapters against controlled SMTP and Graph services. It covers personal/team
credential isolation, no eager credentials, all eight unchanged schemas,
missing-account/no-fallback errors, denied and replayed approvals, changed
recipients/content/account, read and attachment permission, and redaction.
`go test ./pkg/email -count=1 -timeout=120s` includes a controlled IMAP protocol
assertion for peek versus default mark-read behavior.
`go test ./pkg/tools -run '(Email|Reply)' -count=1 -timeout=120s` checks the existing
email handler compatibility. All three commands passed on 2026-11-05. The focused
reply/dispatch suite also passed with the race detector:

```sh
go test -race ./pkg/launcher -run '^TestBotEmail(Reply|RechecksAfterVaultPush|DispatcherTargetBinding)' -count=1 -timeout=120s
```

The reply-specific `TestBotEmailReplyGraphSnapshot` proves one explicit MIME send
for an unchanged approval, checkpoint snapshot restoration, tampered checkpoint
rejection, changed Reply-To/CC/source text (including beyond the display limit),
subject/thread/attachment metadata, outgoing body/HTML/reply-all/ID/account and
cached recipient rejection. Child invocation and post-vault revocation cannot
reach the node. A controlled HTTP 504 after receipt of the MIME message returns
an error and cannot reuse its consumed approval; this tests an ambiguous response,
not hard socket-timeout interruption or delivery. Every case rejects grant replay.
`TestBotEmailReplySMTPSnapshot` checks the same reviewed SMTP MIME recipients and
thread headers. Checkpoint restoration here is in-process, not a worker restart
or a complete owner-approval UI/API journey.

These tests exercise the real handlers and node dispatcher through an in-process
node-protocol test adapter, not a deployed Docker/OpenShell sandbox or real mail
provider. Graph read/manage tests redirect the fixed Graph hostname only in test
transport. Existing IMAP/SMTP raw socket operations do not comprehensively honor
context cancellation; the request context timeout alone is not proof of hard
socket interruption. Sandbox lifecycle/network policy remains the outer boundary.
