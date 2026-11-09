import { expect, it, vi } from 'vitest'
import { decideBotOperation } from '../botReviews'
import type { BotOperationView } from '../botOperationResult'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
it('posts only consent coordinates with operation identity in the route, never a mandate', async () => {
  const operation = { operation_id: 'op', revision: 4, exception: { payload_digest: 'digest', expires_at: '2099-01-01T00:00:00Z' }, mandate_ref: 'must-not-leak' } as unknown as BotOperationView
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ status: 'pending' })))
  await decideBotOperation({ id: 'bot', scope: 'personal' }, operation, true)
  const [url, init] = vi.mocked(teamFetch).mock.calls[0]
  expect(String(url)).toContain('/bots/bot/operations/op/decision')
  expect(JSON.parse(String(init?.body))).toEqual({ payload_digest: 'digest', expected_revision: 4, expires_at: '2099-01-01T00:00:00Z', approve: true })
})
