import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BotContinueInteractive from '../BotContinueInteractive'
import { continueInteractive } from '@/api/botReviews'
import type { Bot } from '@/api/bots'
vi.mock('@/api/botReviews', () => ({ continueInteractive: vi.fn() }))
const bot = { id: 'b', revision: 8, scope: 'personal' } as Bot
beforeEach(() => vi.clearAllMocks())
it('requires an explicit click, retries exact run with a stable key and current snapshot revision, never resends', async () => {
  vi.mocked(continueInteractive).mockRejectedValueOnce(new Error('Expired')).mockResolvedValueOnce({} as never)
  const changed = vi.fn()
  const view = render(<BotContinueInteractive bot={bot} runId="run" disabled={false} onChange={changed} />)
  expect(continueInteractive).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Continue interactive' }))
  await screen.findByRole('alert')
  view.rerender(<BotContinueInteractive bot={{ ...bot, revision: 9 }} runId="run" disabled={false} onChange={changed} />)
  fireEvent.click(screen.getByRole('button', { name: 'Continue interactive' }))
  await waitFor(() => expect(changed).toHaveBeenCalledOnce())
  const calls = vi.mocked(continueInteractive).mock.calls
  expect(calls[0][1]).toBe('run'); expect(calls[0][2]).toBe(calls[1][2]); expect(calls[1][0].revision).toBe(9)
  expect(screen.getByRole('button', { name: 'Continuation requested' })).toBeDisabled()
})
it('closed, read-only or failed snapshot disables authorization', () => {
  render(<BotContinueInteractive bot={bot} runId="run" disabled onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button'))
  expect(continueInteractive).not.toHaveBeenCalled()
})

it('does not refresh a replacement Home after a late continuation receipt', async () => {
  let resolve!: (value: never) => void
  vi.mocked(continueInteractive).mockReturnValue(new Promise(r => { resolve = r }))
  const changed = vi.fn()
  const view = render(<BotContinueInteractive bot={bot} runId="old" disabled={false} onChange={changed} />)
  fireEvent.click(screen.getByRole('button'))
  view.unmount()
  resolve({} as never)
  await Promise.resolve()
  expect(changed).not.toHaveBeenCalled()
})
