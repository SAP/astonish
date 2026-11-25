import { beforeEach, describe, expect, it, vi } from 'vitest'
import { botOperationsBusy, isBotOperation, mergeBotOperation, OperationPresentation, type BotOperationView } from '../botOperationResult'
import { connectBotEvents } from '../botEvents'
import { teamFetch } from '../teamContext'

vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.mocked(teamFetch).mockReset())

async function streamOperations(states: unknown[], onOperation: (state: unknown) => void) {
  const frames = states.map((state, index) => {
    const cursor = `home:${index + 1}`
    return `id: ${cursor}\nevent: bot_operation\ndata: ${JSON.stringify({ ...state as object, _projection: { event_id: `home/row-${index}/0`, session_id: 'home', run_id: 'run', turn_id: 'turn', sequence: index + 1, cursor } })}\n\n`
  }).join('')
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(`${frames}id: home:${states.length}\nevent: done\ndata: {"persisted":true}\n\n`))
  const done = vi.fn(), error = vi.fn()
  const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', onEvent: (type, data) => { if (type === 'bot_operation') onOperation(data) }, onDone: done, onError: error })
  try {
    await vi.waitFor(() => expect(done).toHaveBeenCalledOnce())
    expect(error).not.toHaveBeenCalled()
  } finally { controller.abort() }
}

const pending: BotOperationView = { schema_version: 1, bot_id: 'bot', home_session_id: 'home', operation_id: 'schedule', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending', decision: 'execute', summary: 'Change queued.', presentation: OperationPresentation.Internal }
const applied: BotOperationView = { ...pending, revision: 3, state_sequence: 3, status: 'applied', presentation: OperationPresentation.Conversation, summary: "I've created the schedule. It runs every minute. Next run: 2026-11-10 22:31 UTC.", verified_schedule: { trigger_id: 'trigger', schedule_id: 'schedule', version: 1, enabled: true, removed: false, next_run: '2026-11-10T22:31:00Z' } }

describe('operation presentation transport', () => {
  it.each([undefined, ...Object.values(OperationPresentation)])('accepts optional presentation %s', presentation => {
    expect(isBotOperation({ ...pending, presentation })).toBe(true)
  })
  it.each(['', 'hidden', 'Conversation', null, false, 1, {}])('rejects unsupported presentation %j at the canonical consumer boundary', async presentation => {
    const state = { ...pending, presentation }
    expect(isBotOperation(state)).toBe(false)
    expect(mergeBotOperation({}, state, 'bot', 'home')).toEqual({})
    const accepted = vi.fn()
    await streamOperations([state], data => {
      if (isBotOperation(data)) accepted(data)
      expect(mergeBotOperation({}, data, 'bot', 'home')).toEqual({})
    })
    expect(accepted).not.toHaveBeenCalled()
  })
  it('dispatches internal live and restored heads without losing busy state or rolling back confirmed results', async () => {
    let heads = {}
    const busy: boolean[] = []
    const applying = { ...pending, status: 'applying' as const, revision: 2, state_sequence: 2 }
    await streamOperations([pending, applying, applied, pending], data => {
      heads = mergeBotOperation(heads, data, 'bot', 'home')
      busy.push(botOperationsBusy(heads))
    })
    expect(busy).toEqual([true, true, false, false])
    expect(heads).toEqual({ schedule: applied })
    const restored = mergeBotOperation({}, applied, 'bot', 'home')
    expect(mergeBotOperation(restored, applying, 'bot', 'home')).toBe(restored)
  })
})
