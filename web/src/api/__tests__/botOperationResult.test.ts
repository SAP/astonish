import { beforeEach, describe, expect, it, vi } from 'vitest'
import { botOperationsBusy, mergeBotOperation, OperationStatus, type BotOperationView } from '../botOperationResult'
import { connectBotEvents } from '../botEvents'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.mocked(teamFetch).mockReset())
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
  it('delivers canonical durable state while the consumer ignores unknown versions without raw text', async () => {
    const unknown = { ...operation, schema_version: 2 }
    const frames = [operation, unknown].map((data, index) => {
      const cursor = `home:${index + 1}`
      return `id: ${cursor}\nevent: bot_operation\ndata: ${JSON.stringify({ ...data, _projection: { event_id: `home/operation-${index}/0`, session_id: 'home', run_id: 'run', cursor } })}\n\n`
    }).join('')
    vi.mocked(teamFetch).mockResolvedValueOnce(new Response(`${frames}id: home:2\nevent: done\ndata: {"persisted":true}\n\n`))
    let states = {}
    const emit = vi.fn((type, data) => {
      if (type === 'bot_operation') states = mergeBotOperation(states, data, 'bot', 'home')
    })
    const done = vi.fn(), error = vi.fn()
    const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', onEvent: emit, onDone: done, onError: error })
    try {
      await vi.waitFor(() => expect(done).toHaveBeenCalledOnce())
      expect(error).not.toHaveBeenCalled()
      expect(emit.mock.calls).toEqual([['bot_operation', operation], ['bot_operation', unknown]])
      expect(states).toEqual({ one: operation })
      expect(emit.mock.calls.some(([type]) => type === 'text')).toBe(false)
    } finally { controller.abort() }
  })
})
