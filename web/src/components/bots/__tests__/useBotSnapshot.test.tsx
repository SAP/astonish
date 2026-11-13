import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useBotSnapshot } from '../useBotSnapshot'
import * as api from '@/api/botSnapshot'
import type { BotSnapshot } from '@/api/botSnapshot'
vi.mock('@/api/botSnapshot', () => ({ getBotSnapshot: vi.fn(), readBotActivityPage: vi.fn(), writeBotReadCursor: vi.fn() }))
vi.mock('@/api/teamContext', () => ({ getActiveTeam: () => 'team-a' }))
const snapshot = (id = 'a', viewer = 'owner', latest = 121, read = 0): BotSnapshot => ({ bot: { id, scope: 'team', owner_id: 'owner', name: id, status: 'active', version: 1, revision: 1, home_session_id: '', home: { id: '24c09d23-63e1-42d8-8baa-5f540fabc645', bot_id: id, scope: 'team', backend: 'docker', storage_key: 'retained-volume', revision: 1 }, created_at: '', updated_at: '' }, read_cursor: { bot_id: id, viewer_id: viewer, sequence: read }, latest_sequence: latest, unread_count: latest-read, pending_approvals: [] })
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.getBotSnapshot).mockImplementation(async id => snapshot(id))
  vi.mocked(api.readBotActivityPage).mockImplementation(async (_id, _scope, _team, after) => ({ activity: Array.from({ length: Math.min(50, 121 - after) }, (_, i) => ({ sequence: after+i+1, kind: 'report', payload: {}, created_at: '' })) }))
  vi.mocked(api.writeBotReadCursor).mockResolvedValue(undefined)
})
afterEach(() => vi.useRealTimers())
it('catches up applied operation heads after an already in-flight snapshot without inventing a root version', async () => {
  const initial = snapshot('a', 'owner', 0)
  initial.bot.home_session_id = 'home'
  vi.mocked(api.getBotSnapshot).mockResolvedValue(initial)
  const hook = renderHook(() => useBotSnapshot('a', 'team', 'owner'))
  await waitFor(() => expect(hook.result.current.data).toBeDefined())
  let resolve!: (value: BotSnapshot) => void
  vi.mocked(api.getBotSnapshot).mockImplementationOnce(() => new Promise(r => { resolve = r }))
  act(() => hook.result.current.retry())
  const applied = { schema_version: 1 as const, bot_id: 'a', home_session_id: 'home', operation_id: 'cadence', revision: 9, state_sequence: 17, operation: 'schedule_edit', status: 'applied' as const, decision: 'execute' as const, summary: 'Applied' }
  const committed = { ...initial, bot: { ...initial.bot, revision: 6, version: 3 }, operations: [applied] }
  vi.mocked(api.getBotSnapshot).mockResolvedValue(committed)
  let root: unknown
  await act(async () => {
    const synced = hook.result.current.syncOperations([applied])
    resolve(initial)
    root = await synced
  })
  expect(root).toEqual(committed.bot)
  expect(hook.result.current.data?.bot.version).toBe(3)
  expect(api.getBotSnapshot).toHaveBeenCalledTimes(3)
  await act(async () => { await hook.result.current.syncOperations([applied]) })
  expect(api.getBotSnapshot).toHaveBeenCalledTimes(3)
  vi.mocked(api.getBotSnapshot).mockResolvedValue(initial)
  await act(async () => hook.result.current.retry())
  expect(hook.result.current.data?.bot.version).toBe(3)
})
it('fails closed when a committed head cannot be reconciled and ignores foreign or pending heads', async () => {
  const initial = snapshot('a', 'owner', 0)
  initial.bot.home_session_id = 'home'
  vi.mocked(api.getBotSnapshot).mockResolvedValue(initial)
  const hook = renderHook(() => useBotSnapshot('a', 'team', 'owner'))
  await waitFor(() => expect(hook.result.current.data).toBeDefined())
  const applied = { schema_version: 1 as const, bot_id: 'a', home_session_id: 'home', operation_id: 'cadence', revision: 2, state_sequence: 2, operation: 'schedule_edit', status: 'applied' as const, decision: 'execute' as const, summary: 'Applied' }
  await act(async () => {
    await expect(hook.result.current.syncOperations([{ ...applied, bot_id: 'other' }, { ...applied, home_session_id: 'other' }, { ...applied, status: 'pending' }])).resolves.toEqual(initial.bot)
  })
  expect(api.getBotSnapshot).toHaveBeenCalledTimes(1)
  await act(async () => { await expect(hook.result.current.syncOperations([applied])).rejects.toThrow(/refresh/i) })
  expect(hook.result.current.data?.bot.version).toBe(1)
})
it('shares counts, catches >50 events, deduplicates refresh, and reads the persisted cursor', async () => {
  const { result, unmount } = renderHook(() => ({ sidebar: useBotSnapshot('a', 'team', 'owner'), workspace: useBotSnapshot('a', 'team', 'owner', true) }))
  await waitFor(() => expect(result.current.workspace.activity).toHaveLength(121))
  expect(result.current.sidebar.data).toBe(result.current.workspace.data)
  expect(api.readBotActivityPage).toHaveBeenCalledWith('a', 'team', 'team-a', 100, expect.any(AbortSignal))
  act(() => result.current.workspace.retry())
  await waitFor(() => expect(result.current.workspace.loading).toBe(false))
  expect(new Set(result.current.workspace.activity.map(e => e.sequence)).size).toBe(121)
  vi.mocked(api.getBotSnapshot).mockResolvedValue(snapshot('a', 'owner', 121, 121))
  await act(() => result.current.workspace.markRead(121))
  expect(result.current.sidebar.data?.unread_count).toBe(0)
  unmount()
  const reload = renderHook(() => useBotSnapshot('a', 'team', 'owner', true))
  await waitFor(() => expect(reload.result.current.data?.read_cursor.sequence).toBe(121))
  expect(reload.result.current.activity).toHaveLength(121)
})
it('aborts a stale Bot response and resets activity and cursor on viewer switches', async () => {
  let resolve!: (value: BotSnapshot) => void
  let oldSignal: AbortSignal | undefined
  vi.mocked(api.getBotSnapshot).mockImplementationOnce((_id, _scope, _team, signal) => { oldSignal = signal; return new Promise(r => { resolve = r }) })
  const hook = renderHook(({ id, viewer }) => useBotSnapshot(id, 'team', viewer), { initialProps: { id: 'a', viewer: 'owner' } })
  vi.mocked(api.getBotSnapshot).mockResolvedValue(snapshot('b', 'other', 4, 2))
  hook.rerender({ id: 'b', viewer: 'other' })
  expect(hook.result.current.data).toBeUndefined()
  expect(oldSignal?.aborted).toBe(true)
  await waitFor(() => expect(hook.result.current.data?.unread_count).toBe(2))
  await act(async () => resolve(snapshot()))
  expect(hook.result.current.data?.bot.id).toBe('b')
  expect(hook.result.current.activity).toEqual([])
})
it('keeps run work ordered across newer snapshots and reload', async () => {
  const initial = snapshot('a', 'owner', 0)
  initial.bot.home_session_id = 'home'
  const state: NonNullable<BotSnapshot['run_states']>[number] = { schema_version: 1, bot_id: 'a', run_id: 'run', session_id: 'trace', home_session_id: 'home', presentation: 'home_interactive', status: 'succeeded', state_sequence: 7, work: { schema_version: 1, protocol: 'agent_led_v1', bot_id: 'a', run_id: 'run', work_revision: 3, phase: 'finished', disposition: 'answered' } }
  initial.run_states = [state]
  vi.mocked(api.getBotSnapshot).mockResolvedValue(initial)
  const hook = renderHook(() => useBotSnapshot('a', 'team', 'owner'))
  await waitFor(() => expect(hook.result.current.data?.run_states).toEqual([state]))
  vi.mocked(api.getBotSnapshot).mockResolvedValue({ ...initial, latest_sequence: 1, run_states: [{ ...state, state_sequence: 6, status: 'running' }, { ...state, run_id: 'foreign', bot_id: 'other' }] })
  await act(async () => hook.result.current.retry())
  expect(hook.result.current.data?.run_states).toEqual([state])
  vi.mocked(api.getBotSnapshot).mockResolvedValue({ ...initial, latest_sequence: 2, run_states: [{ ...state, state_sequence: 8, work: { ...state.work!, work_revision: 2 } }] })
  await act(async () => hook.result.current.retry())
  expect(hook.result.current.data?.run_states).toEqual([state])
  hook.unmount()
  vi.mocked(api.getBotSnapshot).mockResolvedValue(initial)
  const restored = renderHook(() => useBotSnapshot('a', 'team', 'owner'))
  await waitFor(() => expect(restored.result.current.data?.run_states).toEqual([state]))
})
it('refreshes in the background without a transcript event', async () => {
  vi.useFakeTimers()
  const hook = renderHook(() => useBotSnapshot('a', 'team', 'owner'))
  await act(async () => { await Promise.resolve() })
  vi.mocked(api.getBotSnapshot).mockResolvedValue(snapshot('a', 'owner', 122))
  await act(async () => { vi.advanceTimersByTime(15000); await Promise.resolve() })
  expect(hook.result.current.data?.unread_count).toBe(122)
})
