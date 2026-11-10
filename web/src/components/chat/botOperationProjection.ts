import { OperationDecision, OperationPresentation, OperationStatus, operationSource, type BotOperationView } from '@/api/botOperationResult'
import type { ChatMsg } from './chatTypes'

// Heads own authority/busy state; transcript entries own chronological placement.
// Equal-head replay still projects a missing entry after a reset/snapshot load.
export function projectBotOperation(messages: ChatMsg[], operation: BotOperationView, replay = false): ChatMsg[] {
  const id = operation.operation_id
  const index = messages.findIndex(m => (m.type === 'agent' || m.type === 'bot_operation') && m.operationId === id)
  const previous = messages[index]
  const needsDecision = operation.status === OperationStatus.Blocked && operation.decision === OperationDecision.RequireAuthorization
  if (operation.presentation === OperationPresentation.Internal && !needsDecision) {
    return index < 0 ? messages : messages.filter((_, i) => i !== index)
  }
  const conversation = operation.presentation === OperationPresentation.Conversation && !needsDecision
  const message: ChatMsg = conversation
    ? { type: 'agent', content: [operation.summary, operation.blocker].filter(Boolean).join('\n\n'), operationId: id, operationReplay: replay || (previous?.type === 'agent' && previous.operationReplay), _streaming: false }
    : { type: 'bot_operation', operationId: id, eventId: `operation/${id}` }
  // A durable publication supersedes a snapshot-only placement (or a pending
  // legacy card). Duplicate replay never moves an already replayed publication.
  const relocate = replay && conversation && index >= 0 && (previous?.type !== 'agent' || !previous.operationReplay)
  if (index >= 0 && !relocate) {
    if (JSON.stringify(previous) === JSON.stringify(message)) return messages
    return messages.map((m, i) => i === index ? message : m)
  }
  const next = relocate ? messages.filter((_, i) => i !== index) : messages
  if (!replay) {
    const source = operationSource(operation.source_user_event_id)
    const at = source ? next.findIndex(m => m.type === 'user' && m.eventId === source) : -1
    if (at >= 0) return [...next.slice(0, at + 1), message, ...next.slice(at + 1)]
  }
  return [...next, message]
}
