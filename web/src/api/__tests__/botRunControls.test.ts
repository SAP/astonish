import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BotAPIError, type BotRun } from '../bots'
import { cancelBotRun, canStopBotRun } from '../botRunControls'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.resetAllMocks())
describe('exact-run controls API', () => {
  it.each(['personal', 'team'] as const)('posts only the encoded target and %s scope, with no body', async scope => {
    const run = { id: 'run/one', status: 'interrupted' }
    vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify(run), { status: 202 }))
    expect(await cancelBotRun({ id: 'bot/one', scope }, run.id)).toEqual(run)
    expect(teamFetch).toHaveBeenCalledWith(`/api/studio/bots/bot%2Fone/runs/run%2Fone/cancel?scope=${scope}`, { method: 'POST' })
  })
  it.each([403, 409, 503])('preserves %s errors for review and retry', async status => {
    vi.mocked(teamFetch).mockResolvedValue(new Response('Unavailable', { status }))
    await expect(cancelBotRun({ id: 'bot', scope: 'team' }, 'run')).rejects.toEqual(new BotAPIError(status, 'Unavailable'))
  })
  it.each(['queued', 'running', 'waiting_approval', 'interrupted', 'succeeded', 'partial', 'failed', 'skipped', 'cancelled'] as BotRun['status'][])('gates status %s', status => {
    expect(canStopBotRun({ status })).toBe(['queued', 'running', 'waiting_approval'].includes(status))
  })
})
