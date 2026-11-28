import type { BotContinueReceipt } from '@/api/botReviews'
import type { ErrorMessage } from './chatTypes'

export const BotFailureKind = {
  Unavailable: 'unavailable', Access: 'access', Changed: 'changed',
  Decision: 'decision', Attachments: 'attachments',
} as const
export type BotFailureKindValue = typeof BotFailureKind[keyof typeof BotFailureKind]
export interface BotLocalFailure {
  source: 'local'
  kind: BotFailureKindValue
  outcome: 'unknown'
  message: string
}
const messages: Record<BotFailureKindValue, string> = {
  unavailable: 'Updates unavailable. Refresh before trying again; work may still be running.',
  access: 'Bot access unavailable with the current authorization. Check access and refresh; the work outcome is unknown.',
  changed: 'Refresh and review the current saved action before trying again. This request’s outcome is unknown.',
  decision: 'Decision receipt unavailable. Refresh this conversation before trying again.',
  attachments: 'Attachments unavailable in this conversation. Remove them before sending; no files were attached.',
}
// Local status is not execution evidence. Never read message/body/stack.
export function botLocalFailure(error?: unknown, fallback: BotFailureKindValue = BotFailureKind.Unavailable): BotLocalFailure {
  const status = error && typeof error === 'object' && 'status' in error ? error.status : undefined
  const kind = status === 401 || status === 403 ? BotFailureKind.Access : status === 409 || status === 412 ? BotFailureKind.Changed : fallback
  return { source: 'local', kind, outcome: 'unknown', message: messages[kind] }
}
export type BotFeedbackEvent =
  | { kind: 'decision_saved'; decision: 'approval' | 'answer'; continuation: BotContinueReceipt['continuation'] }
  | { kind: 'approved_checkpoint'; paused: boolean }
  | { kind: 'review_required' | 'reconciliation_pending' | 'fresh_proposal_required' | 'answer_authorization_required' | 'continuation_authorization_required' | 'read_only' }

const decisionMessages: Record<BotContinueReceipt['continuation'], Record<'approval' | 'answer', string>> = {
  blocked_by_pause: { approval: 'Approval saved. Bot remains paused.', answer: 'Answer saved. Bot remains paused.' },
  queued: { approval: 'Approval saved. Continuation queued; work is not yet complete.', answer: 'Answer saved. Continuation queued; work is not yet complete.' },
}
// Deterministic product status beside controls, never Employee speech.
export function botEmployeeFeedback(event: BotFeedbackEvent): string {
  switch (event.kind) {
    case 'decision_saved': return decisionMessages[event.continuation][event.decision]
    case 'approved_checkpoint': return event.paused
      ? 'Approval saved. Bot remains paused; resuming requires a separate review.'
      : 'Approval saved. Only this saved checkpoint can be continued.'
    case 'review_required': return 'Review this exact saved change before proceeding.'
    case 'reconciliation_pending': return 'Change saved. Outcome not yet confirmed; do not submit a replacement.'
    case 'fresh_proposal_required': return 'A changed action requires a fresh proposal. Request one in Chat.'
    case 'answer_authorization_required': return 'Fresh authorization required to submit this answer. Select Continue interactive for this saved question.'
    case 'continuation_authorization_required': return 'Authorization required to continue this turn. The original message will not be sent again.'
    case 'read_only': return 'Owner decision required. This review is read-only.'
  }
}

// Compatibility for callers: error state must never have an agent message type.
export function botFailureMessage(error?: unknown, fallback?: BotFailureKindValue): ErrorMessage {
  return { type: 'error', content: botLocalFailure(error, fallback).message }
}
