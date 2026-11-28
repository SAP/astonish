import { beforeEach, expect, it, vi } from 'vitest'
import { getBotSnapshot, getBotReadCursor, readBotActivityPage, writeBotReadCursor } from '../botSnapshot'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.clearAllMocks())
it('pins team, encodes Bot identity, pages after sequence, and never sends a viewer id', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('{"activity":[]}'))
  const controller = new AbortController()
  await readBotActivityPage('bot/id', 'team', 'team-a', 100, controller.signal)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot%2Fid/activity?after=100&limit=50&scope=team', expect.objectContaining({ signal: controller.signal, method: 'GET' }), 'team-a')
  vi.mocked(teamFetch).mockResolvedValue(new Response(null, { status: 204 }))
  await writeBotReadCursor('bot', 'personal', 'team-a', 121)
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot/read?scope=personal', expect.objectContaining({ body: '{"sequence":121}', method: 'POST' }), 'team-a')
})
it('gets snapshots and viewer cursors and propagates authorization errors', async () => {
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('{"latest_sequence":121}'))
  expect(await getBotSnapshot('b', 'team', null)).toEqual({ latest_sequence: 121, operations: [], mandate: null })
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('{"sequence":100}'))
  expect(await getBotReadCursor('b', 'team', null)).toEqual({ sequence: 100 })
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('Forbidden', { status: 403 }))
  await expect(getBotSnapshot('b', 'team', null)).rejects.toMatchObject({ status: 403 })
})
