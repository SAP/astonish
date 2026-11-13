import type { BotContinueReceipt } from '@/api/botReviews'
import type { AgentMessage } from './chatTypes'

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
  unavailable: "I can’t confirm the latest outcome right now. Please refresh before trying again; your work may still be running.",
  access: "I can’t access this Bot with your current authorization. Please check your access and refresh. I can’t confirm the outcome of your work.",
  changed: "I need you to refresh and review the current saved action before trying again. I can’t confirm this request’s outcome.",
  decision: "I can’t confirm whether your decision was saved. Please refresh this conversation before trying again.",
  attachments: "I can’t accept attachments in this conversation. Please remove them before sending; I haven’t attached the files.",
}
// Never read message/body/stack: local errors are not evidence of execution failure.
// Global failures without an authenticated Bot need no model call or invented identity.
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
  blocked_by_pause: {
    approval: 'I saved your approval, but I’m still paused.',
    answer: 'I saved your answer, but I’m still paused.',
  },
  queued: {
    approval: 'I saved your approval and queued the continuation. I haven’t completed the work yet.',
    answer: 'I saved your answer and queued the continuation. I haven’t completed the work yet.',
  },
}

// Local acknowledgements use only typed, confirmed facts. They need no model call,
// and never interpret or rewrite the employee's saved question or action scope.
export function botEmployeeFeedback(event: BotFeedbackEvent): string {
  switch (event.kind) {
    case 'decision_saved': return decisionMessages[event.continuation][event.decision]
    case 'approved_checkpoint': return event.paused
      ? 'I saved your approval, but I’m still paused. Resuming needs a separate review.'
      : 'I saved your approval. You can continue only this saved checkpoint.'
    case 'review_required': return 'I need you to review this exact saved change before I proceed.'
    case 'reconciliation_pending': return 'I saved the change, but I haven’t confirmed its outcome yet. Please don’t submit a replacement.'
    case 'fresh_proposal_required': return 'I need a fresh proposal before you can authorize a changed action. Please ask me in Chat.'
    case 'answer_authorization_required': return 'I need fresh authorization before I can submit your answer. Please select Continue interactive for this saved question.'
    case 'continuation_authorization_required': return 'I need your authorization to continue this turn. I won’t send your original message again.'
    case 'read_only': return 'I need the owner to decide; this review is read-only.'
  }
}

export function botFailureMessage(error?: unknown, fallback?: BotFailureKindValue): AgentMessage {
  return { type: 'agent', content: botLocalFailure(error, fallback).message, _streaming: false }
}
