import { expect, it } from 'vitest'
import { projectBotOperation } from '../botOperationProjection'
import { groupChronologicalActivity, isHardBreak, isSoftNote } from '../toolActivity'
import type { BotOperationView } from '@/api/botOperationResult'
import type { ChatMsg } from '../chatTypes'

const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 2, state_sequence: 4, operation: 'schedule_create', status: 'applied', decision: 'execute', summary: 'Schedule confirmed.', presentation: 'conversation', source_user_event_id: 'source' }
const source: ChatMsg = { type: 'user', eventId: 'source', content: 'Schedule reports' }
const report: ChatMsg = { type: 'agent', content: 'Later report' }

it('repairs snapshot-only placement on durable replay and preserves duplicates in place', () => {
  const fallback = projectBotOperation([source, report], operation)
  expect(fallback.map(m => 'content' in m && m.content)).toEqual(['Schedule reports', 'Schedule confirmed.', 'Later report'])
  const durable = projectBotOperation(fallback, operation, true)
  expect(durable.map(m => 'content' in m && m.content)).toEqual(['Schedule reports', 'Later report', 'Schedule confirmed.'])
  expect(projectBotOperation(durable, operation, true)).toBe(durable)
  expect(projectBotOperation([], operation, true)).toHaveLength(1)
})
it.each(['failed', 'blocked', 'cancelled'] as const)('projects %s and blocker as ordinary assistant text', status => {
  expect(projectBotOperation([], { ...operation, status, blocker: 'Explanation' }, true)[0]).toMatchObject({ type: 'agent', content: 'Schedule confirmed.\n\nExplanation' })
})
it('keeps internal work hidden, compatibility and authorization cards intact', () => {
  expect(projectBotOperation([], { ...operation, presentation: 'internal', status: 'pending' }, true)).toEqual([])
  for (const presentation of [undefined, 'internal', 'conversation'] as const) {
    expect(projectBotOperation([], { ...operation, presentation, status: 'blocked', decision: 'require_authorization' }, true)[0].type).toBe('bot_operation')
  }
  expect(projectBotOperation([], { ...operation, presentation: undefined }, true)[0].type).toBe('bot_operation')
})
it('keeps operation publications outside tool activity without discarding mixed text', () => {
  const [confirmation] = projectBotOperation([], operation, true)
  const messages: ChatMsg[] = [{ type: 'tool_call', toolName: 'schedule', toolArgs: {} }, { type: 'agent', content: 'Mixed deliverable' }, confirmation, report]
  expect(isHardBreak(confirmation)).toBe(true)
  expect(isSoftNote(confirmation)).toBe(false)
  expect(groupChronologicalActivity(messages)).toContainEqual({ kind: 'passthrough', index: 2 })
  expect(messages[1]).toMatchObject({ content: 'Mixed deliverable' })
})
