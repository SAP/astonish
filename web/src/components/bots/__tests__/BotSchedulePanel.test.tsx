import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BotSchedulePanel from '../BotSchedulePanel'
import * as api from '@/api/bots'
import * as triggers from '@/api/botTriggers'
vi.mock('@/api/bots', async original => ({ ...await original<typeof api>(), getBotVersion: vi.fn(), listBotGoals: vi.fn() }))
vi.mock('@/api/botTriggers', async original => ({ ...await original<typeof triggers>(), saveTrigger: vi.fn(), actOnTrigger: vi.fn() }))
const bot = { id: 'bot', scope: 'personal', revision: 3, version: 2, status: 'active' } as api.Bot
const hook: triggers.TriggerSpec = { id: 'hook', kind: 'webhook', instructions: 'Observe', enabled: false, catch_up_once: false, credential_ref: 'ref', hook_status: 'disabled' }
const version = { bot_id: 'bot', version: 2, persona: { timezone: 'UTC' }, triggers: [hook] } as api.BotVersion
const change = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api.listBotGoals).mockResolvedValue({ goals: [] }) })
it('does not enable a hook before proof; refreshes exact server version after dry-run', async () => {
  vi.mocked(triggers.actOnTrigger).mockResolvedValue({ ...bot, revision: 4, version: 3 })
  vi.mocked(api.getBotVersion).mockResolvedValue({ ...version, version: 3, triggers: [{ ...hook, hook_status: 'dry_run_passed' }] })
  render(<BotSchedulePanel bot={bot} version={version} readOnly={false} onChange={vi.fn()} />)
  change('Trigger change reason', 'Reviewed')
  expect(screen.getByRole('button', { name: 'Enable reviewed trigger' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Review dry-run' }))
  expect(screen.getByRole('button', { name: 'Confirm dry-run' })).toBeDisabled()
  change('Proof event ID', 'event'); change('Proof timestamp (Unix seconds)', '1730000000'); change('Proof signature', 'signed'); change('Proof payload (base64, exact signed bytes)', 'e30=')
  fireEvent.click(screen.getByRole('button', { name: 'Confirm dry-run' }))
  await waitFor(() => expect(triggers.actOnTrigger).toHaveBeenCalledWith(bot, hook, 'dry-run', 'Reviewed', { bot_id: 'bot', trigger_id: 'hook', version: 2, event_id: 'event', timestamp: 1730000000, signature: 'signed', payload: 'e30=' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Enable reviewed trigger' })).toBeEnabled())
  expect(api.getBotVersion).toHaveBeenCalledWith('bot', 3, 'personal')
})
it('requires a new review after edits and preserves invalid input on server rejection', async () => {
  vi.mocked(triggers.saveTrigger).mockRejectedValue(new Error('Invalid credential reference'))
  render(<BotSchedulePanel bot={bot} version={version} readOnly={false} onChange={vi.fn()} />)
  change('Trigger change reason', 'Changed')
  fireEvent.click(screen.getByRole('button', { name: 'Edit trigger' }))
  const review = screen.getByLabelText(/I reviewed these changes/)
  fireEvent.click(review)
  change('Credential reference', 'invalid-ref')
  expect(review).not.toBeChecked()
  expect(screen.getByRole('button', { name: 'Save reviewed changes disabled' })).toBeDisabled()
  fireEvent.click(review); fireEvent.click(screen.getByRole('button', { name: 'Save reviewed changes disabled' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Invalid credential reference')
  expect(screen.getByLabelText('Credential reference')).toHaveValue('invalid-ref')
})
it('reviews removal separately and respects read-only mode', async () => {
  const { rerender } = render(<BotSchedulePanel bot={bot} version={version} readOnly={false} onChange={vi.fn()} />)
  change('Trigger change reason', 'Retired'); fireEvent.click(screen.getByRole('button', { name: 'Remove trigger' }))
  expect(triggers.actOnTrigger).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Confirm remove' })).toBeEnabled()
  rerender(<BotSchedulePanel bot={bot} version={version} readOnly onChange={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Confirm remove' })).toBeDisabled()
  expect(screen.getByLabelText('Schedule instructions')).toBeDisabled()
})
it('saves catch-up edits disabled and blocks further changes if the server refresh fails', async () => {
  const cron: triggers.TriggerSpec = { id: 'cron', kind: 'cron', cron: '0 9 * * *', timezone: 'UTC', instructions: 'Observe', enabled: true, catch_up_once: false }
  vi.mocked(triggers.saveTrigger).mockResolvedValue({ ...bot, revision: 4, version: 3 })
  vi.mocked(api.getBotVersion).mockRejectedValue(new Error('Network unavailable'))
  render(<BotSchedulePanel bot={bot} version={{ ...version, triggers: [cron] }} readOnly={false} onChange={vi.fn()} />)
  change('Trigger change reason', 'Catch up'); fireEvent.click(screen.getByRole('button', { name: 'Edit trigger' }))
  fireEvent.click(screen.getByLabelText('Catch up once after a missed occurrence'))
  fireEvent.click(screen.getByLabelText(/I reviewed these changes/))
  fireEvent.click(screen.getByRole('button', { name: 'Save reviewed changes disabled' }))
  await waitFor(() => expect(triggers.saveTrigger).toHaveBeenCalledWith(bot, expect.objectContaining({ id: 'cron', catch_up_once: true }), 'Catch up'))
  expect(await screen.findByText(/Saved state could not be refreshed/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Save reviewed changes disabled' })).toBeDisabled()
  expect(screen.getByLabelText('Cron expression')).toHaveValue('0 9 * * *')
})
it('offers only active goals with persisted deadlines', async () => {
  vi.mocked(api.listBotGoals).mockResolvedValue({ goals: [
    { id: 'due', title: 'Due goal', status: 'active', due_at: '2030-01-01T12:00:00Z' },
    { id: 'no-due', title: 'No deadline', status: 'active' },
  ] as api.BotGoal[] })
  render(<BotSchedulePanel bot={bot} version={version} readOnly={false} onChange={vi.fn()} />)
  change('Trigger type', 'goal_deadline')
  expect(await screen.findByRole('option', { name: /Due goal/ })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: 'No deadline' })).not.toBeInTheDocument()
  expect(screen.getByText(/Next run: unavailable/)).toBeInTheDocument()
})
