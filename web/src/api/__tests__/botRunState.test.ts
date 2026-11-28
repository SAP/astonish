import { beforeEach, expect, it, vi } from 'vitest'
import { botRunsBusy, mergeBotReceipt, mergeBotRunState, RunStatus, type BotRunState } from '../botRunState'
import { connectBotEvents } from '../botEvents'
import { teamFetch } from '../teamContext'
import type { BotMessageReceipt } from '../botReviews'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.mocked(teamFetch).mockReset())

async function receive(events: [string, object][]) {
  const frames = events.map(([type, data], index) => {
    const cursor = `home:${index + 1}`
    return `id: ${cursor}\nevent: ${type}\ndata: ${JSON.stringify({ ...data, _projection: { event_id: `home/row-${index}/0`, session_id: 'home', run_id: 'r', turn_id: 'turn', cursor } })}\n\n`
  }).join('')
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(`${frames}id: home:${events.length}\nevent: done\ndata: {"persisted":true}\n\n`))
  const emit = vi.fn(), done = vi.fn(), error = vi.fn()
  const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', onEvent: emit, onDone: done, onError: error })
  try {
    await vi.waitFor(() => expect(done).toHaveBeenCalledOnce())
    expect(error).not.toHaveBeenCalled()
    return emit.mock.calls
  } finally { controller.abort() }
}
const state: BotRunState = { schema_version: 1, bot_id: 'bot', run_id: 'r', session_id: 'trace', home_session_id: 'home', presentation: 'home_interactive', status: 'running', state_sequence: 3 }
const merge = (s: Parameters<typeof mergeBotRunState>[0], patch: Partial<BotRunState> = {}) => mergeBotRunState(s, { ...state, ...patch }, 'bot', 'home')
it('keeps public work identical across replay and rejects cross-run, session and revision regressions', () => {
  const work: NonNullable<BotRunState['work']> = { schema_version: 1, protocol: 'agent_led_v1', bot_id: 'bot', run_id: 'r', work_revision: 3, phase: 'working', items: [] }
  const live = merge({}, { work })
  expect(merge(live, { work })).toBe(live)
  for (const patch of [{ work: { ...work, run_id: 'other' } }, { work: { ...work, bot_id: 'other' } }, { work: { ...work, work_revision: 2 } }, { session_id: 'other', work }]) expect(merge(live, { ...patch, state_sequence: 4 })).toBe(live)
  const final = merge(live, { work: { ...work, work_revision: 4, disposition: 'answered', phase: 'finished' }, status: 'succeeded', state_sequence: 5 })
  expect(merge({}, final.r)).toEqual(final)
  expect(merge(final, { state_sequence: 6, status: 'succeeded' }).r.work).toEqual(final.r.work)
  expect(merge(live, { work: { ...work, schema_version: 99 }, state_sequence: 4 }).r.work?.schema_version).toBe(99)
})

it('keeps two quick turns independent, clears only on durable waiting/terminal state', () => {
  let s = merge({})
  s = merge(s, { run_id: 'r2', status: 'queued' })
  s = merge(s, { status: 'succeeded', state_sequence: 4 })
  expect(botRunsBusy(s)).toBe(true)
  s = merge(s, { run_id: 'r2', status: 'waiting_approval', state_sequence: 5 })
  expect(botRunsBusy(s)).toBe(false)
})
it.each(Object.values(RunStatus))('projects %s without interpreting transport as execution', status => {
  expect(botRunsBusy(merge({}, { status }))).toBe(status === 'queued' || status === 'running')
})
it('rejects stale replay, unknown schema, foreign bot/home and dedicated presentation', () => {
  const s = merge({})
  for (const patch of [{ state_sequence: 2 }, { schema_version: 2 }, { bot_id: 'other' }, { home_session_id: 'other' }, { presentation: '' }]) expect(merge(s, patch)).toBe(s)
})
it('never lets an HTTP receipt overwrite newer SSE or snapshot state', () => {
  const receipt = { run: { id: 'r', bot_id: 'bot', session_id: 'trace', status: 'queued' }, home_session_id: 'home', user_event_id: 'u', history_cursor: 999 } as BotMessageReceipt
  const done = merge({}, { status: 'succeeded' })
  expect(mergeBotReceipt(done, receipt, 'bot', 'home')).toBe(done)
  const seeded = mergeBotReceipt({}, receipt, 'bot', 'home')
  expect(botRunsBusy(seeded)).toBe(true)
  expect(botRunsBusy(merge(seeded, { status: 'waiting_approval', state_sequence: 1 }))).toBe(false)
  expect(mergeBotReceipt({}, receipt, 'bot', 'foreign')).toEqual({})
})
it('routes canonical state-only metadata and backend-sanitized owner answers', async () => {
  const events = await receive([
    ['bot_run_state', state],
    ['user_message', { text: 'Readable answer', eventId: 'answer-row', invocationId: 'turn' }],
  ])
  expect(events).toEqual([
    ['bot_run_state', state],
    ['user_message', { text: 'Readable answer', eventId: 'answer-row', invocationId: 'turn' }],
  ])
  expect(JSON.stringify(events)).not.toContain('option-id')
  expect(JSON.stringify(events)).not.toContain('bot_owner_answer')
  expect(mergeBotRunState({}, events[0][1], 'bot', 'home')).toEqual({ r: state })
})

it('restores backend-projected owner answer text without adding a client legacy parser', async () => {
  const canonical: [string, object][] = [['user_message', { text: 'Only local release notes', eventId: 'answer-row', invocationId: 'turn' }]]
  const live = await receive(canonical)
  const restored = await receive(canonical)
  expect(restored).toEqual(live)
  expect(restored).toEqual(canonical)
  expect(JSON.stringify(restored)).not.toContain('bot_owner_answer')
})
