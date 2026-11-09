import { beforeEach, expect, it, vi } from 'vitest'
import { reviewAdaptiveJob, convertAdaptiveJob } from '../botAdaptive'
import { BotAPIError } from '../bots'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn(), getActiveTeam: () => 'selected-team' }))
beforeEach(() => vi.clearAllMocks())
it('reviews the authorized source without mutation and sends the exact CAS token on conversion', async () => {
  const review = { source_job_id: 'source/id', updated_at: '2026-11-05T22:00:00.123456789Z', schedule: { cron: '0 9 * * *', timezone: 'UTC' }, instructions: 'Observe' }
  vi.mocked(teamFetch).mockResolvedValueOnce(Response.json(review)).mockResolvedValueOnce(Response.json({ id: 'new', status: 'draft' }))
  expect(await reviewAdaptiveJob('source/id', 'team')).toEqual(review)
  expect(teamFetch).toHaveBeenNthCalledWith(1, '/api/studio/bots/convert-adaptive/source%2Fid?scope=team', expect.objectContaining({ method: 'GET' }), 'selected-team')
  const body = { name: 'Watcher', charter: 'Observe', expected_updated_at: review.updated_at, idempotency_key: 'stable', reviewed: true, reviewed_for_team: true }
  await convertAdaptiveJob('source/id', 'team', body)
  expect(teamFetch).toHaveBeenNthCalledWith(2, '/api/studio/bots/convert-adaptive/source%2Fid?scope=team', expect.objectContaining({ method: 'POST', body: JSON.stringify(body) }), 'selected-team')
  expect(teamFetch).toHaveBeenCalledTimes(2)
})
it('preserves conflict status for renewed review', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('Source changed', { status: 409 }))
  await expect(reviewAdaptiveJob('id', 'personal')).rejects.toEqual(new BotAPIError(409, 'Source changed'))
})
