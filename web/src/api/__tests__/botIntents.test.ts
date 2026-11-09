import { beforeEach, describe, expect, it, vi } from 'vitest'
import { requestBotConversation, requestBotTestRun, requestBotRun, type Bot } from '../bots'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const bot = { id: 'a/b', scope: 'team', version: 7 } as Bot
beforeEach(() => vi.clearAllMocks())
describe('explicit Bot execution intents', () => {
  it.each([
    ['test-run', requestBotTestRun],
    ['conversation', requestBotConversation],
    ['runs', requestBotRun],
  ] as const)('posts %s without lifecycle mutation and preserves retry identity', async (endpoint, send) => {
    vi.mocked(teamFetch).mockImplementation(async () => new Response(JSON.stringify({ id: 'run', session_id: 'thread' }), { status: 202 }))
    expect(await send(bot, 'Reviewed text', 'stable-key')).toEqual({ id: 'run', session_id: 'thread' })
    await send(bot, 'Reviewed text', 'stable-key')
    expect(teamFetch).toHaveBeenCalledTimes(2)
    for (const [url, init] of vi.mocked(teamFetch).mock.calls) {
      expect(url).toBe(`/api/studio/bots/a%2Fb/${endpoint}?scope=team`)
      expect(init).toMatchObject({ method: 'POST', headers: { 'Content-Type': 'application/json' } })
      expect(JSON.parse(init!.body as string)).toEqual({ version: 7, instructions: 'Reviewed text', idempotency_key: 'stable-key' })
    }
  })
  it('forwards optional conversation attachments and surfaces backend rejection without dropping them', async () => {
    const attachments = [{ filename: 'note.txt', mimeType: 'text/plain', data: 'aGk=' }]
    vi.mocked(teamFetch).mockResolvedValue(new Response('Conversation attachments unsupported', { status: 400 }))
    await expect(requestBotConversation(bot, 'Hello', 'key', attachments)).rejects.toMatchObject({ status: 400 })
    expect(JSON.parse(vi.mocked(teamFetch).mock.calls[0][1]!.body as string)).toEqual({ version: 7, instructions: 'Hello', idempotency_key: 'key', attachments })
  })
  it.each([requestBotTestRun, requestBotConversation])('surfaces forbidden and unknown admission errors', async send => {
    vi.mocked(teamFetch).mockResolvedValueOnce(new Response('Owner only', { status: 403 }))
    await expect(send(bot, 'text', 'key')).rejects.toMatchObject({ status: 403, message: 'Owner only' })
    vi.mocked(teamFetch).mockRejectedValueOnce(new Error('Outcome unknown'))
    await expect(send(bot, 'text', 'key')).rejects.toThrow('Outcome unknown')
  })
})
