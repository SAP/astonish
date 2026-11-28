import { render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import SchedulerSettings from '../SchedulerSettings'
import { teamFetch } from '@/api/teamContext'
vi.mock('@/api/teamContext', () => ({ teamFetch: vi.fn() }))
it('makes Bot-managed jobs navigation-only while preserving ordinary job controls', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ jobs: [
    { id: 'managed', name: 'Bot morning', scope: 'personal', mode: 'bot', bot_managed: true, payload: { bot_id: 'bot-id' } },
    { id: 'ordinary', name: 'Ordinary', scope: 'personal', mode: 'adaptive' },
  ] })))
  render(<SchedulerSettings config={{ enabled: true }} />)
  const card = await screen.findByRole('article', { name: 'Bot-managed schedule' })
  expect(within(card).getByRole('link', { name: 'Open Bot' })).toHaveAttribute('href', '#/chat/bots/personal/bot-id')
  expect(within(card).queryByRole('button')).not.toBeInTheDocument()
  expect(screen.getByTitle('Run now')).toBeInTheDocument()
})
