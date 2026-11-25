import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import BotFilesPanel, { BotPublicationDelivery } from '../BotFilesPanel'
import * as api from '@/api/botFiles'
import { BotAPIError, type Bot } from '@/api/bots'
vi.mock('@/api/botFiles', async original => ({ ...await original<typeof api>(), listBotFiles: vi.fn(), previewBotFile: vi.fn(), downloadBotFile: vi.fn(), botFileAccess: vi.fn() }))
const bot = { id: 'b', scope: 'personal', status: 'paused', home: { id: 'h' } } as Bot
const entry: api.BotFileEntry = { path: 'MEMORY.md', kind: 'file', size_bytes: 20, freshness: 'one', preview: 'text' }
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api.botFileAccess).mockImplementation((target, preview, freshness) => ({ identity: target.botId + target.path, freshness, preview: preview === 'text', readPreview: signal => api.previewBotFile(target, signal), download: signal => api.downloadBotFile(target, signal) })); vi.mocked(api.listBotFiles).mockResolvedValue({ entries: [entry] }); vi.mocked(api.previewBotFile).mockResolvedValue({ entry, content: '# Current memory', truncated: false }) })
it('is lazy while closed, fetches selection only and aborts on close', async () => {
  const view = render(<BotFilesPanel bot={bot} enabled={false} />)
  expect(api.listBotFiles).not.toHaveBeenCalled()
  view.rerender(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  expect(await screen.findByRole('heading', { name: 'Current memory' })).toBeVisible()
  expect(api.previewBotFile).toHaveBeenCalledTimes(1)
  const signal = vi.mocked(api.previewBotFile).mock.calls[0][1]!
  view.rerender(<BotFilesPanel bot={bot} enabled={false} />)
  expect(signal.aborted).toBe(true)
  expect(screen.queryByText('Current memory')).toBeNull()
})
it('does not fetch binary previews and exposes only raw download', async () => {
  vi.mocked(api.listBotFiles).mockResolvedValue({ entries: [{ ...entry, path: 'large.bin', preview: 'download' }] })
  render(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'large.bin' }))
  expect(await screen.findByText('Preview unavailable. Download this file.')).toBeVisible()
  expect(api.previewBotFile).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(screen.queryByText('Download as PDF')).toBeNull()
  fireEvent.click(screen.getByText('Download File'))
  await waitFor(() => expect(api.downloadBotFile).toHaveBeenCalledTimes(1))
})
it('clears inaccessible files but labels transient last-known listing', async () => {
  render(<BotFilesPanel bot={bot} enabled />)
  await screen.findByRole('button', { name: 'MEMORY.md' })
  vi.mocked(api.listBotFiles).mockRejectedValueOnce(new Error('Offline'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh files' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Last known listing')
  vi.mocked(api.listBotFiles).mockRejectedValueOnce(new BotAPIError(403, 'Revoked'))
  fireEvent.click(screen.getByRole('button', { name: 'Retry files' }))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'MEMORY.md' })).toBeNull())
})
it('never provisions unprepared Homes and ignores stale identity completion', async () => {
  const view = render(<BotFilesPanel bot={{ ...bot, home: null }} enabled />)
  expect(api.listBotFiles).not.toHaveBeenCalled()
  let resolve!: (page: api.BotFilePage) => void
  vi.mocked(api.listBotFiles).mockReturnValueOnce(new Promise(r => { resolve = r }))
  view.rerender(<BotFilesPanel bot={bot} enabled />)
  const signal = vi.mocked(api.listBotFiles).mock.calls[0][1]!
  vi.mocked(api.listBotFiles).mockResolvedValue({ entries: [] })
  view.rerender(<BotFilesPanel bot={{ ...bot, id: 'other' }} enabled />)
  expect(signal.aborted).toBe(true)
  await act(async () => resolve({ entries: [entry] }))
  expect(await screen.findByText('Directory is empty.')).toBeVisible()
  expect(screen.queryByText('MEMORY.md')).toBeNull()
})
it('opts into hidden files and clears hidden selection when hiding again', async () => {
  vi.mocked(api.listBotFiles).mockImplementation(async (_target, _signal, _cursor, hidden) => ({ entries: hidden ? [{ ...entry, path: '.notes.md' }] : [entry] }))
  render(<BotFilesPanel bot={bot} enabled />)
  await screen.findByRole('button', { name: 'MEMORY.md' })
  expect(screen.getByRole('checkbox', { name: 'Show hidden files' })).not.toBeChecked()
  fireEvent.click(screen.getByRole('checkbox', { name: 'Show hidden files' }))
  fireEvent.click(await screen.findByRole('button', { name: '.notes.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
  const signal = vi.mocked(api.previewBotFile).mock.calls[0][1]!
  fireEvent.click(screen.getByRole('checkbox', { name: 'Show hidden files' }))
  expect(signal.aborted).toBe(true)
  expect(screen.queryByRole('region', { name: 'File preview' })).toBeNull()
  await screen.findByRole('button', { name: 'MEMORY.md' })
})
it.each(['paused', 'stopped'] as const)('allows read-only %s browsing in both scopes', async status => {
  const view = render(<BotFilesPanel bot={{ ...bot, status }} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
  const signal = vi.mocked(api.previewBotFile).mock.calls[0][1]!
  view.rerender(<BotFilesPanel bot={{ ...bot, status, scope: 'team' }} enabled />)
  expect(signal.aborted).toBe(true)
  expect(screen.queryByRole('heading', { name: 'Current memory' })).toBeNull()
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
})
it.each([401, 403, 404, 410])('discards listings and preview bytes after HTTP %s', async status => {
  render(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
  vi.mocked(api.listBotFiles).mockRejectedValueOnce(new BotAPIError(status, 'Unavailable'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh files' }))
  await screen.findByRole('alert')
  expect(screen.queryByRole('button', { name: 'MEMORY.md' })).toBeNull()
  expect(screen.queryByRole('heading', { name: 'Current memory' })).toBeNull()
})
it('surfaces unsupported and empty states without a preview request', async () => {
  vi.mocked(api.listBotFiles).mockRejectedValueOnce(new BotAPIError(501, 'File browsing unsupported'))
  render(<BotFilesPanel bot={bot} enabled />)
  expect(await screen.findByRole('alert')).toHaveTextContent('File browsing unsupported')
  vi.mocked(api.listBotFiles).mockResolvedValueOnce({ entries: [] })
  fireEvent.click(screen.getByRole('button', { name: 'Retry files' }))
  expect(await screen.findByText('Directory is empty.')).toBeVisible()
  expect(api.previewBotFile).not.toHaveBeenCalled()
})
it('clears and aborts selected content when deletion starts', async () => {
  const view = render(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
  const signal = vi.mocked(api.previewBotFile).mock.calls[0][1]!
  view.rerender(<BotFilesPanel bot={{ ...bot, permanent_delete: true }} enabled />)
  expect(signal.aborted).toBe(true)
  expect(screen.getByText('Bot files are unavailable.')).toBeVisible()
  expect(screen.queryByText('Current memory')).toBeNull()
})
it('aborts preview when the retained Home identity changes', async () => {
  const view = render(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'MEMORY.md' }))
  await screen.findByRole('heading', { name: 'Current memory' })
  const signal = vi.mocked(api.previewBotFile).mock.calls[0][1]!
  view.rerender(<BotFilesPanel bot={{ ...bot, home: { ...bot.home!, id: 'replacement' } }} enabled />)
  expect(signal.aborted).toBe(true)
  expect(screen.queryByRole('heading', { name: 'Current memory' })).toBeNull()
  await screen.findByRole('button', { name: 'MEMORY.md' })
})
it('shows binary ticket failures instead of claiming a completed download', async () => {
  vi.mocked(api.listBotFiles).mockResolvedValueOnce({ entries: [{ ...entry, path: 'large.bin', preview: 'download' }] })
  vi.mocked(api.downloadBotFile).mockRejectedValueOnce(new BotAPIError(403, 'Download permission revoked'))
  render(<BotFilesPanel bot={bot} enabled />)
  fireEvent.click(await screen.findByRole('button', { name: 'large.bin' }))
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  fireEvent.click(screen.getByRole('button', { name: 'Download File' }))
  expect(await screen.findByText(/Download permission revoked/)).toBeVisible()
  expect(api.previewBotFile).not.toHaveBeenCalled()
})
it('preserves immutable ordered artifact IDs, without eager previews', () => {
  const publication: api.BotFilePublication = { schema_version: 1, bot_id: 'b', run_id: 'r', home_id: 'h', home_session_id: 's', report_sequence: 4, event_id: 'event', summary: 'Report ready.', artifacts: ['second', 'first'].map(id => ({ id, home_id: 'h', relative_path: id + '.md', sha256: 'a'.repeat(64), size_bytes: 12, media_type: 'text/markdown', title: '', preview: 'text' })) }
  const view = render(<BotPublicationDelivery publication={publication} scope="team" />)
  expect([...view.container.querySelectorAll('[data-artifact-id]')].map(e => e.getAttribute('data-artifact-id'))).toEqual(['second', 'first'])
  expect(api.previewBotFile).not.toHaveBeenCalled()
})
