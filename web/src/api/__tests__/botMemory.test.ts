import { beforeEach, expect, it, vi } from 'vitest'
import { teamFetch } from '../teamContext'
import { applyBotMemoryReview, getBotDocuments, getBotMemoryHealth, reconcileBotCorrection, saveBotDocument, submitBotCorrection } from '../botMemory'
import { BotAPIError, type Bot } from '../bots'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const bot = { id: 'a/b', scope: 'team', revision: 7 } as Bot
beforeEach(() => { vi.clearAllMocks(); vi.mocked(teamFetch).mockImplementation(async () => new Response('{}')) })
it('uses team transport, encoded Bot ID, scope, and cancellation for reads', async () => {
  const controller = new AbortController()
  await getBotDocuments(bot, controller.signal)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/a%2Fb/documents?scope=team', expect.objectContaining({ method: 'GET', signal: controller.signal }))
  await getBotMemoryHealth(bot)
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/a%2Fb/memory-health?scope=team', expect.objectContaining({ method: 'GET' }))
})
it('sends only the explicit document edit CAS and confirmation', async () => {
  const edit = { expected_revision: 7, content: 'Reviewed soul', reason: 'Verified', confirmed: true }
  await saveBotDocument(bot, 'SOUL.md', edit)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/a%2Fb/documents/SOUL.md?scope=team', expect.objectContaining({ method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(edit) }))
})
it('keeps 202 intent distinct from bodyless reconciliation', async () => {
  const correction = { id: 'intent', expected_revision: 7, superseded_source: 'memory:source', replacement: 'Friday', goal_ids: [], evidence: { summary: 'Verified', source: 'calendar', observed_at: 'today' } }
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(JSON.stringify({ id: 'intent', intent: 'reconcile' }), { status: 202 }))
  expect(await submitBotCorrection(bot, correction)).toEqual({ id: 'intent', intent: 'reconcile' })
  await reconcileBotCorrection(bot, 'intent/a')
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/a%2Fb/corrections/intent%2Fa/reconcile?scope=team', { method: 'POST', signal: undefined })
})
it.each([409, 403, 503])('preserves HTTP %s errors for conflict/access/unavailable UI', async status => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('Server detail', { status }))
  await expect(getBotDocuments(bot)).rejects.toEqual(new BotAPIError(status, 'Server detail'))
})
it('uses mapped ingest and consolidation endpoints with immutable review identity', async () => {
  const review = { id: 'review', expected_revision: 7, kind: 'document' as const, document: 'people/INDEX.md', reason: 'Verified', confirmed: true, edits: [] }
  await applyBotMemoryReview(bot, review)
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/a%2Fb/documents/people/INDEX.md/ingest?scope=team', expect.objectContaining({ method: 'POST', body: JSON.stringify(review) }))
  await applyBotMemoryReview(bot, { ...review, kind: 'consolidate', document: undefined })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/a%2Fb/memory-health/consolidate?scope=team', expect.objectContaining({ method: 'POST' }))
})
