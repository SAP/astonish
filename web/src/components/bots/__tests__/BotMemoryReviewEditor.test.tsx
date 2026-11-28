import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import BotMemoryReviewEditor from '../BotMemoryReviewEditor'
import { applyBotMemoryReview, getBotDocuments, getBotMemoryHealth } from '@/api/botMemory'
import type { Bot } from '@/api/bots'
vi.mock('@/api/botMemory', () => ({ applyBotMemoryReview: vi.fn(), getBotDocuments: vi.fn(), getBotMemoryHealth: vi.fn() }))
const bot = { id: 'bot', scope: 'personal', revision: 2 } as Bot
const sources = [{ id: 'a', session_id: 's', expected_fingerprint: 'hash-a' }, { id: 'b', session_id: 's', expected_fingerprint: 'hash-b' }]
beforeEach(() => vi.clearAllMocks())
it('requires explicit reviewed mapping and retains source fingerprints', async () => {
  vi.mocked(applyBotMemoryReview).mockResolvedValue({ id: 'receipt', bot_id: 'bot', revision: 3, actor_id: 'owner', request_fingerprint: 'hash', replacement_ids: ['replacement'], sources: [], reason: 'Verified' })
  render(<BotMemoryReviewEditor bot={bot} readOnly={false} sources={sources} kind="consolidate" />)
  fireEvent.change(screen.getByLabelText('Reviewed memory content'), { target: { value: 'Merged evidence' } })
  fireEvent.change(screen.getByLabelText('Memory review reason'), { target: { value: 'Verified' } })
  fireEvent.click(screen.getByRole('button', { name: 'Review mapped changes' }))
  expect(applyBotMemoryReview).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Confirm mapped changes' }))
  expect(await screen.findByText(/Saved reviewed records/)).toBeInTheDocument()
  expect(applyBotMemoryReview).toHaveBeenCalledWith(bot, expect.objectContaining({ kind: 'consolidate', confirmed: true, expected_revision: 2, edits: [{ session_id: 's', sources, content: 'Merged evidence', category: 'note' }] }))
})
it('retains mapped draft when source comparison fails', async () => {
  const { BotAPIError } = await import('@/api/bots')
  vi.mocked(applyBotMemoryReview).mockRejectedValue(new BotAPIError(409, 'Stale source'))
  vi.mocked(getBotDocuments).mockResolvedValue({ bot_id: 'bot', revision: 4, version: 1, documents: [] })
  vi.mocked(getBotMemoryHealth).mockResolvedValue({ bot_id: 'bot', scope: 'personal', review_only: true, apply_supported: true, health: { evaluated_at: 'today', recommendation_count: 0, recommendations: [] }, snapshot: { records: [{ id: 'a', session_id: 's', snippet: 'Changed', path: 'source' }], fingerprints: { a: 'new' }, fingerprint: 'hash', truncated: false, evaluated_at: 'today' } })
  render(<BotMemoryReviewEditor bot={bot} readOnly={false} sources={sources} kind="consolidate" />)
  fireEvent.change(screen.getByLabelText('Reviewed memory content'), { target: { value: 'Local draft' } })
  fireEvent.change(screen.getByLabelText('Memory review reason'), { target: { value: 'Verified' } })
  fireEvent.click(screen.getByRole('button', { name: 'Review mapped changes' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm mapped changes' }))
  expect(await screen.findByText('Stale source')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Reload mapped sources for comparison' }))
  expect(await screen.findByText(/Some sources are unavailable/)).toBeInTheDocument()
  expect(screen.getByLabelText('Reviewed memory content')).toHaveValue('Local draft')
  expect(screen.getByRole('button', { name: 'Use compared source fingerprints' })).toBeDisabled()
})
