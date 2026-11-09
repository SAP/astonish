import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BotSidebar from '../BotSidebar'
import BotWorkspace from '../BotWorkspace'
import * as snapshots from '@/api/botSnapshot'
import * as bots from '@/api/bots'
import type { BotSnapshot } from '@/api/botSnapshot'
vi.mock('@/api/botSnapshot', () => ({ getBotSnapshot: vi.fn(), readBotActivityPage: vi.fn(), writeBotReadCursor: vi.fn() }))
vi.mock('@/api/auth', () => ({ getMe: async () => ({ user: { id: 'owner' } }) }))
vi.mock('@/api/botIntake', () => ({ currentIntakeNamespace: async () => 'fixture', readIntakePointer: () => null }))
vi.mock('../BotDraftDialog', () => ({ default: () => null }))
vi.mock('../BotPanels', () => ({ BotGoals: () => null, BotMemoryCharterPanel: () => null, BotSchedule: () => null, BotTemplatePublication: () => null }))
vi.mock('../BotControls', () => ({ BotDecisionInbox: () => null, BotLifecycleControls: () => null, BotPolicySummary: () => null }))
vi.mock('../../StudioChat', () => ({ default: () => <div>Shared transcript</div> }))
vi.mock('@/api/bots', async original => ({ ...await original<typeof bots>(), listBots: vi.fn(), getBotVersion: vi.fn(), listBotRuns: vi.fn(), listBotGoals: vi.fn() }))
const bot: bots.Bot = { id: 'fixture', scope: 'personal', owner_id: 'owner', name: 'Watcher', status: 'draft', version: 1, revision: 1, home_session_id: '', home: null, created_at: '', updated_at: '' }
const makeSnapshot = (read: number): BotSnapshot => ({ bot, read_cursor: { bot_id: bot.id, viewer_id: 'owner', sequence: read }, latest_sequence: 121, unread_count: 121-read, pending_approvals: [], latest_outcome: { id: 'latest', status: 'succeeded', report: { summary: 'Finished background check' } } as bots.BotRun })
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(bots.listBots).mockResolvedValue({ bots: [bot] })
  vi.mocked(bots.listBotRuns).mockResolvedValue({ runs: [] })
  vi.mocked(bots.listBotGoals).mockResolvedValue({ goals: [] })
  vi.mocked(bots.getBotVersion).mockResolvedValue(undefined as unknown as bots.BotVersion)
  vi.mocked(snapshots.getBotSnapshot).mockResolvedValue(makeSnapshot(0))
  vi.mocked(snapshots.readBotActivityPage).mockImplementation(async (_id, _scope, _team, after) => ({ activity: Array.from({ length: Math.min(50, 121-after) }, (_, i) => ({ sequence: after+i+1, kind: 'report', created_at: '', payload: {} })) }))
  vi.mocked(snapshots.writeBotReadCursor).mockImplementation(async () => { vi.mocked(snapshots.getBotSnapshot).mockResolvedValue(makeSnapshot(121)) })
})
it.each([false, true])('keeps sidebar name-only while workspace details own unread activity (collapsed=%s)', async collapsed => {
  const { rerender } = render(<BotSidebar collapsed={collapsed} />)
  const link = await screen.findByRole('link', { name: 'Watcher' })
  expect(link).toHaveTextContent(collapsed ? /^$/ : /^Watcher$/)
  expect(link).toHaveAttribute('title', 'Watcher')
  expect(snapshots.getBotSnapshot).not.toHaveBeenCalled()
  expect(snapshots.readBotActivityPage).not.toHaveBeenCalled()
  expect(snapshots.writeBotReadCursor).not.toHaveBeenCalled()
  rerender(<><BotSidebar collapsed={collapsed} /><BotWorkspace route={{ botId: bot.id, scope: bot.scope }} theme="dark" userId="owner" /></>)
  fireEvent.click(await screen.findByRole('button', { name: 'Details' }))
  fireEvent.click(screen.getByText('Runs & activity', { selector: 'summary' }))
  const count = (unread: number) => expect(screen.getAllByText(new RegExp(`${unread} unread updates`))).toHaveLength(1)
  await waitFor(() => count(121))
  expect(screen.getAllByText(/Finished background check/)).toHaveLength(1)
  expect(await screen.findByText('#121 report ·')).toBeInTheDocument()
  await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Mark displayed activity read' })))
  await waitFor(() => count(0))
  expect(screen.getByRole('link', { name: 'Watcher' })).toHaveTextContent(collapsed ? /^$/ : /^Watcher$/)
  expect(snapshots.writeBotReadCursor).toHaveBeenCalledWith('fixture', 'personal', null, 121, expect.any(AbortSignal))
})
