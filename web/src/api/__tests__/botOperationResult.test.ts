import { describe, expect, it, vi } from 'vitest'
import { botOperationsBusy, mergeBotOperation, OperationStatus, type BotOperationView } from '../botOperationResult'
import { createBotTranscriptMapper, dispatchBotTranscript } from '../botEvents'
const operation: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending', decision: 'execute', summary: 'Daily weather schedule' }
describe('authoritative operations', () => {
  it.each(Object.values(OperationStatus))('accepts exact %s status', status => {
    const states = mergeBotOperation({}, { ...operation, status }, 'bot', 'home')
    expect(states.one.status).toBe(status)
    expect(botOperationsBusy(states)).toBe(status === 'pending' || status === 'applying')
  })
  it('keeps two queued operations independent through stale replay and reconnect snapshot', () => {
    let states = mergeBotOperation({}, operation, 'bot', 'home')
    states = mergeBotOperation(states, { ...operation, operation_id: 'two', state_sequence: 2 }, 'bot', 'home')
    states = mergeBotOperation(states, { ...operation, status: 'applied', revision: 2, state_sequence: 3 }, 'bot', 'home')
    expect(botOperationsBusy(states)).toBe(true)
    expect(mergeBotOperation(states, operation, 'bot', 'home')).toBe(states)
    expect(mergeBotOperation(states, { ...operation, revision: 1, state_sequence: 9 }, 'bot', 'home')).toBe(states)
    expect(mergeBotOperation(states, states.one, 'bot', 'home')).toBe(states)
    expect(states.two.status).toBe('pending')
  })
  it.each([{ schema_version: 2 }, { status: 'complete' }, { decision: 'allow' }, { bot_id: 'other' }, { home_session_id: 'other' }])('ignores unsupported or cross-scope state %j', delta => {
    const states = {}
    expect(mergeBotOperation(states, { ...operation, ...delta }, 'bot', 'home')).toBe(states)
  })
  it('dispatches durable state outside runtime and ignores unknown versions without raw text', () => {
    const emit = vi.fn(), mapper = createBotTranscriptMapper()
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: operation } } }, emit, mapper)
    expect(emit).toHaveBeenCalledWith('bot_operation', operation)
    emit.mockClear()
    dispatchBotTranscript({ Actions: { StateDelta: { bot_operation: { ...operation, schema_version: 2 } as unknown as BotOperationView } }, Content: { parts: [{ text: 'must not show' }] } }, emit, mapper)
    expect(emit).not.toHaveBeenCalled()
  })
})
