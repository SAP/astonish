import { expect, it } from 'vitest'
import { projectBotOperation } from '../botOperationProjection'
import type { BotOperationView } from '@/api/botOperationResult'
import type { ChatMsg } from '../chatTypes'

const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 2, state_sequence: 4, operation: 'schedule_create', status: 'applied', decision: 'execute', summary: 'I could not complete this task.', presentation: 'conversation', source_user_event_id: 'source' }
const source: ChatMsg = { type: 'user', eventId: 'source', content: 'Schedule reports' }
const employee: ChatMsg = { type: 'agent', content: operation.summary }
const artifact: ChatMsg = { type: 'artifact', path: '/report.pdf', toolName: 'write_file' }

it.each(['pending', 'applying', 'applied', 'failed', 'blocked', 'cancelled'] as const)('never turns %s runtime summaries or blockers into Employee speech', status => {
  for (const presentation of [undefined, 'internal', 'conversation', 'legacy'] as const) {
    const messages = [source, employee, artifact]
    const next = projectBotOperation(messages, { ...operation, presentation: presentation === 'legacy' ? undefined : presentation, status, blocker: 'I need permission.' }, true)
    expect(next).toBe(messages)
  }
})
it('removes old operation speech without matching Employee text or discarding artifacts', () => {
  const messages: ChatMsg[] = [source, { ...employee, operationId: operation.operation_id }, artifact, employee]
  expect(projectBotOperation(messages, operation, true)).toEqual([source, artifact, employee])
})
it('keeps exact authorization controls live and restored, then removes them on resolution', () => {
  const decision = { ...operation, status: 'blocked' as const, decision: 'require_authorization' as const }
  const live = projectBotOperation([source, employee, artifact], decision)
  expect(live.map(m => m.type)).toEqual(['user', 'bot_operation', 'agent', 'artifact'])
  expect(projectBotOperation(live, decision, true)).toBe(live)
  expect(projectBotOperation(live, operation, true)).toEqual([source, employee, artifact])
  expect(projectBotOperation([source, employee, artifact], decision, true).map(m => m.type)).toEqual(['user', 'agent', 'artifact', 'bot_operation'])
})
