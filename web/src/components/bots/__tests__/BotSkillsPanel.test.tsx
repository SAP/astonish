import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BotSkillsPanel, { SkillRevision } from '../BotSkillsPanel'
import * as api from '@/api/botSkills'
import { getBotVersion, type Bot, type BotVersion } from '@/api/bots'
vi.mock('@/api/bots', async original => ({ ...await original<typeof import('@/api/bots')>(), getBotVersion: vi.fn() }))
vi.mock('@/api/botSkills', async original => ({ ...await original<typeof api>(), listBotSkills: vi.fn(), listBotSkillRevisions: vi.fn(), listBotSkillRuns: vi.fn(), importLegacyBotSkill: vi.fn() }))
const bot = { id: 'bot', scope: 'personal' } as Bot
const skill = { id: 'skill', bot_id: 'bot', title: 'Research', status: 'active', head_revision: 2, head_digest: 'digest' } as api.BotSkill
const revision = { id: 'revision', bot_id: 'bot', skill_id: 'skill', schema_version: 1, revision: 2, parent_revision: 1, digest: 'digest', responsibility: 'Check original sources', expectations: [{ id: 'e', text: 'Include links', provenance: { bot_id: 'bot', level: 'owner_specified', recorded_at: 'today' } }], method: ['Read sources'], sources: [], provenance: [], change_reason: 'Owner correction' } as api.BotSkillRevision
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api.listBotSkills).mockResolvedValue({ skills: [skill] }); vi.mocked(api.listBotSkillRevisions).mockResolvedValue({ revisions: [revision] }); vi.mocked(api.listBotSkillRuns).mockResolvedValue({ runs: [] }) })
it('inspects canonical revisions and receipts without mutating or interpreting prose', async () => {
  render(<BotSkillsPanel bot={bot} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Research' }))
  expect(await screen.findByText('Responsibility: Check original sources')).toBeInTheDocument()
  expect(screen.getByText('Include links')).toBeInTheDocument()
  expect(await screen.findByText('No linked runs.')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /approve|execute|save/i })).not.toBeInTheDocument()
})
it('shows loading, empty legacy and failed reads separately and retries', async () => {
  vi.mocked(api.listBotSkills).mockRejectedValueOnce(new Error('offline')).mockResolvedValue({ skills: [] })
  render(<BotSkillsPanel bot={bot} />)
  expect(screen.getByText('Loading skills…')).toBeInTheDocument()
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: 'Refresh skills' }))
  expect(await screen.findByText(/No committed skills yet/)).toBeInTheDocument()
})
it('hides old identities and marks failed refresh as stale', async () => {
  const view = render(<BotSkillsPanel bot={bot} />)
  await screen.findByRole('button', { name: 'Research' })
  vi.mocked(api.listBotSkills).mockRejectedValue(new Error('offline'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh skills' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Stale skills')
  view.rerender(<BotSkillsPanel bot={{ ...bot, id: 'other' }} />)
  expect(screen.queryByRole('button', { name: 'Research' })).not.toBeInTheDocument()
  await waitFor(() => expect(api.listBotSkills).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'other' }), expect.any(AbortSignal), expect.any(Object)))
})
it('drops selected history immediately on a scope switch', async () => {
  const view = render(<BotSkillsPanel bot={bot} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Research' }))
  await screen.findByText('Responsibility: Check original sources')
  view.rerender(<BotSkillsPanel bot={{ ...bot, scope: 'team' }} />)
  expect(screen.queryByRole('region', { name: 'Skill history' })).not.toBeInTheDocument()
  await waitFor(() => expect(api.listBotSkills).toHaveBeenLastCalledWith(expect.objectContaining({ scope: 'team' }), expect.any(AbortSignal), expect.any(Object)))
})
it('requires explicit confirmation and cancellation never writes', async () => {
  const trigger = { id: 'legacy', instructions: 'Read original releases' }
  vi.mocked(getBotVersion).mockResolvedValue({ triggers: [trigger] } as BotVersion)
  vi.mocked(api.importLegacyBotSkill).mockResolvedValue(skill)
  render(<BotSkillsPanel bot={bot} />)
  fireEvent.click(screen.getByRole('button', { name: 'Review legacy import' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Select legacy procedure legacy' }))
  expect(api.importLegacyBotSkill).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Cancel legacy import' }))
  expect(api.importLegacyBotSkill).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Review legacy import' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Select legacy procedure legacy' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm legacy import' }))
  expect(await screen.findByText(/Imported Research as a draft skill/)).toBeInTheDocument()
  expect(api.importLegacyBotSkill).toHaveBeenCalledExactlyOnceWith(bot, trigger, expect.any(String))
})
it('keeps failed imports unconfirmed and reuses the request key on retry', async () => {
  vi.mocked(getBotVersion).mockResolvedValue({ triggers: [{ id: 'legacy', instructions: 'Pinned source' }] } as BotVersion)
  vi.mocked(api.importLegacyBotSkill).mockRejectedValueOnce(new Error('stale version')).mockResolvedValue(skill)
  render(<BotSkillsPanel bot={bot} />)
  fireEvent.click(screen.getByRole('button', { name: 'Review legacy import' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Select legacy procedure legacy' }))
  fireEvent.click(screen.getByRole('button', { name: 'Confirm legacy import' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Import not confirmed')
  expect(screen.queryByText(/Imported Research/)).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Confirm legacy import' }))
  await screen.findByText(/Imported Research/)
  const calls = vi.mocked(api.importLegacyBotSkill).mock.calls
  expect(calls[0]).toEqual(calls[1])
})
it('separates source observations from proposed usefulness', () => {
  render(<SkillRevision value={{ ...revision, sources: [{ url: 'https://example.test', purpose: 'Proposed comparison source', access_method: 'fetch', observation: { status: 'returned', http_status: 200, call_id: 'call', truncated: true }, provenance: { level: 'proposed', bot_id: 'bot', recorded_at: 'today' } }] }} />)
  expect(screen.getByText('Access observation: returned · HTTP 200 · call call · truncated')).toBeInTheDocument()
  expect(screen.getByText(/Source usefulness is agent judgment/)).toBeInTheDocument()
  expect(screen.getByText(/proposed · today/)).toBeInTheDocument()
})
it('never interprets unknown schemas', () => {
  render(<SkillRevision value={{ ...revision, schema_version: 99 }} />)
  expect(screen.getByRole('status')).toHaveTextContent('Unsupported skill schema 99')
  expect(screen.queryByText(/Check original sources/)).not.toBeInTheDocument()
})
it('uses server cursors for skill pages and archived records', async () => {
  vi.mocked(api.listBotSkills).mockResolvedValueOnce({ skills: [skill], next: 'cursor' }).mockResolvedValue({ skills: [] })
  render(<BotSkillsPanel bot={bot} />)
  fireEvent.click(await screen.findByRole('button', { name: 'More skills' }))
  await waitFor(() => expect(api.listBotSkills).toHaveBeenLastCalledWith(bot, expect.any(AbortSignal), { after: 'cursor', include_archived: false }))
  fireEvent.click(screen.getByLabelText('Include archived skills'))
  await waitFor(() => expect(api.listBotSkills).toHaveBeenLastCalledWith(bot, expect.any(AbortSignal), { after: '', include_archived: true }))
})
