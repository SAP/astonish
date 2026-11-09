import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotHomeSetup, { hasBotHome } from '../BotHomeSetup'
import * as api from '@/api/bots'
vi.mock('@/api/bots', async original => ({ ...await original<typeof api>(), setupBotHome: vi.fn() }))
const bot = { id: 'bot', scope: 'personal', status: 'draft', revision: 1, home_session_id: '82c09d23-63e1-42d8-8baa-5f540fabc645', home: null } as api.Bot
const home: api.BotHomeBinding = { id: '24c09d23-63e1-42d8-8baa-5f540fabc645', bot_id: bot.id, scope: 'personal', backend: 'docker', storage_key: 'retained-volume', revision: 1 }
beforeEach(() => vi.clearAllMocks())
it('prepares only on explicit click and returns the new revision without running', async () => {
  const saved = { ...bot, revision: 2, home }, onPrepared = vi.fn()
  let finish!: (bot: api.Bot) => void
  vi.mocked(api.setupBotHome).mockReturnValue(new Promise(resolve => { finish = resolve }))
  render(<BotHomeSetup bot={bot} disabled={false} onPrepared={onPrepared} onRefresh={vi.fn()} />)
  expect(api.setupBotHome).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Prepare retained home' }))
  expect(screen.getByRole('button', { name: 'Preparing retained home…' })).toBeDisabled()
  finish(saved)
  await waitFor(() => expect(onPrepared).toHaveBeenCalledWith(saved))
})
it('shows precise unavailable errors and permits an explicit retry', async () => {
  vi.mocked(api.setupBotHome).mockRejectedValue(new api.BotAPIError(503, 'Bot operation unavailable'))
  render(<BotHomeSetup bot={bot} disabled={false} onPrepared={vi.fn()} onRefresh={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Prepare retained home' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Retained-home preparation is unavailable on this server (503)')
  fireEvent.click(screen.getByRole('button', { name: 'Prepare retained home' }))
  await waitFor(() => expect(api.setupBotHome).toHaveBeenCalledTimes(2))
})
it('requires a refreshed revision after conflict, never silently resubmits', async () => {
  vi.mocked(api.setupBotHome).mockRejectedValue(new api.BotAPIError(409, 'conflict'))
  const props = { disabled: false, onPrepared: vi.fn(), onRefresh: vi.fn() }
  const view = render(<BotHomeSetup bot={bot} {...props} />)
  fireEvent.click(screen.getByRole('button', { name: 'Prepare retained home' }))
  await screen.findByRole('alert')
  expect(screen.getByRole('button', { name: 'Prepare retained home' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Refresh Bot before retrying' }))
  expect(props.onRefresh).toHaveBeenCalledOnce()
  view.rerender(<BotHomeSetup bot={{ ...bot, revision: 2 }} {...props} />)
  expect(screen.getByRole('button', { name: 'Prepare retained home' })).toBeEnabled()
  expect(api.setupBotHome).toHaveBeenCalledOnce()
})
it.each([{ status: 'active', home: null }, { status: 'paused', home }, { status: 'draft', home }] as const)('hides preparation for $status with its binding', overrides => {
  render(<BotHomeSetup bot={{ ...bot, ...overrides }} disabled={false} onPrepared={vi.fn()} onRefresh={vi.fn()} />)
  expect(screen.queryByRole('button', { name: 'Prepare retained home' })).not.toBeInTheDocument()
})
it.each([null, undefined, { ...home, id: '' }, { ...home, id: '00000000-0000-0000-0000-000000000000' }])('does not confuse an empty binding with a prepared home: %j', binding => {
  const draft = { ...bot, home: binding } as api.Bot
  expect(hasBotHome(draft)).toBe(false)
  render(<BotHomeSetup bot={draft} disabled={false} onPrepared={vi.fn()} onRefresh={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Prepare retained home' })).toBeEnabled()
})
it('recognizes the physical binding independently of the conversation ID', () => {
  expect(hasBotHome({ ...bot, home, home_session_id: '' })).toBe(true)
})
it('does not grant preparation to read-only viewers', () => {
  render(<BotHomeSetup bot={bot} disabled onPrepared={vi.fn()} onRefresh={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Prepare retained home' })).toBeDisabled()
})
