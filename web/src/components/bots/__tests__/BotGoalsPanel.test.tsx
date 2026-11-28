import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { BotGoals } from '../BotPanels'
import * as api from '@/api/botGoals'
import * as bots from '@/api/bots'
vi.mock('@/api/botGoals', async original => ({ ...await original<typeof api>(), createGoal: vi.fn(), editGoal: vi.fn(), confirmGoal: vi.fn(), listTrackedItems: vi.fn(), saveTrackedItem: vi.fn() }))
vi.mock('@/api/bots', async original => ({ ...await original<typeof bots>(), listBotGoals: vi.fn() }))
const bot = { id: 'b', scope: 'personal', status: 'active' } as bots.Bot
const goal: bots.BotGoal = { id: 'g', revision: 3, title: 'Launch', description: 'Ship', completion_criteria: 'Verified release', due_at: '2030-01-01T10:00:00Z', status: 'active', evidence: [] }
const change = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
const click = (name: string) => fireEvent.click(screen.getByRole('button', { name }))
const setup = async (readOnly = false) => { render(<BotGoals bot={bot} goals={[goal]} readOnly={readOnly} onChange={vi.fn()} />); click('Review goal: Launch'); await waitFor(() => expect(screen.queryByText('Loading commitments…')).not.toBeInTheDocument()) }
const evidence = () => { change('Completion summary', 'I verified'); change('Evidence source', 'Release report'); change('Evidence observed time (local)', '2020-01-01T10:00'); fireEvent.click(screen.getByLabelText(/I reviewed the saved/)) }
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api.listTrackedItems).mockResolvedValue({ items: [] }); vi.mocked(bots.listBotGoals).mockResolvedValue({ goals: [{ ...goal, revision: 4 }] }) })
it('creates a goal with a local deadline and clears the draft only after success', async () => {
  render(<BotGoals bot={bot} goals={[]} readOnly={false} onChange={vi.fn()} />)
  change('Goal title', 'New outcome'); change('Completion criteria', 'Measured'); change('Goal due time (local, optional)', '2030-01-01T10:00')
  click('Save goal')
  await waitFor(() => expect(api.createGoal).toHaveBeenCalledWith(bot, { title: 'New outcome', description: '', completion_criteria: 'Measured', due_at: new Date('2030-01-01T10:00').toISOString() }))
  await waitFor(() => expect(screen.getByLabelText('Goal title')).toHaveValue(''))
})
it('keeps completed goal evidence visible without offering mutation controls', async () => {
  render(<BotGoals bot={bot} goals={[{ ...goal, status: 'completed', evidence: [{ kind: 'owner_confirmation', summary: 'Release checked', source: 'release.md', actor_id: 'owner', observed_at: '2020-01-01T10:00:00Z' }] }]} readOnly={false} onChange={vi.fn()} />)
  click('Review goal: Launch')
  await waitFor(() => expect(screen.queryByText('Loading commitments…')).not.toBeInTheDocument())
  expect(screen.getByLabelText('Goal title')).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Save commitment' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Confirm completion: Launch' })).not.toBeInTheDocument()
  expect(screen.getAllByText('Source: release.md')).toHaveLength(2)
})
it('retains commitment identity and draft when a network failure is retried', async () => {
  vi.mocked(api.saveTrackedItem).mockRejectedValue(new Error('Offline'))
  await setup(); change('Commitment title', 'Persistent responsibility'); click('Save commitment'); await screen.findByRole('alert'); click('Save commitment')
  await waitFor(() => expect(api.saveTrackedItem).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.saveTrackedItem).mock.calls[0]).toEqual(vi.mocked(api.saveTrackedItem).mock.calls[1])
})
it('edits all goal fields and clears a deadline using the reviewed revision', async () => {
  await setup(); change('Goal title', 'Launch v2'); change('Goal description', 'Details'); change('Completion criteria', 'Published'); change('Goal due time (local, optional)', '')
  click('Save goal')
  await waitFor(() => expect(api.editGoal).toHaveBeenCalledWith(bot, 'g', { title: 'Launch v2', description: 'Details', completion_criteria: 'Published', due_at: null, expected_revision: 3 }))
})
it('preserves drafts after CAS conflict and reloads without silently rebasing a save', async () => {
  vi.mocked(api.editGoal).mockRejectedValueOnce(new bots.BotAPIError(409, 'Revision changed'))
  await setup(); change('Goal title', 'My draft'); click('Save goal')
  expect(await screen.findByRole('alert')).toHaveTextContent('Revision changed')
  expect(screen.getByLabelText('Goal title')).toHaveValue('My draft'); expect(screen.getByRole('button', { name: 'Save goal' })).toBeDisabled()
  click('Reload current state'); await screen.findByText('Current saved goal · revision 4')
  expect(screen.getByLabelText('Goal title')).toHaveValue('My draft')
  click('Save goal'); await waitFor(() => expect(api.editGoal).toHaveBeenLastCalledWith(bot, 'g', expect.objectContaining({ expected_revision: 4, title: 'My draft' })))
})
it('creates durable commitments and requires reasons for blocked or cancelled status', async () => {
  await setup(); change('Commitment title', 'Notify owners'); change('Commitment status', 'blocked')
  expect(screen.getByRole('button', { name: 'Save commitment' })).toBeDisabled()
  change('Commitment reason (required)', 'Awaiting signoff'); change('Commitment due time (local, optional)', '2030-01-01T10:00'); click('Save commitment')
  await waitFor(() => expect(api.saveTrackedItem).toHaveBeenCalledWith(bot, 'g', expect.any(String), expect.objectContaining({ expected_goal_revision: 3, title: 'Notify owners', status: 'blocked', reason: 'Awaiting signoff', due_at: expect.any(String), evidence: [] })))
})
it('requires evidence review, resets review on changes and keeps timestamp stable on retry', async () => {
  vi.mocked(api.confirmGoal).mockRejectedValue(new Error('Offline'))
  await setup(); expect(screen.getByRole('button', { name: 'Confirm completion: Launch' })).toBeDisabled()
  evidence(); change('Evidence source', 'Final report'); expect(screen.getByLabelText(/I reviewed the saved/)).not.toBeChecked()
  fireEvent.click(screen.getByLabelText(/I reviewed the saved/)); click('Confirm completion: Launch'); await screen.findByRole('alert'); click('Confirm completion: Launch')
  await waitFor(() => expect(api.confirmGoal).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.confirmGoal).mock.calls[0]).toEqual(vi.mocked(api.confirmGoal).mock.calls[1])
})
it('blocks goal completion while commitments are open and updates a commitment with fresh proof', async () => {
  vi.mocked(api.listTrackedItems).mockResolvedValue({ items: [{ id: 'i', goal_id: 'g', title: 'Verify', status: 'open', evidence: [] }] })
  await setup(); evidence(); expect(screen.getByRole('button', { name: 'Confirm completion: Launch' })).toBeDisabled()
  click('Edit commitment: Verify'); change('Commitment status', 'completed'); expect(screen.getByRole('button', { name: 'Save commitment' })).toBeDisabled()
  evidence(); click('Save commitment'); await waitFor(() => expect(api.saveTrackedItem).toHaveBeenCalledWith(bot, 'g', 'i', expect.objectContaining({ status: 'completed', evidence: [expect.objectContaining({ summary: 'I verified', source: 'Release report' })] })))
})
it('keeps all mutation inputs read-only and reports tracked-item loading failures', async () => {
  await setup(true); expect(screen.getByLabelText('Goal title')).toBeDisabled(); expect(screen.getByLabelText('Commitment title')).toBeDisabled(); expect(screen.getByLabelText('Completion summary')).toBeDisabled()
})
it('preserves a draft and blocks unsafe writes if post-save refresh fails', async () => {
  vi.mocked(bots.listBotGoals).mockRejectedValue(new Error('Offline'))
  await setup(); change('Goal title', 'Draft'); click('Save goal')
  expect(await screen.findByRole('alert')).toHaveTextContent('Saved, but current state could not be refreshed')
  expect(screen.getByLabelText('Goal title')).toHaveValue('Draft'); expect(screen.getByRole('button', { name: 'Save goal' })).toBeDisabled()
})
it('surfaces list failures without treating them as empty successful data', async () => {
  vi.mocked(api.listTrackedItems).mockRejectedValue(new Error('Cannot load commitments'))
  await setup(); expect(screen.getByRole('alert')).toHaveTextContent('Cannot load commitments'); expect(screen.getByRole('button', { name: 'Confirm completion: Launch' })).toBeDisabled()
})
