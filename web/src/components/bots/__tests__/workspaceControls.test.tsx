import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BotRunNow from '../BotRunNow'
import { BotMemoryCharterPanel } from '../BotPanels'
import * as memoryApi from '@/api/botMemory'
import * as api from '@/api/bots'
vi.mock('@/api/bots', async original => ({ ...await original<typeof api>(), requestBotRun: vi.fn() }))
vi.mock('@/api/botMemory', () => ({ getBotDocuments: vi.fn(), getBotMemoryHealth: vi.fn(), saveBotDocument: vi.fn() }))
const bot = { id: 'b', scope: 'team', owner_id: 'o', name: 'Bot', revision: 1, version: 1, status: 'active' } as api.Bot
beforeEach(() => vi.clearAllMocks())
it('preserves manual instructions and idempotency on failed admission retry', async () => {
  vi.mocked(api.requestBotRun).mockRejectedValueOnce(new Error('Temporary failure')).mockResolvedValueOnce({ id: 'run' } as api.BotRun)
  const onRun = vi.fn()
  render(<BotRunNow bot={bot} disabled={false} onRun={onRun} />)
  fireEvent.change(screen.getByLabelText('Run instructions'), { target: { value: 'Check evidence' } })
  fireEvent.click(screen.getByRole('button', { name: 'Run now' }))
  await screen.findByText('Temporary failure')
  expect(screen.getByLabelText('Run instructions')).toHaveValue('Check evidence')
  fireEvent.click(screen.getByRole('button', { name: 'Run now' }))
  await waitFor(() => expect(onRun).toHaveBeenCalled())
  expect(vi.mocked(api.requestBotRun).mock.calls[0]).toEqual(vi.mocked(api.requestBotRun).mock.calls[1])
})
it('preserves charter CAS conflict edits through version refetch', async () => {
  vi.mocked(memoryApi.saveBotDocument).mockRejectedValue(new api.BotAPIError(409, 'Review latest revision'))
  vi.mocked(memoryApi.getBotDocuments).mockResolvedValue({ bot_id: bot.id, revision: 1, version: 1, documents: [{ name: 'CHARTER.md', content: 'Original' }] })
  vi.mocked(memoryApi.getBotMemoryHealth).mockRejectedValue(new Error('Health unavailable'))
  const view = render(<BotMemoryCharterPanel bot={bot} readOnly={false} />)
  const editor = await screen.findByLabelText('Content CHARTER.md')
  fireEvent.change(editor, { target: { value: 'Local changes' } })
  fireEvent.change(screen.getByLabelText('Reason CHARTER.md'), { target: { value: 'Reviewed reason' } })
  fireEvent.click(screen.getByRole('button', { name: 'Review CHARTER.md' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save CHARTER.md' }))
  await screen.findByText('Review latest revision')
  view.rerender(<BotMemoryCharterPanel bot={{ ...bot, revision: 2, version: 2 }} readOnly={false} />)
  expect(screen.getByLabelText('Content CHARTER.md')).toBe(editor)
  expect(editor).toHaveValue('Local changes')
  vi.mocked(memoryApi.getBotDocuments).mockResolvedValue({ bot_id: bot.id, revision: 2, version: 2, documents: [{ name: 'CHARTER.md', content: 'Server changed' }] })
  fireEvent.click(screen.getByRole('button', { name: 'Reload and compare CHARTER.md' }))
  await screen.findByText('Server changed')
  expect(editor).toHaveValue('Local changes')
  expect(screen.getByLabelText('Reason CHARTER.md')).toHaveValue('Reviewed reason')
  expect(screen.getByRole('button', { name: 'Review CHARTER.md' })).toBeDisabled()
})
