import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotStopRun from '../BotStopRun'
import { cancelBotRun } from '@/api/botRunControls'
import type { Bot, BotRun } from '@/api/bots'
vi.mock('@/api/botRunControls', async original => ({ ...await original<typeof import('@/api/botRunControls')>(), cancelBotRun: vi.fn() }))
const bot = { id: 'bot', name: 'Watcher', scope: 'personal' } as Bot
const run = { id: 'exact-run', bot_id: 'bot', status: 'running' } as BotRun
beforeEach(() => vi.resetAllMocks())
it('requires exact review, disables saving, and does not claim interrupted teardown is success', async () => {
  let resolve!: (run: BotRun) => void
  vi.mocked(cancelBotRun).mockImplementation(() => new Promise(done => { resolve = done }))
  const onChange = vi.fn()
  render(<BotStopRun bot={bot} run={run} onChange={onChange} />)
  fireEvent.click(screen.getByRole('button', { name: 'Stop run' }))
  expect(screen.getByRole('region', { name: 'Review stop run exact-run' })).toHaveTextContent('does not stop or pause the Bot')
  expect(cancelBotRun).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Confirm stop run' }))
  expect(screen.getByRole('button', { name: 'Requesting stop…' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Keep run' })).toBeDisabled()
  await act(async () => resolve({ ...run, status: 'interrupted' }))
  expect(cancelBotRun).toHaveBeenCalledExactlyOnceWith(bot, run.id)
  expect(screen.getByRole('status')).toHaveTextContent('interrupted. Execution teardown may still be pending')
  expect(onChange).toHaveBeenCalledTimes(1)
})
it('preserves the exact review on failure and retries only that run', async () => {
  vi.mocked(cancelBotRun).mockRejectedValueOnce(new Error('Conflict')).mockResolvedValue({ ...run, status: 'interrupted' })
  render(<BotStopRun bot={bot} run={run} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Stop run' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm stop run' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Conflict')
  expect(screen.getByRole('region')).toHaveTextContent('exact-run')
  fireEvent.click(screen.getByRole('button', { name: 'Confirm stop run' }))
  await waitFor(() => expect(cancelBotRun).toHaveBeenCalledTimes(2))
  expect(cancelBotRun).toHaveBeenLastCalledWith(bot, 'exact-run')
})
it.each(['queued', 'running', 'waiting_approval'] as const)('offers eligible %s and respects read-only', status => {
  render(<BotStopRun bot={bot} run={{ ...run, status }} disabled onChange={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Stop run' })).toBeDisabled()
})
it.each(['interrupted', 'succeeded', 'partial', 'failed', 'skipped', 'cancelled'] as const)('does not offer terminal %s', status => {
  render(<BotStopRun bot={bot} run={{ ...run, status }} onChange={vi.fn()} />)
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})
it('does not confirm after the run becomes terminal during review', () => {
  const view = render(<BotStopRun bot={bot} run={run} onChange={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Stop run' }))
  view.rerender(<BotStopRun bot={bot} run={{ ...run, status: 'succeeded' }} onChange={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Confirm stop run' })).toBeDisabled()
  expect(cancelBotRun).not.toHaveBeenCalled()
})
