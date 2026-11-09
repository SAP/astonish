import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotDraftDialog from '../BotDraftDialog'
import * as api from '@/api/bots'
import { sourceBackend } from './sourceBackend'
let backend: ReturnType<typeof sourceBackend>
vi.mock('@/api/bots', async original => ({ ...await original<typeof api>(), promoteChatToBot: vi.fn(), createBot: vi.fn(), listBotTemplates: vi.fn(), getBotTemplate: vi.fn(), forkBotTemplate: vi.fn(), botLifecycle: vi.fn() }))
const bot = { id: 'new', scope: 'personal' } as api.Bot
beforeEach(() => {
  vi.clearAllMocks(); localStorage.clear(); window.location.hash = '#/chat/source'
  backend = sourceBackend()
})
it('restores persisted promotion when SQLite returns null for empty carryover arrays', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url === '/api/auth/me') return Response.json({ user: { id: 'owner' } })
    return Response.json({ id: 'saved', revision: 1, content: {
      mode: 'chat', source_id: 'source', name: 'Partial promotion', charter: '',
      language: '', timezone: 'UTC', identity: '', working_style: '', owner_preferences: '',
      carryover: { summary: 'Selected summary', memories: [{ content: 'Selected fact', category: 'fact' }], goals: null, artifacts: null },
    } })
  }))
  render(<BotDraftDialog open resumeId="saved" onClose={vi.fn()} />)
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue('Partial promotion'))
  expect(screen.getByLabelText('Carryover summary')).toHaveValue('Selected summary')
  expect(screen.getByLabelText('Memory content 1')).toHaveValue('Selected fact')
  expect(screen.getByRole('button', { name: 'Review draft' })).toBeDisabled()
  expect(api.createBot).not.toHaveBeenCalled()
  expect(api.promoteChatToBot).not.toHaveBeenCalled()
})

function fill() {
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Watcher' } })
  fireEvent.change(screen.getByLabelText('Bot charter'), { target: { value: 'Observe' } })
}
async function review() { fireEvent.click(screen.getByRole('button', { name: 'Review draft' })); await screen.findByRole('button', { name: 'Save reviewed draft' }) }
function save() {
  fireEvent.click(screen.getByRole('checkbox'))
  fireEvent.click(screen.getByRole('button', { name: 'Save reviewed draft' }))
}
it('reviews selected text copies, preserves edits on Back, and never activates or changes source on failure', async () => {
  backend.failDecision('Try again')
  render(<BotDraftDialog open sourceSession="source" onClose={vi.fn()} />)
  fill()
  fireEvent.change(screen.getByLabelText('Carryover summary'), { target: { value: 'Redacted summary' } })
  fireEvent.click(screen.getByRole('button', { name: 'Add memory' }))
  fireEvent.change(screen.getByLabelText('Memory content 1'), { target: { value: 'Selected fact' } })
  fireEvent.click(screen.getByRole('button', { name: 'Add goal' }))
  fireEvent.change(screen.getByLabelText('Goal title 1'), { target: { value: 'Verify' } })
  fireEvent.change(screen.getByLabelText('Goal criteria 1'), { target: { value: 'Official source' } })
  fireEvent.click(screen.getByRole('button', { name: 'Add text snapshot' }))
  fireEvent.change(screen.getByLabelText('Snapshot title 1'), { target: { value: 'Report' } })
  fireEvent.change(screen.getByLabelText('Snapshot content 1'), { target: { value: 'Reviewed excerpt' } })
  await review()
  expect(screen.getByText('Selected fact')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Back' }))
  expect(screen.getByLabelText('Memory content 1')).toHaveValue('Selected fact')
  await review(); save()
  expect(await screen.findByRole('alert')).toHaveTextContent('Try again')
  expect(window.location.hash).toBe('#/chat/source')
  fireEvent.click(screen.getByRole('button', { name: 'Save reviewed draft' }))
  await waitFor(() => expect(backend.decisions).toHaveLength(2))
  const request = backend.proposals[0].request
  expect(request.carryover).toEqual({ summary: 'Redacted summary', memories: [{ content: 'Selected fact', category: 'fact' }], goals: [{ title: 'Verify', completion_criteria: 'Official source' }], artifacts: [{ title: 'Report', content: 'Reviewed excerpt' }] })
  expect(backend.decisions[1]).toEqual(backend.decisions[0])
  expect(backend.proposals).toHaveLength(1)
  expect(api.botLifecycle).not.toHaveBeenCalled()
})
it('reviews template details and forks the selected revision to a private draft', async () => {
  const template: api.BotTemplate = { id: 'tpl', revision: 7, definition: { schema_version: 1, name: 'Template', charter: 'Observe', language: 'en', suggested_goals: [{ title: 'Watch release', description: 'Official releases', completion_criteria: 'Published release' }], suggested_schedules: [{ cron: '0 9 * * *', timezone: 'UTC', instructions: 'Check releases' }] } }
  vi.mocked(api.listBotTemplates).mockResolvedValue({ templates: [template] })
  vi.mocked(api.getBotTemplate).mockResolvedValue(template)
  vi.mocked(api.forkBotTemplate).mockResolvedValue(bot)
  render(<BotDraftDialog inline open onClose={vi.fn()} />)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Browse team templates' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Template · revision 7' }))
  await screen.findByText('Watch release')
  expect(screen.getByText(/0 9 \* \* \*/)).toBeInTheDocument()
  await review(); save()
  await waitFor(() => expect(backend.decisions).toHaveLength(1))
  expect(backend.proposals[0].request).toMatchObject({ mode: 'template', source_id: template.id, template_revision: 7, name: 'Template' })
  await waitFor(() => expect(window.location.hash).toContain('/bots/personal/new'))
  expect(api.createBot).not.toHaveBeenCalled()
  expect(api.botLifecycle).not.toHaveBeenCalled()
})
it('requires complete carryover rows, permits removal, and renews consent and key after edits', async () => {
  backend.failDecision('Try again')
  render(<BotDraftDialog open sourceSession="source" onClose={vi.fn()} />)
  fill()
  fireEvent.click(screen.getByRole('button', { name: 'Add memory' }))
  expect(screen.getByRole('button', { name: 'Review draft' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Remove memory 1' }))
  await review(); save()
  await screen.findByRole('alert')
  const key = backend.proposals[0].request.idempotency_key
  fireEvent.click(screen.getByRole('button', { name: 'Back' }))
  fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Edited' } })
  await review()
  expect(screen.getByRole('checkbox')).not.toBeChecked()
  expect(screen.getByRole('button', { name: 'Save reviewed draft' })).toBeDisabled()
  save()
  await waitFor(() => expect(backend.decisions).toHaveLength(2))
  expect(backend.proposals[1].request.idempotency_key).not.toBe(key)
})
it('shows template failure and empty states, and permits returning to a blank draft', async () => {
  vi.mocked(api.listBotTemplates).mockRejectedValueOnce(new Error('Templates unavailable')).mockResolvedValueOnce({ templates: [] })
  render(<BotDraftDialog open onClose={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Browse team templates' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Templates unavailable')
  fireEvent.click(screen.getByRole('button', { name: 'Browse team templates' }))
  expect(await screen.findByText(/No team templates available/)).toBeInTheDocument()
  fill(); await review()
  expect(screen.getByText('Observe')).toBeInTheDocument()
  expect(api.forkBotTemplate).not.toHaveBeenCalled()
})
it('retains mounted intake when hidden/reopened but isolates a changed source', () => {
  const close = vi.fn()
  const { rerender } = render(<BotDraftDialog open sourceSession="source" onClose={close} />)
  fill()
  rerender(<BotDraftDialog open={false} sourceSession="source" onClose={close} />)
  rerender(<BotDraftDialog open sourceSession="source" onClose={close} />)
  expect(screen.getByLabelText('Bot name')).toHaveValue('Watcher')
  fill()
  rerender(<BotDraftDialog open sourceSession="other" onClose={close} />)
  expect(screen.getByLabelText('Bot name')).toHaveValue('')
  expect(api.promoteChatToBot).not.toHaveBeenCalled()
})
it('requires explicit discard for dirty cancellation without creating a Bot', async () => {
  const close = vi.fn()
  const { rerender } = render(<BotDraftDialog open sourceSession="source" onClose={close} />)
  fill(); await review()
  fireEvent.click(screen.getByRole('button', { name: 'Cancel intake' }))
  expect(close).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Keep editing' }))
  expect(screen.getByText('Watcher')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Cancel intake' }))
  fireEvent.click(screen.getByRole('button', { name: 'Discard intake' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(api.promoteChatToBot).not.toHaveBeenCalled()
  expect(window.location.hash).toBe('#/chat/source')
  rerender(<BotDraftDialog open={false} sourceSession="source" onClose={close} />)
  rerender(<BotDraftDialog open sourceSession="source" onClose={close} />)
  expect(screen.getByLabelText('Bot name')).toHaveValue('')
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(false)
})

it('retains dirty intake and reload warning when Escape persistence fails', async () => {
  const close = vi.fn()
  backend.failSave('Intake unavailable')
  const storage = vi.spyOn(Storage.prototype, 'setItem')
  render(<BotDraftDialog open onClose={close} />)
  fill()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  expect(close).not.toHaveBeenCalled()
  expect(await screen.findByRole('alert')).toHaveTextContent('Intake not saved')
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(true)
  expect(storage).not.toHaveBeenCalled()
  storage.mockRestore()
})

it('closes empty intake without confirmation and removes the reload guard on unmount', () => {
  const close = vi.fn()
  const { unmount } = render(<BotDraftDialog open onClose={close} />)
  fireEvent.click(screen.getByRole('button', { name: 'Cancel intake' }))
  expect(close).toHaveBeenCalledOnce()
  unmount()
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(false)
})

it('reviews and saves blank-draft persona using the existing creation contract only', async () => {
  vi.mocked(api.createBot).mockResolvedValue(bot)
  render(<BotDraftDialog open onClose={vi.fn()} />)
  fill()
  fireEvent.change(screen.getByLabelText('Bot language'), { target: { value: 'de' } })
  fireEvent.change(screen.getByLabelText('Bot timezone'), { target: { value: 'Europe/Berlin' } })
  fireEvent.change(screen.getByLabelText('Bot identity'), { target: { value: 'Release observer' } })
  await review()
  expect(screen.getByText('Release observer')).toBeInTheDocument()
  expect(screen.getByText(/No contact, publishing or spending permission/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Back' }))
  expect(screen.getByLabelText('Bot identity')).toHaveValue('Release observer')
  await review(); save()
  await waitFor(() => expect(api.createBot).toHaveBeenCalledWith({
    name: 'Watcher', idempotency_key: expect.any(String), definition: { charter: 'Observe', persona: {
      language: 'de', timezone: 'Europe/Berlin', soul: '', identity: 'Release observer', user: '',
    } },
  }))
  await waitFor(() => expect(screen.getByLabelText('Bot name')).toHaveValue(''))
  expect(api.botLifecycle).not.toHaveBeenCalled()
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(false)
})

it('blocks Escape and cancellation during reviewed save, preserving a failed request for retry', async () => {
  backend.failSave('Intake unavailable')
  let rejectSave!: (error: Error) => void
  vi.mocked(api.createBot).mockImplementationOnce(() => new Promise((_, reject) => { rejectSave = reject }))
  const close = vi.fn()
  render(<BotDraftDialog open onClose={close} />)
  fill(); await review(); save()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(close).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Cancel intake' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Discard intake' })).not.toBeInTheDocument()
  rejectSave(new Error('Save unavailable'))
  expect(await screen.findByRole('alert')).toHaveTextContent('Save unavailable')
  fireEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Intake not saved')
  expect(close).not.toHaveBeenCalled()
})
