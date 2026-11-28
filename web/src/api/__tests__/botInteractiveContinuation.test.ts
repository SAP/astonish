import { beforeEach, expect, it, vi } from 'vitest'
import { continueInteractive, sendBotMessage } from '../botReviews'
import { teamFetch } from '../teamContext'
import type { Bot } from '../bots'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.clearAllMocks())
it('preserves explicit planning false and originating consent identity', async () => {
  const bot = { id: 'bot', version: 2, revision: 7, scope: 'personal' } as Bot
  vi.mocked(teamFetch).mockResolvedValue(new Response('{}', { status: 202 }))
  await sendBotMessage(bot, 'Plan', 'message', undefined, { planMode: false })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot/messages?scope=personal', expect.objectContaining({ body: JSON.stringify({ version: 2, instructions: 'Plan', idempotency_key: 'message', planMode: false }) }))
  const planExecution = { run_id: 'run', session_id: 'session', plan_id: 'plan', revision: 3, sequence: 9 }
  vi.mocked(teamFetch).mockResolvedValue(new Response('Unsupported Bot operation', { status: 503 }))
  await expect(continueInteractive(bot, 'run', 'consent', undefined, { approvedPlanExecution: true, planExecution })).rejects.toMatchObject({ status: 503 })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot/runs/run/continue-interactive?scope=personal', expect.objectContaining({ body: JSON.stringify({ expected_revision: 7, idempotency_key: 'consent', approvedPlanExecution: true, planExecution }) }))
})
it('continues only the exact saved run and response on explicit invocation, preserving errors', async () => {
  const bot = { id: 'bot/id', revision: 7, scope: 'personal' } as Bot
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('Authorization expired', { status: 403 }))
  await expect(continueInteractive(bot, 'run/id', 'stable', { question_id: 'q', answer: 'custom text' })).rejects.toMatchObject({ status: 403, message: 'Authorization expired' })
  expect(teamFetch).toHaveBeenCalledTimes(1)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot%2Fid/runs/run%2Fid/continue-interactive?scope=personal', expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_revision: 7, idempotency_key: 'stable', response: { question_id: 'q', answer: 'custom text' } }) }))
})
it.each(['personal', 'team'] as const)('sends checkpoint-bound progress without a fabricated answer (%s)', async scope => {
  const bot = { id: 'bot/id', revision: 7, scope } as Bot
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ id: 'run/id', status: 'queued' }), { status: 202 }))
  await expect(continueInteractive(bot, 'run/id', 'stable', { kind: 'progress_v1', checkpoint_revision: 4 })).resolves.toMatchObject({ id: 'run/id', status: 'queued' })
  expect(teamFetch).toHaveBeenCalledWith(`/api/studio/bots/bot%2Fid/runs/run%2Fid/continue-interactive?scope=${scope}`, expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_revision: 7, idempotency_key: 'stable', kind: 'progress_v1', checkpoint_revision: 4 }) }))
})
