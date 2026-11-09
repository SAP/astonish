import { describe, it, expect, vi } from 'vitest'
import { requestBotRun, type Bot } from '../bots'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
describe('Bot attachments', () => {
  it('passes shared Chat attachment payloads to scoped admission', async () => {
    vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ id: 'run' }), { status: 202 }))
    const attachments = [{ filename: 'note.txt', mimeType: 'text/plain', data: 'aGVsbG8=' }]
    await requestBotRun({ id: 'bot', scope: 'team', version: 3 } as Bot, '', 'key', attachments)
    const [url, init] = vi.mocked(teamFetch).mock.calls.at(-1)!
    expect(url).toBe('/api/studio/bots/bot/runs?scope=team')
    expect(JSON.parse(init!.body as string)).toEqual({ version: 3, instructions: '', idempotency_key: 'key', attachments })
  })
})
