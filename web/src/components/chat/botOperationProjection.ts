import { OperationDecision, OperationStatus, operationSource, type BotOperationView } from '@/api/botOperationResult'
import type { ChatMsg } from './chatTypes'

// Heads retain authority/busy state. Only exact authorization is a transcript
// card; runtime summaries never become Employee speech, even on legacy replay.
export function projectBotOperation(messages: ChatMsg[], operation: BotOperationView, replay = false): ChatMsg[] {
  const id = operation.operation_id
  const index = messages.findIndex(m => (m.type === 'agent' || m.type === 'bot_operation') && m.operationId === id)
  const needsDecision = operation.status === OperationStatus.Blocked && operation.decision === OperationDecision.RequireAuthorization
  if (!needsDecision) return index < 0 ? messages : messages.filter((_, i) => i !== index)
  const message: ChatMsg = { type: 'bot_operation', operationId: id, eventId: `operation/${id}` }
  if (index >= 0) {
    if (JSON.stringify(messages[index]) === JSON.stringify(message)) return messages
    return messages.map((m, i) => i === index ? message : m)
  }
  if (!replay) {
    const source = operationSource(operation.source_user_event_id)
    const at = source ? messages.findIndex(m => m.type === 'user' && m.eventId === source) : -1
    if (at >= 0) return [...messages.slice(0, at + 1), message, ...messages.slice(at + 1)]
  }
  return [...messages, message]
}
