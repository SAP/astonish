import { beforeEach, expect, it, vi } from 'vitest'
import { continueInteractive } from '../botReviews'
import { teamFetch } from '../teamContext'
import type { Bot } from '../bots'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.clearAllMocks())
it('continues only the exact saved run and response on explicit invocation, preserving errors', async () => {
  const bot = { id: 'bot/id', revision: 7, scope: 'personal' } as Bot
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('Authorization expired', { status: 403 }))
  await expect(continueInteractive(bot, 'run/id', 'stable', { question_id: 'q', answer: 'custom text' })).rejects.toMatchObject({ status: 403, message: 'Authorization expired' })
  expect(teamFetch).toHaveBeenCalledTimes(1)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot%2Fid/runs/run%2Fid/continue-interactive?scope=personal', expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_revision: 7, idempotency_key: 'stable', response: { question_id: 'q', answer: 'custom text' } }) }))
})
