import { describe, expect, it, vi } from 'vitest'
import { botOperationsBusy, isBotOperation, mergeBotOperation, OperationPresentation, type BotOperationView } from '../botOperationResult'
import { createBotTranscriptMapper, dispatchBotTranscript } from '../botEvents'

const pending: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'schedule', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending', decision: 'execute', summary: 'Change queued.', presentation: OperationPresentation.Internal }
const applied: BotOperationView = { ...pending, revision: 3, state_sequence: 3, status: 'applied', presentation: OperationPresentation.Conversation, summary: "I've created the schedule. It runs every minute. Next run: 2026-11-10 22:31 UTC.", verified_schedule: { trigger_id: 'trigger', schedule_id: 'schedule', version: 1, enabled: true, removed: false, next_run: '2026-11-10T22:31:00Z' } }

describe('operation presentation transport', () => {
  it.each([undefined, ...Object.values(OperationPresentation)])('accepts optional presentation %s', presentation => {
    expect(isBotOperation({ ...pending, presentation })).toBe(true)
  })
  it.each(['', 'hidden', 'Conversation', null, false, 1, {}])('rejects unsupported presentation %j', presentation => {
    const state = { ...pending, presentation }
    expect(isBotOperation(state)).toBe(false)
    expect(mergeBotOperation({}, state, 'bot', 'home')).toEqual({})
    const emit = vi.fn()
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: state as BotOperationView } } }, emit)
    expect(emit).not.toHaveBeenCalled()
  })
  it('dispatches internal live and restored heads without losing busy state or rolling back confirmed results', () => {
    let heads = {}
    const emit = vi.fn((type, state) => { if (type === 'bot_operation') heads = mergeBotOperation(heads, state, 'bot', 'home') })
    const mapper = createBotTranscriptMapper()
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: pending } } }, emit, mapper)
    expect(botOperationsBusy(heads)).toBe(true)
    const applying = { ...pending, status: 'applying' as const, revision: 2, state_sequence: 2 }
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: applying } } }, emit, mapper)
    expect(botOperationsBusy(heads)).toBe(true)
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: applied } } }, emit, mapper)
    expect(botOperationsBusy(heads)).toBe(false)
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: pending } } }, emit, mapper)
    expect(heads).toEqual({ schedule: applied })
    const restored = mergeBotOperation({}, applied, 'bot', 'home')
    expect(mergeBotOperation(restored, applying, 'bot', 'home')).toBe(restored)
  })
})
