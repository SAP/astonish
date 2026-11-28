# Retained Home setup for an Active Bot

Bots are unreleased and create as Active immediately; there is no Draft lifecycle
or activation onboarding. Creation allocates a conversation ID but not physical
storage, does not queue ordinary work and does not enable recurrence. Explicit
title-first creation admits exactly one requested conversation.

## Setup and permissions

Authorized work prepares required isolated Home resources lazily. Missing runtime,
model or backend prerequisites fail closed without a host fallback. Viewing an
Active, Paused or Stopped Bot does not provision resources or execute work.

An authorized owner/editor may explicitly prepare Home with
`POST /api/studio/bots/{botID}/home?scope=personal|team` and
`{"expected_revision":N}`. The returned server-owned binding, not the preallocated
`home_session_id`, proves registered storage. Missing/empty/zero-UUID Home IDs are
unprepared. Setup changes no lifecycle status and enables no schedule.

Studio exposes preparation for eligible editable Bots without storage, disables
in-flight duplicate submissions, preserves retry on 503 and requires refresh after
409. Read-only/Deleted views cannot prepare. Model status loading/error/empty states
remain distinct; model selection uses the retained Home session, never a run.
Personal owners and team owners/admins may configure eligible idle Active, Paused
or Stopped Bots; active-run/lease barriers and Deleted status reject edits.

## Security and replay

- Public principal, owner, membership, scope and revision checks precede backend IO.
  IDs, client storage paths and public actor fields grant no authority.
- Trusted daemon composition derives exact org/team/user/Bot storage identity;
  backend binding validation and service-only registry attestation remain mandatory.
- Registration rechecks requester and owner membership after IO and inside the
  fenced CAS. Missing callbacks fail closed. Platform membership checks are not a
  claim of distributed serializable revocation-through-commit.
- Exact lost-response retries converge on the same binding. A conflicting edit is
  not a lost-response retry. Failed CAS never deletes shared/concurrently used Home;
  unregistered storage is retained for safe reconciliation.
- Interactive preparation checks the original turn registry, current owner/version,
  queued state and deadline. A prerequisite failure leaves the same run queued and
  unleased; restart requires explicit authenticated continuation, not reconstructed
  authority from a persisted caller ceiling.

400 rejects invalid fields, 401 missing identity, 403 forbidden access, 404 scoped
misses, 409 stale/ineligible/binding conflicts, and 503 unavailable capability.
Neither setup, model selection, question answers nor successful work enables
recurrence. Pause/stop disables schedules; resume does not re-enable them.

## Evidence boundary

`TestBotHomeSetupHTTPConfiguredDaemon` covers production HTTP/tenant routing over
SQLite with controlled backend and authentication, including personal/team editor
and viewer rules, exact retry, forged paths, revocation and mutation during IO.
`--stage active-bot-status` requires fresh exact built-browser/client behavior.
These are not deployed sandbox, PostgreSQL or paid-provider proof. No existing
user DB, ordinary Chats or shared resource cleanup is authorized by this document.
