import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, waitFor, fireEvent, within } from '@testing-library/react'
import budgetStop from '../../../../pkg/api/testdata/bot_budget_stop.json'
import StudioChat from '../StudioChat'
import BotWorkspace from '../bots/BotWorkspace'
import * as snapshots from '../../api/botSnapshot'
import * as reviews from '../../api/botReviews'
import { BotAPIError } from '../../api/bots'
import type { BotSnapshot } from '../../api/botSnapshot'
vi.mock('../../api/botSnapshot', () => ({ getBotSnapshot: vi.fn(), readBotActivityPage: vi.fn().mockResolvedValue({ activity: [] }), writeBotReadCursor: vi.fn() }))
vi.mock('../../api/botReviews', async original => ({ ...await original<typeof reviews>(), listControlProposals: vi.fn().mockResolvedValue({ proposals: [] }), sendBotMessage: vi.fn() }))
import { connectBotEvents, type BotStreamOptions } from '../../api/botEvents'
import * as teamContext from '../../api/teamContext'
import { connectChat, connectChatStream, fetchCacheDiagnostics, fetchKnowledgeDebug, fetchSessionHistory, fetchSessionStatus, patchSessionModel } from '../../api/studioChat'

vi.mock('../../api/botEvents', () => ({ connectBotEvents: vi.fn().mockReturnValue(new AbortController()) }))
vi.mock('../../api/bots', async original => ({ ...await original<typeof import('../../api/bots')>(), listBots: vi.fn().mockResolvedValue({ bots: [] }) }))

function canonicalFrame(type: string, data: Record<string, unknown>, row: string, cursor: number, part = 0): string {
  return `id: bot-home:${cursor}\nevent: ${type}\ndata: ${JSON.stringify({ ...data, _projection: { event_id: `bot-home/${row}/${part}`, session_id: 'bot-home', run_id: '11111111-1111-4111-8111-111111111111', turn_id: data.invocationId, cursor: `bot-home:${cursor}` } })}\n\n`
}

// Mock all API modules
vi.mock('../../api/studioChat', async original => ({
  ...await original<typeof import('../../api/studioChat')>(),
  fetchSessions: vi.fn().mockResolvedValue([]),
  fetchSessionHistory: vi.fn().mockResolvedValue([]),
  deleteSession: vi.fn().mockResolvedValue({}),
  connectChat: vi.fn().mockReturnValue(new AbortController()),
  fetchCacheDiagnostics: vi.fn().mockResolvedValue({ sessionId: 'sess-debug', assistantTurn: 1, rounds: [] }),
  fetchKnowledgeDebug: vi.fn().mockResolvedValue({ sessionId: 'sess-debug', invocationId: 'inv-1', knowledge: null, tools: null }),
  stopChat: vi.fn().mockResolvedValue({}),
  fetchSessionStatus: vi.fn().mockResolvedValue({ running: false }),
  connectChatStream: vi.fn().mockReturnValue(new AbortController()),
  fetchNetworkDenials: vi.fn().mockResolvedValue([]),
  fetchSessionModelStatus: vi.fn().mockResolvedValue(null),
  patchSessionModel: vi.fn().mockResolvedValue({}),
  fetchAvailableProviders: vi.fn().mockResolvedValue([]),
}))

vi.mock('../../api/platform', () => ({
  listSessionMemories: vi.fn().mockResolvedValue({ memories: [] }),
}))

vi.mock('../../api/fleetChat', () => ({
  startFleetSession: vi.fn().mockResolvedValue({}),
  connectFleetStream: vi.fn().mockReturnValue(new AbortController()),
  sendFleetMessage: vi.fn().mockResolvedValue({}),
  stopFleetSession: vi.fn().mockResolvedValue({}),
  fetchFleetSessions: vi.fn().mockResolvedValue([]),
}))

// Mock HomePage to avoid its dependencies
vi.mock('../HomePage', () => ({ default: () => <div data-testid="home-page">HomePage</div> }))

// Mock chat sub-components
vi.mock('../chat/FleetStartDialog', () => ({ default: () => null }))
vi.mock('../chat/FleetTemplatePicker', () => ({ default: () => null }))
vi.mock('../chat/FleetExecutionPanel', () => ({ default: () => null }))
vi.mock('../chat/chatTypes', async original => ({
  ...await original<typeof import('../chat/chatTypes')>(),
  getAgentColor: () => '#888',
}))

// Mock react-markdown to avoid ESM issues
vi.mock('react-markdown', () => ({
  default: ({ children }: { children: string }) => <span>{children}</span>,
}))
vi.mock('remark-gfm', () => ({
  default: () => {},
}))

describe('StudioChat', () => {
  it.each(['personal', 'team'] as const)('renders format-3 public conversation once live and after reload (%s)', async scope => {
    // SQL encoding is exercised by the Go fixture; the browser receives only
    // existing canonical Chat projections. Use the real transport here.
    const transport = await vi.importActual<typeof import('../../api/botEvents')>('../../api/botEvents')
    const onSend = vi.fn().mockResolvedValue(undefined)
    const renderReview = vi.fn((_kind: 'approval' | 'proposal', id: string) => <div data-review-id={id}>Which markets?</div>)
    const question = canonicalFrame('bot_review', { kind: 'approval', reviewId: 'q', invocationId: 'turn', eventId: 'question' }, 'question', 1)
    const answer = canonicalFrame('user_message', { text: 'U.S. markets and major indexes', invocationId: 'turn', eventId: 'answer' }, 'answer', 2)
    const acknowledgement = canonicalFrame('text', { text: 'I will research U.S. markets.', invocationId: 'turn' }, 'ack', 3)
    const tools = canonicalFrame('tool_call', { id: 'search', name: 'perplexity_web_search', args: { query: 'U.S. major indexes' }, invocationId: 'turn' }, 'search', 3)
      + canonicalFrame('tool_result', { id: 'search', name: 'perplexity_web_search', result: { text: 'Major indexes rose' }, invocationId: 'turn' }, 'search-result', 4)
    const final = canonicalFrame('text', { text: 'Major indexes rose. Source: https://fixture.test/index', invocationId: 'final-turn' }, 'final', 5)
      + canonicalFrame('turn_complete', { invocationId: 'final-turn' }, 'final', 6, 1)
    const replay = 'id: bot-home:6\nevent: replay_complete\ndata: {"persisted":true}\n\n'
    let writer!: ReadableStreamDefaultController<Uint8Array>
    const fetch = vi.spyOn(teamContext, 'teamFetch')
    const mount = () => {
      vi.mocked(connectBotEvents).mockImplementationOnce(transport.connectBotEvents)
      fetch.mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(c) { writer = c } })))
      return render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope, readOnly: false, onSend, renderReview }} />)
    }
    const push = (text: string) => act(async () => writer.enqueue(new TextEncoder().encode(text)))
    let view = mount()
    try {
      await push('id: bot-home:0\nevent: replay_complete\ndata: {"persisted":true}\n\n')
      expect(screen.queryByTestId('agent-message-bubble')).not.toBeInTheDocument()
      await push(question)
      expect(screen.getByText('Which markets?')).toBeVisible()
      await push(answer)
      expect(screen.getByText('U.S. markets and major indexes')).toBeVisible()
      expect(screen.queryByText('I will research U.S. markets.')).not.toBeInTheDocument()
      await push(acknowledgement)
      expect(screen.getByText('I will research U.S. markets.')).toBeVisible()
      expect(screen.queryByText(/Major indexes rose/)).not.toBeInTheDocument()
      await push(tools + final + replay)
      const assertConversation = () => {
        for (const text of ['U.S. markets and major indexes', 'I will research U.S. markets.', 'Major indexes rose. Source: https://fixture.test/index']) expect(screen.getAllByText(text)).toHaveLength(1)
        expect(view.container.textContent!.indexOf('U.S. markets')).toBeLessThan(view.container.textContent!.indexOf('I will research'))
        expect(onSend).not.toHaveBeenCalled()
        expect(connectChat).not.toHaveBeenCalled()
        expect(fetchSessionHistory).not.toHaveBeenCalled()
      }
      assertConversation()
      await push(final + replay)
      assertConversation()
      view.unmount()
      view = mount()
      await push(question + answer + acknowledgement + tools + final + replay)
      assertConversation()
    } finally {
      view.unmount()
      fetch.mockRestore()
    }
  })

  it('preserves a focused unfinished composer draft while an active Chat stream completes', async () => {
    vi.mocked(fetchSessionStatus).mockResolvedValueOnce({ sessionId: 'draft-stream', running: true })
    vi.mocked(fetchSessionHistory).mockResolvedValueOnce({ id: 'draft-stream', title: 'Draft stream', messages: [] })
    const view = render(<StudioChat theme="dark" initialSessionId="draft-stream" hideSidebar />)
    await waitFor(() => expect(connectChatStream).toHaveBeenCalled())
    const stream = vi.mocked(connectChatStream).mock.calls.at(-1)![0]
    const composer = await screen.findByTestId('chat-input')
    expect(composer).not.toBeDisabled()
    composer.focus()
    fireEvent.change(composer, { target: { value: 'Unfinished next instruction' } })
    const sendsBefore = vi.mocked(connectChat).mock.calls.length
    fireEvent.keyDown(composer, { key: 'Enter', code: 'Enter' })
    expect(vi.mocked(connectChat).mock.calls).toHaveLength(sendsBefore)
    await act(async () => { stream.onEvent('text', { text: 'Current turn complete' }); stream.onEvent('done', {}) })
    expect(screen.getByTestId('chat-input')).toBe(composer)
    expect(composer).toHaveFocus()
    expect(composer).toHaveValue('Unfinished next instruction')
    view.unmount()
  })

  it.each(['send', 'reconnect', 'bot'] as const)('renders mixed public activity through %s', async mode => {
    vi.mocked(connectChatStream).mockClear()
    vi.mocked(fetchSessionStatus).mockResolvedValueOnce({ sessionId: 'mixed', running: mode === 'reconnect' })
    vi.mocked(fetchSessionHistory).mockResolvedValueOnce({ id: 'mixed', title: 'Mixed', messages: [] })
    const onSend = vi.fn()
    const view = render(<StudioChat theme="dark" initialSessionId="mixed" hideSidebar {...(mode === 'bot' ? { botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend } } : {})} />)
    if (mode === 'send') {
      fireEvent.change(await screen.findByTestId('chat-input'), { target: { value: 'Compare public evidence' } })
      fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
      await waitFor(() => expect(connectChat).toHaveBeenCalled())
    } else {
      await waitFor(() => expect(mode === 'bot' ? connectBotEvents : connectChatStream).toHaveBeenCalled())
    }
    const stream = mode === 'send' ? vi.mocked(connectChat).mock.calls.at(-1)![0] : mode === 'reconnect' ? vi.mocked(connectChatStream).mock.calls.at(-1)![0] : vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('text', { text: 'Mixed public acknowledgement' })
      stream.onEvent('system', { content: 'Public mixed execution boundary' })
      stream.onEvent('tool_call', { id: 'read', name: 'read_file', args: { path: '/workspace/public-receipt.txt' } })
      stream.onEvent('tool_result', { id: 'read', name: 'read_file', result: { content: 'Public receipt' } })
      stream.onEvent('subtask_progress', { event_type: 'delegation_start', batch_id: 'batch', tasks: [{ task_id: 'research', name: 'Mixed research', description: 'Compare evidence' }] })
      stream.onEvent('subtask_progress', { event_type: 'task_start', batch_id: 'batch', task_id: 'research', task_name: 'Mixed research' })
      stream.onEvent('subtask_progress', { event_type: 'task_complete', batch_id: 'batch', task_id: 'research', task_name: 'Mixed research', status: 'success' })
      stream.onEvent('subtask_progress', { event_type: 'plan_snapshot', snapshot: { plan_id: 'plan', goal: 'Mixed verification plan', revision: 1, sequence: 1, document: { lifecycle: 'approved' }, steps: [{ name: 'check', description: 'Verify public receipt', status: 'complete' }] } })
      stream.onEvent('error', { message: 'Public recoverable error' })
      stream.onEvent('text', { text: 'Mixed presentation complete' })
      stream.onEvent('turn_complete', {})
    })
    expect(view.container).toHaveTextContent('Mixed public acknowledgement')
    expect(view.container).toHaveTextContent('Mixed presentation complete')
    expect(screen.getByTestId('tool-activity-block')).toBeVisible()
    expect(screen.getByText('Mixed research')).toBeVisible()
    expect(screen.getByText('Mixed verification plan')).toBeVisible()
    expect(onSend).not.toHaveBeenCalled()
    view.unmount()
  })

  it.each(['send', 'reconnect'] as const)('applies the same ordered presentation events through %s', async mode => {
    vi.mocked(connectChatStream).mockClear()
    vi.mocked(fetchSessionStatus).mockResolvedValueOnce({ sessionId: 'shared-events', running: mode === 'reconnect' })
    vi.mocked(fetchSessionHistory).mockResolvedValueOnce({ id: 'shared-events', title: 'Shared events', messages: [] })
    const view = render(<StudioChat theme="dark" initialSessionId="shared-events" hideSidebar />)
    await waitFor(() => expect(fetchSessionStatus).toHaveBeenCalledWith('shared-events'))
    if (mode === 'send') {
      fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'Exercise shared events' } })
      fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
      await waitFor(() => expect(connectChat).toHaveBeenCalled())
    } else {
      await waitFor(() => expect(connectChatStream).toHaveBeenCalled())
    }
    const stream = mode === 'send'
      ? vi.mocked(connectChat).mock.calls.at(-1)![0]
      : vi.mocked(connectChatStream).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('text', { text: 'First ' })
      stream.onEvent('thinking', { text: 'Temporary progress' })
      stream.onEvent('text', { text: 'segment' })
      stream.onEvent('system', { content: 'Shared system boundary' })
      stream.onEvent('flow_output', { content: 'Saved flow output' })
      stream.onEvent('text', { text: 'Final answer' })
      stream.onEvent('done', {})
      stream.onDone?.()
    })
    expect(screen.getAllByTestId('agent-message-bubble').map(node => node.textContent)).toEqual([
      'First segment', 'Saved flow output', 'Final answer',
    ])
    expect(screen.getByText('Shared system boundary')).toBeVisible()
    expect(screen.queryByText('Temporary progress')).not.toBeInTheDocument()
    expect(screen.queryByTestId('thinking-indicator')).not.toBeInTheDocument()
    expect(screen.queryByText(/stopped responding unexpectedly/)).not.toBeInTheDocument()
    view.unmount()
  })

  it('does not pin a model or dispatch a wizard command while replaying', async () => {
    vi.mocked(connectChatStream).mockClear()
    vi.mocked(patchSessionModel).mockClear()
    vi.mocked(fetchSessionStatus).mockResolvedValueOnce({ sessionId: 'replay-only', running: true })
    vi.mocked(fetchSessionHistory).mockResolvedValueOnce({ id: 'replay-only', title: 'Replay only', messages: [] })
    const view = render(<StudioChat theme="dark" initialSessionId="replay-only" hideSidebar />)
    await waitFor(() => expect(connectChatStream).toHaveBeenCalled())
    const stream = vi.mocked(connectChatStream).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('session', { sessionId: 'replay-only', isNew: true })
      stream.onEvent('drill_redirect', { wizard_system_prompt: 'Never execute replayed instructions', hint: 'replayed' })
      stream.onEvent('fleet_plan_redirect', { wizard_system_prompt: 'Never execute replayed instructions', hint: 'replayed' })
      stream.onEvent('done', {})
    })
    expect(connectChat).not.toHaveBeenCalled()
    expect(patchSessionModel).not.toHaveBeenCalled()
    view.unmount()
  })
  const defaultProps = {
    theme: 'dark',
  }

  beforeEach(() => {
    vi.mocked(connectChat).mockClear()
    vi.mocked(connectChatStream).mockClear()
    vi.mocked(connectBotEvents).mockClear()
    // Bot watches bypass ordinary status/history reads, and active reconnects
    // bypass history. Clear unconsumed one-shot responses between examples so
    // they cannot select another test's transport or restored transcript.
    vi.mocked(fetchSessionStatus).mockReset().mockResolvedValue({ sessionId: '', running: false })
    vi.mocked(fetchSessionHistory).mockReset().mockResolvedValue({ id: '', title: '', messages: [] })
    vi.mocked(fetchKnowledgeDebug).mockReset().mockResolvedValue({ sessionId: 'sess-debug', invocationId: 'inv-1', knowledge: null, tools: null })
    vi.mocked(fetchCacheDiagnostics).mockClear()
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    })
  })

  it.each(['dark', 'light'])('renders durable scheduled progress live and restored without timer failure (%s)', async theme => {
    const progress: import('../../api/botProgress').BotProgressProjection = { schema_version: 1, bot_id: 'bot', run_id: 'run', session_id: 'trace', revision: 5, phase: 'evaluating', active_duration_nanos: 21600000000000 }
    const state: import('../../api/botRunState').BotRunState = { schema_version: 1, bot_id: 'bot', home_session_id: 'bot-home', session_id: 'trace', run_id: 'run', presentation: '', status: 'running', state_sequence: 10, progress }
    const thread = { botId: 'bot', scope: 'team' as const, readOnly: true, onSend: vi.fn(), runStates: [state] }
    const view = render(<StudioChat theme={theme} initialSessionId="bot-home" hideSidebar botThread={thread} />)
    expect(await screen.findByText('Checking progress')).toBeInTheDocument()
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    const phases = await import('../../api/botProgress')
    let sequence = 11
    for (const phase of Object.values(phases.ProgressPhase)) {
      await act(async () => stream.onEvent('bot_run_state', { ...state, state_sequence: sequence++, progress: { ...progress, revision: sequence, phase } }))
      expect(screen.getByTestId('thinking-indicator')).toHaveTextContent(phases.progressLabels[phase])
      expect(screen.queryByText(/timed out|failed/i)).not.toBeInTheDocument()
    }
    await act(async () => stream.onState?.('reconnecting', 'cursor'))
    expect(screen.getByTestId('thinking-indicator')).toHaveTextContent('Checking an unconfirmed action')
    view.unmount()
    render(<StudioChat theme={theme} initialSessionId="bot-home" hideSidebar botThread={thread} />)
    expect(await screen.findByText('Checking progress')).toBeInTheDocument()
    expect(thread.onSend).not.toHaveBeenCalled()
  })

  it('keeps standalone work rows out of Bot chat through live updates, reconnect and restore without remounting the composer', async () => {
    const state: import('../../api/botRunState').BotRunState = { schema_version: 1, bot_id: 'bot', home_session_id: 'bot-home', session_id: 'trace', run_id: 'run', presentation: 'home_interactive', status: 'running', state_sequence: 2, work: { schema_version: 1, protocol: 'agent_led_v1', bot_id: 'bot', run_id: 'run', work_revision: 2, phase: 'working', items: [{ id: 'item', status: 'in_progress', summary: 'Comparing original sources' }] } }
    const thread = { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend: vi.fn(), runStates: [state] }
    const view = render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={thread} />)
    const input = await screen.findByTestId('chat-input')
    fireEvent.change(input, { target: { value: 'Keep this draft' } })
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    const done = { ...state, state_sequence: 4, status: 'succeeded' as const, work: { ...state.work!, work_revision: 3, disposition: 'answered' as const, phase: 'finished' as const } }
    const other = { ...state, run_id: 'other', session_id: 'other-trace', work: { ...state.work!, run_id: 'other' } }
    await act(async () => {
      stream.onEvent('bot_run_state', other as unknown as Record<string, unknown>)
      stream.onEvent('bot_run_state', done as unknown as Record<string, unknown>)
      stream.onEvent('bot_run_state', state as unknown as Record<string, unknown>)
      stream.onState?.('live', 'cursor')
    })
    const assertNoWorkRows = () => {
      expect(screen.queryAllByRole('region', { name: /Work for run/ })).toHaveLength(0)
      expect(screen.queryByText(/^(Last known work · )?(Work in progress|Answer provided)$/)).not.toBeInTheDocument()
      expect(screen.queryByText(/Bot transcript:/)).not.toBeInTheDocument()
    }
    assertNoWorkRows()
    expect(screen.getByText('Working…')).toBeInTheDocument()
    expect(screen.getByTestId('chat-input')).toBe(input)
    expect(input).toHaveValue('Keep this draft')
    await act(async () => stream.onState?.('reconnecting', 'cursor'))
    assertNoWorkRows()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    view.rerender(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ ...thread, runStates: [state] }} />)
    assertNoWorkRows()
    expect(input).toHaveValue('Keep this draft')
    view.unmount()
    render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ ...thread, runStates: [done, other] }} />)
    await screen.findByTestId('chat-input')
    assertNoWorkRows()
    expect(screen.queryByText(/All requirements verified/)).not.toBeInTheDocument()
  })

  it('sends after applied cadence using the authoritative snapshot and never replays a concurrent conflict', async () => {
    const initial: BotSnapshot = { bot: { id: 'bot', scope: 'personal', owner_id: 'owner', name: 'Weather', status: 'active', revision: 5, version: 2, home_session_id: 'bot-home', home: null, created_at: '', updated_at: '' }, read_cursor: { bot_id: 'bot', viewer_id: 'owner', sequence: 0 }, latest_sequence: 0, unread_count: 0, pending_approvals: [] }
    vi.mocked(snapshots.getBotSnapshot).mockResolvedValue(initial)
    vi.mocked(reviews.sendBotMessage).mockReset().mockResolvedValue({ run: { id: 'next' } as never, home_session_id: 'bot-home', user_event_id: 'next-user', history_cursor: 20 })
    render(<BotWorkspace theme="dark" route={{ botId: 'bot', scope: 'personal' }} userId="owner" />)
    const input = await screen.findByTestId('chat-input')
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    const applied = { schema_version: 1 as const, bot_id: 'bot', home_session_id: 'bot-home', operation_id: 'cadence', revision: 2, state_sequence: 17, operation: 'schedule_edit', status: 'applied' as const, decision: 'execute' as const, summary: 'Cadence committed' }
    vi.mocked(snapshots.getBotSnapshot).mockResolvedValue({ ...initial, bot: { ...initial.bot, revision: 6, version: 3 }, operations: [applied] })
    await act(async () => { stream.onEvent('bot_operation', applied); stream.onState?.('live', 'bot-home:17') })
    await screen.findByRole('heading', { name: 'Update schedule · Applied' })
    fireEvent.change(input, { target: { value: 'Pause these updates' } })
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(reviews.sendBotMessage).toHaveBeenCalledTimes(1))
    expect(reviews.sendBotMessage).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 6, version: 3 }), 'Pause these updates', expect.any(String), undefined)
    await waitFor(() => expect(input).toHaveValue(''))
    vi.mocked(reviews.sendBotMessage).mockRejectedValueOnce(new BotAPIError(409, 'Bot changed; reload and review before retrying'))
    fireEvent.change(input, { target: { value: 'Concurrent instruction' } })
    fireEvent.click(screen.getByTestId('send-button'))
    expect(await screen.findByRole('alert', { name: '' })).toHaveTextContent('refresh and review the current saved action before trying again')
    expect(input).toHaveValue('Concurrent instruction')
    await act(async () => { stream.onEvent('bot_operation', applied); stream.onState?.('reconnecting', 'cursor'); stream.onState?.('live', 'cursor') })
    expect(screen.getByRole('alert', { name: '' })).toHaveTextContent('refresh and review the current saved action before trying again')
    expect(input).toHaveValue('Concurrent instruction')
    expect(reviews.sendBotMessage).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it.each(['dark', 'light'])('renders one confirmed schedule message through live, stale snapshot, and reload (%s)', async theme => {
    const operation = { schema_version: 1 as const, bot_id: 'bot', home_session_id: 'bot-home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending' as const, decision: 'execute' as const, presentation: 'internal' as const, summary: 'Change queued.', source_user_event_id: 'human' }
    const applied = { ...operation, status: 'applied' as const, presentation: 'conversation' as const, revision: 3, state_sequence: 6, summary: "I've created the schedule. It runs every minute. Next run: 2026-11-10 22:31 UTC.", verified_schedule: { trigger_id: 'trigger', schedule_id: 'schedule', version: 1, enabled: true, removed: false, next_run: '2026-11-10T22:31:00Z' } }
    const onSend = vi.fn()
    const props = { theme, hideSidebar: true, initialSessionId: 'bot-home' }
    const thread = { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend }
    const view = render(<StudioChat {...props} botThread={{ ...thread, operations: [operation] }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('user_message', { text: 'Keep my entire quoted instruction', eventId: 'human' })
      stream.onEvent('bot_operation', { ...operation, operation_id: 'two', summary: 'Second schedule' })
      stream.onEvent('bot_operation', { ...operation, status: 'applying', revision: 2, state_sequence: 2, summary: 'Applying change.' })
      stream.onEvent('bot_run_state', { schema_version: 1, bot_id: 'bot', home_session_id: 'bot-home', session_id: 'bot-home', run_id: 'run', presentation: 'home_interactive', status: 'succeeded', state_sequence: 5 })
      stream.onState?.('live', 'cursor')
    })
    expect(screen.queryByRole('article', { name: 'Operation result' })).not.toBeInTheDocument()
    expect(screen.queryByTestId('agent-message-bubble')).not.toBeInTheDocument()
    expect(screen.queryByText(/Change queued|Applying change|Second schedule/)).not.toBeInTheDocument()
    expect(screen.getByText('Working…')).toBeInTheDocument()
    await act(async () => {
      stream.onState?.('reconnecting', 'cursor')
      stream.onEvent('bot_operation', applied)
      stream.onEvent('bot_operation', operation)
      stream.onState?.('live', 'new-cursor')
    })
    view.rerender(<StudioChat {...props} botThread={{ ...thread, operations: [operation] }} />)
    expect(screen.getAllByTestId('agent-message-bubble')).toHaveLength(1)
    expect(screen.getByText(applied.summary)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /Create schedule/ })).not.toBeInTheDocument()
    expect(screen.queryByText(/Schedule enabled/)).not.toBeInTheDocument()
    expect(screen.getByText('Working…')).toBeInTheDocument() // independent pending operation two
    expect(screen.getByText('Keep my entire quoted instruction')).toBeInTheDocument()
    view.unmount()
    render(<StudioChat {...props} botThread={{ ...thread, operations: [applied] }} />)
    const restored = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    await act(async () => {
      restored.onEvent('bot_operation', operation)
      restored.onEvent('bot_operation', applied)
      restored.onState?.('live', 'new-cursor')
    })
    expect(screen.getAllByTestId('agent-message-bubble')).toHaveLength(1)
    expect(screen.getByText(applied.summary)).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: 'Operation result' })).not.toBeInTheDocument()
    expect(screen.queryByText('Working…')).not.toBeInTheDocument()
    await act(async () => {
      restored.onEvent('text', { text: 'An unrelated report remains visible.' })
      restored.onEvent('error', { error: 'An unrelated failure remains visible.' })
    })
    expect(screen.getByText('An unrelated report remains visible.')).toBeInTheDocument()
    expect(screen.getByText(/may still be running/)).toBeInTheDocument()
  })

  it.each(['dark', 'light'])('restores schedule confirmation in durable order with normal assistant controls after chunked reload (%s)', async theme => {
    const pending = { schema_version: 1 as const, bot_id: 'bot', home_session_id: 'bot-home', operation_id: 'schedule', revision: 1, state_sequence: 3, operation: 'schedule_create', status: 'pending' as const, decision: 'execute' as const, presentation: 'internal' as const, summary: 'Queued privately', source_user_event_id: 'source' }
    const final = { ...pending, revision: 2, state_sequence: 4, status: 'applied' as const, presentation: 'conversation' as const, summary: 'Schedule confirmed every minute.' }
    const props = { theme, hideSidebar: true, initialSessionId: 'bot-home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend: vi.fn(), operations: [final] } }
    const first = render(<StudioChat {...props} />)
    first.unmount()
    const view = render(<StudioChat {...props} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    const chunk = async (type: string, data: Record<string, unknown>, cursor: string) => act(async () => {
      stream.onEvent(type, data)
      stream.onState?.('live', cursor) // connectivity is not replay completion
    })
    await chunk('user_message', { text: 'Initial greeting', eventId: 'initial' }, 'bot-home:1')
    expect(screen.queryByText(final.summary)).not.toBeInTheDocument()
    await chunk('user_message', { text: 'Schedule my reports', eventId: 'source' }, 'bot-home:2')
    await chunk('bot_operation', pending, 'bot-home:3')
    expect(screen.queryByText(final.summary)).not.toBeInTheDocument()
    await chunk('bot_operation', final, 'bot-home:4') // equal snapshot head must still insert
    for (const text of ['First scheduled report', 'Second scheduled report']) {
      await chunk('text', { text }, text)
      await chunk('turn_complete', {}, text)
    }
    await chunk('bot_operation', pending, 'stale')
    await chunk('bot_operation', final, 'duplicate')
    await act(async () => stream.onReplayComplete?.())
    const bubbles = screen.getAllByTestId('agent-message-bubble')
    expect(bubbles.map(b => b.textContent)).toEqual([final.summary, 'First scheduled report', 'Second scheduled report'])
    expect(screen.getByText('Schedule my reports').compareDocumentPosition(bubbles[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(bubbles[0].getAttribute('style')).toBe(bubbles[1].getAttribute('style'))
    const confirmation = bubbles[0].parentElement!
    expect(within(confirmation).getByTitle('Copy')).toBeEnabled()
    fireEvent.click(within(confirmation).getByTitle('Show raw markdown'))
    expect(within(confirmation).getByTitle('Show formatted')).toBeEnabled()
    expect(confirmation.querySelector('pre')).toHaveTextContent(final.summary)
    view.rerender(<StudioChat {...props} initialSessionId="other-home" botThread={{ ...props.botThread, operations: [] }} />)
    expect(screen.queryByText(final.summary)).not.toBeInTheDocument()
    await chunk('bot_operation', final, 'old-stream')
    expect(screen.queryByText(final.summary)).not.toBeInTheDocument()
  })

  it('waits for replay_complete before placing a snapshot-only confirmation after its source', async () => {
    const operation = { schema_version: 1 as const, bot_id: 'bot', home_session_id: 'bot-home', operation_id: 'only-snapshot', revision: 2, state_sequence: 4, operation: 'schedule_create', status: 'applied' as const, decision: 'execute' as const, presentation: 'conversation' as const, summary: 'Snapshot confirmation', source_user_event_id: 'source' }
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, operations: [operation], onSend: vi.fn() }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    for (const [text, eventId] of [['Initial greeting', 'initial'], ['Schedule reports', 'source']]) {
      await act(async () => { stream.onEvent('user_message', { text, eventId }); stream.onState?.('live', eventId) })
      expect(screen.queryByText(operation.summary)).not.toBeInTheDocument()
    }
    await act(async () => { stream.onEvent('text', { text: 'Later report' }); stream.onEvent('turn_complete', {}); stream.onReplayComplete?.() })
    expect(screen.getAllByTestId('agent-message-bubble').map(b => b.textContent)).toEqual([operation.summary, 'Later report'])
    await act(async () => stream.onReplayComplete?.())
    expect(screen.getAllByText(operation.summary)).toHaveLength(1)
  })

  it('merges snapshot and reconnect operation heads once while pending survives source run completion', async () => {
    const operation = { schema_version: 1 as const, bot_id: 'bot', home_session_id: 'bot-home', operation_id: 'one', revision: 1, state_sequence: 1, operation: 'schedule_create', status: 'pending' as const, decision: 'execute' as const, summary: 'First schedule', source_user_event_id: 'human' }
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, operations: [operation], onSend: vi.fn() }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('user_message', { text: 'Keep my entire quoted instruction', eventId: 'human' })
      stream.onEvent('bot_operation', { ...operation, operation_id: 'two', summary: 'Second schedule' })
      stream.onEvent('bot_run_state', { schema_version: 1, bot_id: 'bot', home_session_id: 'bot-home', session_id: 'bot-home', run_id: 'run', presentation: 'home_interactive', status: 'succeeded', state_sequence: 5 })
      stream.onState?.('live', 'cursor')
      stream.onReplayComplete?.()
    })
    expect(screen.getAllByRole('article', { name: 'Operation result' })).toHaveLength(2)
    expect(screen.getByText('Working…')).toBeInTheDocument()
    await act(async () => {
      stream.onState?.('reconnecting', 'cursor')
      stream.onEvent('bot_operation', { ...operation, status: 'applied', revision: 2, state_sequence: 6 })
      stream.onEvent('bot_operation', operation)
      stream.onState?.('live', 'new-cursor')
    })
    expect(screen.getAllByRole('article', { name: 'Operation result' })).toHaveLength(2)
    expect(screen.getByText('Working…')).toBeInTheDocument()
    expect(screen.getByText('Keep my entire quoted instruction')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Create schedule · Applied' })).toBeInTheDocument()
  })

  it.each(['draft', 'paused', 'active'] as const)('uses accurate %s Bot conversation copy without changing attachment restrictions', async status => {
    const toolFree = status !== 'active'
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', status, readOnly: false, toolFree, onSend: vi.fn() }} />)
    await act(async () => { vi.mocked(connectBotEvents).mock.calls.at(-1)![0].onState?.('live', 'bot-home:1') })
    if (status === 'draft') {
      expect(screen.getByText(/Draft · ready to discuss/)).toBeVisible()
      expect(screen.getByTestId('chat-input')).toHaveAttribute('placeholder', 'Discuss this draft Bot’s setup…')
      expect(screen.queryByText(/paused/i)).not.toBeInTheDocument()
    } else if (status === 'paused') {
      expect(screen.getByText(/Autonomous work paused/)).toBeVisible()
      expect(screen.getByTestId('chat-input')).toHaveAttribute('placeholder', 'Discuss this Bot’s setup while autonomous work is paused…')
    } else {
      expect(screen.queryByText(/ready to discuss|Autonomous work paused/)).not.toBeInTheDocument()
      expect(screen.getByTestId('chat-input')).toHaveAttribute('placeholder', 'Send instructions to request a Bot run…')
    }
    const attach = screen.getByTitle('Attach files (images, PDFs, text files)')
    if (toolFree) expect(attach).toBeDisabled()
    else expect(attach).not.toBeDisabled()
  })

  it.each(['draft', undefined] as const)('does not infer paused state from tool-free restrictions before a home exists (%s)', async status => {
    await act(async () => { render(<StudioChat theme="dark" hideSidebar botThread={{ botId: 'bot', scope: 'personal', status, readOnly: false, toolFree: true, onSend: vi.fn() }} />) })
    expect(screen.queryByText(/paused/i)).not.toBeInTheDocument()
    expect(screen.getByTestId('chat-input')).toHaveAttribute('placeholder', status === 'draft' ? 'Discuss this draft Bot’s setup…' : 'Discuss this Bot’s setup…')
    expect(screen.getByText(/No external tools or scheduled execution. Attachments are unavailable./)).toBeVisible()
    expect(screen.getByTitle('Attach files (images, PDFs, text files)')).toBeDisabled()
  })

  it.each(['personal', 'team'] as const)('keeps %s Bot acknowledgement visible with public tool activity live and on replay', async scope => {
    const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'bot-home', botThread: { botId: 'bot', scope, readOnly: false, onSend: vi.fn(), renderReview: () => <span>Your answer: Local release notes</span> } }
    for (const replay of [false, true]) {
      const view = render(<StudioChat {...props} />)
      const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
      await act(async () => {
        stream.onEvent('bot_review', { kind: 'approval', reviewId: 'a', eventId: 'question' })
        stream.onEvent('user_message', { text: 'Local release notes' })
        stream.onEvent('text', { text: "I'll read the isolated release notes.", invocationId: 'producer' })
        stream.onEvent('tool_call', { name: 'read_file', args: { path: '/isolated/receipt.txt' } })
        if (replay) {
          stream.onEvent('tool_result', { name: 'read_file', result: 'Public release receipt' })
          stream.onEvent('text', { text: 'Title task understood.', invocationId: 'final' })
        }
      })
      const acknowledgement = screen.getByText("I'll read the isolated release notes.")
      expect(acknowledgement).toBeVisible()
      expect(screen.getByTestId('tool-activity-block')).toBeInTheDocument()
      fireEvent.click(screen.getByTestId('tool-activity-block').querySelector('button')!)
      fireEvent.click(screen.getByRole('button', { name: /read_file/ }))
      expect(view.container).toHaveTextContent('/isolated/receipt.txt')
      if (replay) expect(view.container).toHaveTextContent('Public release receipt')
      expect(screen.getByText('Your answer: Local release notes').compareDocumentPosition(acknowledgement) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      if (replay) expect(acknowledgement.compareDocumentPosition(screen.getByText('Title task understood.')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      else expect(screen.queryByText('Title task understood.')).not.toBeInTheDocument()
      view.unmount()
    }
  })

  it('anchors a saved question before its answer and later messages live and on replay', async () => {
    const renderReview = vi.fn(() => <span>Anchored saved question</span>)
    const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'bot-home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend: vi.fn(), questionApprovalIds: ['saved-review'], questionApprovalAnchors: [{ reviewId: 'saved-review', questionId: 'exact-question' }], renderReview } }
    for (const replay of [false, true]) {
      const view = render(<StudioChat {...props} />)
      const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
      act(() => {
        stream.onEvent('tool_call', { name: 'ask_user', args: { prompt: 'Which news?' } })
        stream.onEvent('tool_result', { name: 'ask_user', result: { questionId: 'exact-question' } })
        stream.onEvent('user_message', { text: 'Selected news' })
        stream.onEvent('text', { text: 'New acknowledgement', invocationId: 'ack' })
        stream.onEvent('turn_complete', {})
      })
      const card = screen.getByText('Anchored saved question')
      expect(card.compareDocumentPosition(screen.getByText('Selected news')) & Node.DOCUMENT_POSITION_FOLLOWING, replay ? 'replay' : 'live').toBeTruthy()
      act(() => stream.onEvent('text', { text: 'Later final response', invocationId: 'final' }))
      expect(card.compareDocumentPosition(screen.getByText('Later final response')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(screen.getAllByText('Anchored saved question')).toHaveLength(1)
      view.unmount()
    }
  })

  it('keeps the snapshot question mounted after answering even without a transcript review event', async () => {
    const renderReview = vi.fn((_kind, id) => <span>Your answer: {id}</span>)
    const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'bot-home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend: vi.fn(), pendingApprovalIds: ['saved-answer'], questionApprovalIds: ['saved-answer'], renderReview } }
    const view = render(<StudioChat {...props} />)
    expect(await screen.findByText('Your answer: saved-answer')).toBeVisible()
    view.rerender(<StudioChat {...props} botThread={{ ...props.botThread, pendingApprovalIds: [] }} />)
    expect(screen.getByText('Your answer: saved-answer')).toBeVisible()
    view.unmount()
    render(<StudioChat {...props} botThread={{ ...props.botThread, pendingApprovalIds: [] }} />)
    expect(await screen.findByText('Your answer: saved-answer')).toBeVisible()
  })

  it('renders a durable pending approval when its transcript review event was missed and deduplicates a later event', async () => {
    const renderReview = vi.fn((_kind, id) => <span>Question card: {id}</span>)
    const props = { theme: 'dark', hideSidebar: true, initialSessionId: 'bot-home', botThread: { botId: 'bot', scope: 'personal' as const, readOnly: false, onSend: vi.fn(), pendingApprovalIds: ['approval-from-snapshot'], renderReview } }
    const view = render(<StudioChat {...props} />)
    expect(await screen.findByText('Question card: approval-from-snapshot')).toBeVisible()
    expect(renderReview).toHaveBeenCalledWith('approval', 'approval-from-snapshot')
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    act(() => stream.onEvent('bot_review', { kind: 'approval', reviewId: 'approval-from-snapshot', eventId: 'durable-row' }))
    expect(screen.getAllByText('Question card: approval-from-snapshot')).toHaveLength(1)
    view.rerender(<StudioChat {...props} botThread={{ ...props.botThread, pendingApprovalIds: [] }} />)
    expect(screen.getAllByText('Question card: approval-from-snapshot')).toHaveLength(1)
  })

  it('passes an upgraded question payload through the canonical review without duplicating the row', async () => {
    const renderReview = vi.fn((_kind, _id, question) => <span>{question?.prompt || 'Waiting for question'}</span>)
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn(), renderReview }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    await act(async () => {
      stream.onEvent('bot_review', { kind: 'approval', reviewId: 'a', eventId: 'first' })
      stream.onEvent('bot_review', { kind: 'approval', reviewId: 'a', eventId: 'second', question: { questionId: 'q', kind: 'text', prompt: 'Which AI news?' } })
    })
    expect(screen.getAllByText('Which AI news?')).toHaveLength(1)
    expect(screen.queryByText('Waiting for question')).not.toBeInTheDocument()
    expect(renderReview).toHaveBeenLastCalledWith('approval', 'a', expect.objectContaining({ questionId: 'q' }))
  })

  it.each(['review_id', 'reviewId'])('renders persisted %s reviews once and ignores unknown review kinds', async field => {
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn(), renderReview: (kind, id) => <span>{kind}: {id}</span> }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    act(() => {
      stream.onEvent('bot_review', { kind: 'proposal', [field]: 'review-1', eventId: 'row-1' })
      stream.onEvent('bot_review', { kind: 'proposal', [field]: 'review-1', eventId: 'row-2' })
      stream.onEvent('bot_review', { kind: 'approval', [field]: 'review-2', eventId: 'row-3' })
      stream.onEvent('bot_review', { kind: 'unknown', [field]: 'bad', eventId: 'row-4' })
    })
    expect(screen.getAllByText('proposal: review-1')).toHaveLength(1)
    expect(screen.getByText('approval: review-2')).toBeInTheDocument()
    expect(screen.queryByText('unknown: bad')).not.toBeInTheDocument()
  })

  it('opens sidebar source intake inline without replacing an unfinished intake', async () => {
    render(<StudioChat {...defaultProps} />)
    act(() => window.dispatchEvent(new CustomEvent('astonish:source-intake', { detail: { scope: 'personal' } })))
    fireEvent.change(screen.getByLabelText('Bot name'), { target: { value: 'Unfinished local edit' } })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    act(() => window.dispatchEvent(new CustomEvent('astonish:source-intake', { detail: { scope: 'team', resumeId: 'different-intake' } })))
    expect(screen.getByLabelText('Bot name')).toHaveValue('Unfinished local edit')
  })

  it('locks Bot submission independently of watch state and keeps unknown outcomes local', async () => {
    let reject!: (error: Error) => void
    const onSend = vi.fn(() => new Promise<void>((_, fail) => { reject = fail }))
    render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend }} />)
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
    act(() => stream.onState?.('live', 'bot-home:1'))
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'Keep my text' } })
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
    act(() => stream.onState?.('live', 'bot-home:2'))
    expect(screen.getByTestId('send-button')).toBeDisabled()
    expect(screen.getByTestId('chat-input')).toBeDisabled()
    fireEvent.submit(screen.getByTestId('send-button').closest('form')!)
    expect(onSend).toHaveBeenCalledTimes(1)
    await act(async () => reject(new Error('Outcome unknown')))
    expect(screen.getByRole('alert')).toHaveTextContent('may still be running')
    expect(screen.getByTestId('chat-input')).toHaveValue('Keep my text')
    expect(screen.queryByTestId('user-message')).not.toBeInTheDocument()
  })

  it.each(['draft', 'paused'] as const)('blocks native, paste and dropped attachments in %s while retaining owner text', async status => {
    const onSend = vi.fn().mockResolvedValue(undefined)
    const { container } = render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', status, readOnly: false, toolFree: true, onSend }} />)
    await waitFor(() => expect(connectBotEvents).toHaveBeenCalled())
    act(() => vi.mocked(connectBotEvents).mock.calls.at(-1)![0].onState?.('live', 'bot-home:1'))
    const input = screen.getByTestId('chat-input')
    expect(input).toBeEnabled()
    expect(screen.getByTitle('Attach files (images, PDFs, text files)')).toBeDisabled()
    const file = new File(['hello'], 'blocked.txt', { type: 'text/plain' })
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [file] } })
    fireEvent.paste(input, { clipboardData: { items: [{ kind: 'file', getAsFile: () => file }] } })
    fireEvent.drop(input, { dataTransfer: { files: [file] } })
    expect(screen.queryByText('blocked.txt')).not.toBeInTheDocument()
    expect(onSend).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: 'Discuss my setup' } })
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('Discuss my setup', undefined, []))
  })

  it.each(['native', 'paste'])('submits Bot %s attachments and retains them on failure', async mode => {
    const onSend = vi.fn().mockRejectedValueOnce(new Error('Upload failed')).mockResolvedValue(undefined)
    const { container } = render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend }} />)
    await waitFor(() => expect(connectBotEvents).toHaveBeenCalled())
    act(() => vi.mocked(connectBotEvents).mock.calls.at(-1)![0].onState?.('live', 'bot-home:1'))
    const file = new File(['hello'], 'note.txt', { type: 'text/plain' })
    Object.defineProperty(file, 'arrayBuffer', { value: async () => new Uint8Array([104, 101, 108, 108, 111]).buffer })
    if (mode === 'native') fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [file] } })
    else fireEvent.paste(screen.getByTestId('chat-input'), { clipboardData: { items: [{ kind: 'file', getAsFile: () => file }] } })
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('', [{ filename: 'note.txt', mimeType: 'text/plain', data: 'aGVsbG8=' }], []))
    await waitFor(() => expect(screen.getByText(/may still be running/)).toBeInTheDocument())
    expect(screen.getByText('note.txt')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByText('note.txt')).not.toBeInTheDocument())
  })

  it.each(['live', 'restored'] as const)('shows one explanatory budget-stop report through the actual %s Bot mapper', async mode => {
    const transport = await vi.importActual<typeof import('../../api/botEvents')>('../../api/botEvents')
    vi.mocked(connectBotEvents).mockImplementationOnce(transport.connectBotEvents)
    let source!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start(controller) { source = controller } })
    const fetch = vi.spyOn(teamContext, 'teamFetch').mockResolvedValueOnce(new Response(body))
    const frame = canonicalFrame('text', { text: budgetStop.event.content.parts[0].text, invocationId: budgetStop.event.invocationId }, budgetStop.event.id, 0)
      + canonicalFrame('turn_complete', { invocationId: budgetStop.event.invocationId }, budgetStop.event.id, 1, 1)
    const replayComplete = 'id: bot-home:1\nevent: replay_complete\ndata: {"persisted":true}\n\n'
    const send = async (text: string) => act(async () => { source.enqueue(new TextEncoder().encode(text)) })
    const view = render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn() }} />)
    const assertReport = () => {
      const bubbles = screen.getAllByTestId('agent-message-bubble')
      expect(bubbles).toHaveLength(budgetStop.expected_reports)
      const report = bubbles[0]
      expect(report).toBeVisible()
      expect(report.closest('[data-testid="tool-activity-block"]')).toBeNull()
      // Semantic evidence, not the formatter's exact paragraph wording.
      for (const fact of ['Budget limit reached', 'run budget: 71 tokens', 'Used or reserved: 0 tokens', 'Remaining: 71 tokens', 'Estimated next input: 80 tokens', 'Technical details', 'limit=71', 'accounted=0', 'requested=90']) expect(report).toHaveTextContent(fact)
      expect(report.textContent).toMatch(/increase this Bot’s token budget|shorten the task/i)
      expect(report.textContent).not.toMatch(/\[Error:|Run did not complete; execution requires review/)
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    }
    try {
      if (mode === 'live') await send('id: bot-home:0\nevent: replay_complete\ndata: {"persisted":true}\n\n')
      await send(frame + (mode === 'restored' ? replayComplete : ''))
      assertReport()
      await send(canonicalFrame('turn_complete', { invocationId: budgetStop.event.invocationId }, budgetStop.event.id, 1, 1) + replayComplete)
      assertReport()
      expect(screen.queryByText(/Bot transcript:/)).not.toBeInTheDocument()
    } finally {
      view.unmount()
      fetch.mockRestore()
    }
  })

  it.each(['live', 'restored'] as const)('keeps three scheduled reports outside the scheduling fold using real %s Bot envelopes', async mode => {
    const transport = await vi.importActual<typeof import('../../api/botEvents')>('../../api/botEvents')
    vi.mocked(connectBotEvents).mockImplementationOnce(transport.connectBotEvents)
    let source!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start(controller) { source = controller } })
    const fetch = vi.spyOn(teamContext, 'teamFetch').mockResolvedValueOnce(new Response(body))
    const encoder = new TextEncoder()
    const rows = [
      { type: 'user_message', data: { text: 'Schedule weather', invocationId: 'interactive' } },
      { type: 'cursor', data: {} },
      { type: 'cursor', data: {} },
      { type: 'text', data: { text: 'Schedule requested', invocationId: 'interactive' } },
      ...[1, 2, 3].map(n => ({ type: 'text', data: { text: 'Identical weather summary', invocationId: `bot-report:00000000-0000-4000-8000-00000000000${n}` } })),
    ]
    const frame = (index: number) => canonicalFrame(rows[index].type, rows[index].data, `row-${index}`, index + 1)
      + (rows[index].type === 'text' ? canonicalFrame('turn_complete', { invocationId: rows[index].data.invocationId }, `row-${index}`, index + 1, 1) : '')
    const replayComplete = (cursor: number) => `id: bot-home:${cursor}\nevent: replay_complete\ndata: {"persisted":true}\n\n`
    const assertReports = (count: number) => {
      const reports = screen.getAllByText('Identical weather summary')
      expect(reports).toHaveLength(count)
      for (const report of reports) {
        expect(report).toBeVisible()
        expect(report.closest('[data-testid="tool-activity-block"]')).toBeNull()
      }
      expect(screen.queryByTestId('tool-activity-block')).not.toBeInTheDocument()
      expect(screen.queryByText('schedule_job')).not.toBeInTheDocument()
      if (count === 3) expect(screen.getByText('Schedule requested')).toBeVisible()
    }
    const view = render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn() }} />)
    try {
      await act(async () => {
        source.enqueue(encoder.encode(rows.slice(0, 4).map((_, i) => frame(i)).join('') + (mode === 'live' ? replayComplete(4) : '')))
      })
      for (let i = 4; i < rows.length; i++) {
        await act(async () => { source.enqueue(encoder.encode(frame(i))) })
        assertReports(i - 3)
        // A replayed last-applied row uses its stable cursor, not text equality.
        await act(async () => { source.enqueue(encoder.encode(frame(i))) })
        assertReports(i - 3)
      }
      await act(async () => { source.enqueue(encoder.encode(replayComplete(7))) })
      expect(screen.queryByText(/Bot transcript:/)).not.toBeInTheDocument()
      expect(screen.getAllByTestId('agent-message-bubble')).toHaveLength(4)
    } finally {
      view.unmount()
      fetch.mockRestore()
    }
  })

  it('uses scoped Bot transport, shared transcript, and literal composer text without ordinary Chat commands', async () => {
    vi.mocked(connectChatStream).mockClear()
    vi.mocked(connectBotEvents).mockClear()
    const onSend = vi.fn().mockResolvedValue(undefined)
    const controller = new AbortController()
    vi.mocked(connectBotEvents).mockReturnValue(controller)
    const { unmount } = render(<StudioChat theme="dark" hideSidebar initialSessionId="bot-home" botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend }} />)
    await waitFor(() => expect(connectBotEvents).toHaveBeenCalled())
    expect(connectChatStream).not.toHaveBeenCalled()
    const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0] as BotStreamOptions
    act(() => {
      stream.onState?.('live', 'bot-home:1')
      stream.onEvent('user_message', { text: 'Owner instruction' })
      stream.onEvent('text', { text: 'Bot observation' })
      stream.onEvent('turn_complete', {})
      stream.onEvent('chat_question', { questionId: 'q', kind: 'yesno', prompt: 'Checkpoint question' })
    })
    expect(screen.getByText('Owner instruction')).toBeInTheDocument()
    expect(screen.getByText('Bot observation')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Thinking…')
    expect(screen.queryByText(/Review this checkpoint in the Decision inbox/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Yes' })).not.toBeInTheDocument()
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: '__literal__' } })
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('__literal__', undefined, []))
    expect(connectChat).not.toHaveBeenCalled()
    unmount()
    expect(controller.signal.aborted).toBe(true)
  })

  it('uses the same translucent dark background for transcript and activity agent bubbles', async () => {
    vi.mocked(fetchSessionHistory).mockResolvedValue({
      id: 'agent-bubbles', title: 'Agent bubbles', messages: [
        { type: 'agent', content: 'Writing the first section.' },
        { type: 'tool_call', toolName: 'shell_command', toolArgs: { command: 'printf first' } },
        { type: 'tool_result', toolName: 'shell_command', toolResult: { exit_code: 0 } },
        { type: 'agent', content: 'Finished.' },
      ],
    } as never)

    render(<StudioChat {...defaultProps} initialSessionId="agent-bubbles" />)

    const activity = await screen.findByTestId('tool-activity-block')
    fireEvent.click(activity.querySelector('button')!)
    const bubbles = await screen.findAllByTestId('agent-message-bubble')
    expect(bubbles).toHaveLength(2)
    for (const bubble of bubbles) {
      expect(bubble).toHaveStyle({ backgroundColor: 'rgba(255, 255, 255, 0.08)' })
    }
  })

  it('renders the sidebar with Chats title', async () => {
    render(<StudioChat {...defaultProps} />)
    expect(screen.getByText('Chats')).toBeInTheDocument()
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders the new conversation button', async () => {
    render(<StudioChat {...defaultProps} />)
    // The "+" button for new conversation
    const buttons = screen.getAllByRole('button')
    // At minimum there should be new chat button, fleet button, and collapse toggle
    expect(buttons.length).toBeGreaterThanOrEqual(2)
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders the message input area', async () => {
    render(<StudioChat {...defaultProps} />)
    const textarea = screen.getByTestId('chat-input')
    expect(textarea).toBeInTheDocument()
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('shows the HomePage when there are no messages', async () => {
    render(<StudioChat {...defaultProps} />)
    expect(screen.getByTestId('home-page')).toBeInTheDocument()
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders the send button', async () => {
    render(<StudioChat {...defaultProps} />)
    // The send button is present in the input area
    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThan(0)
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('renders the search input in sidebar', async () => {
    render(<StudioChat {...defaultProps} />)
    const searchInput = screen.getByPlaceholderText(/search/i)
    expect(searchInput).toBeInTheDocument()
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })
  })

  it('shows the browser-local Debug toggle only to platform superadmins and sends debug', async () => {
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    })
    const { rerender } = render(<StudioChat {...defaultProps} isPlatformMode platformRole="member" />)
    expect(screen.queryByRole('switch', { name: 'Debug cache diagnostics' })).not.toBeInTheDocument()

    rerender(<StudioChat {...defaultProps} isPlatformMode platformRole="superadmin" />)
    fireEvent.click(screen.getByRole('switch', { name: 'Debug cache diagnostics' }))
    expect(storage.get('astonish-studio-cache-debug')).toBe('true')
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'hello' } })
    fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })

    await waitFor(() => expect(connectChat).toHaveBeenCalledWith(expect.objectContaining({ debug: true })))
  })

  it('opens diagnostics inline for an assistant turn', async () => {
    vi.mocked(fetchSessionHistory).mockResolvedValue({
      id: 'sess-debug',
      title: 'Debug',
      messages: [{ role: 'assistant', type: 'agent', content: 'Answer', invocationId: 'inv-1' }],
    })
    vi.mocked(fetchCacheDiagnostics).mockResolvedValue({
      sessionId: 'sess-debug',
      invocationId: 'inv-1',
      rounds: [{
        invocationId: 'inv-1', kind: 'provider', stage: 'provider_dispatch', status: 'succeeded', call: 1, stream: true, provider: 'google', model: 'gemini',
        captureLevel: 'canonical-adk', inputHash: 'request-hash', stablePrefixElements: 3,
        stablePrefixBytes: 512, startedAt: '2026-08-29T00:00:00Z', timeToFirstResponse: 1,
        duration: 2, responseCount: 1, payloadOriginalBytes: 10, payloadCapturedBytes: 10,
        payloadTruncated: false, binaryElisions: 0, payload: { cachedTokens: 120 },
        usage: { reported: true, cacheReported: true, promptTokens: 150, cachedTokens: 120, candidateTokens: 10, thoughtTokens: 0, toolUseTokens: 0, totalTokens: 160 },
      }],
    })

    render(<StudioChat {...defaultProps} initialSessionId="sess-debug" isPlatformMode platformRole="superadmin" />)
    const button = await screen.findByRole('button', { name: 'Turn debug (Memory and Cache) for this assistant turn' })
    fireEvent.click(button)

    // Debug panel now opens with tabs — click the Cache tab to see cache diagnostics
    const cacheTab = await screen.findByRole('button', { name: 'Cache' })
    fireEvent.click(cacheTab)

    expect(await screen.findByText('Provider round 1')).toBeInTheDocument()
    expect(screen.getByText('Provider cache hit')).toBeInTheDocument()
    expect(screen.getByText('3 elements · 512 bytes')).toBeInTheDocument()
    expect(fetchCacheDiagnostics).toHaveBeenCalledWith('sess-debug', 'inv-1')
  })

  it('opens Memory for each live assistant invocation after two sends', async () => {
    localStorage.setItem('astonish-studio-cache-debug', 'true')
    render(<StudioChat {...defaultProps} isPlatformMode platformRole="superadmin" />)
    for (const invocationId of ['live-1', 'live-2']) {
      fireEvent.change(screen.getByTestId('chat-input'), { target: { value: `Question ${invocationId}` } })
      fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
      const options = vi.mocked(connectChat).mock.calls.at(-1)![0]
      expect(options.debug).toBe(true)
      await act(async () => {
        options.onEvent('session', { sessionId: 'live-session', isNew: invocationId === 'live-1' })
        options.onEvent('text', { text: `Answer ${invocationId}`, invocationId })
        options.onEvent('done', { status: 'complete', invocationId })
        options.onDone?.()
      })
    }
    const buttons = screen.getAllByRole('button', { name: 'Turn debug (Memory and Cache) for this assistant turn' })
    expect(buttons).toHaveLength(2)
    fireEvent.click(buttons[0])
    await waitFor(() => expect(fetchKnowledgeDebug).toHaveBeenLastCalledWith('live-session', 'live-1'))
    fireEvent.click(buttons[0])
    fireEvent.click(buttons[1])
    await waitFor(() => expect(fetchKnowledgeDebug).toHaveBeenLastCalledWith('live-session', 'live-2'))
    expect(fetchCacheDiagnostics).not.toHaveBeenCalled()
  })

  it('restores two turns into the default Memory tab with matching invocation while capture is off', async () => {
    vi.mocked(fetchSessionHistory).mockResolvedValue({
      id: 'sess-debug', title: 'Restored', messages: [
        { role: 'user', type: 'user', content: 'First question' },
        { role: 'assistant', type: 'agent', content: 'First answer', invocationId: 'inv-1' },
        { role: 'user', type: 'user', content: 'Second question' },
        { role: 'assistant', type: 'agent', content: 'Second answer', invocationId: 'inv-2' },
      ],
    })
    vi.mocked(fetchKnowledgeDebug).mockImplementation(async (sessionId, invocationId) => ({
      sessionId, invocationId, tools: null,
      knowledge: {
        version: 1, invocation_id: invocationId, dispatched: true, capture_status: 'captured',
        type: 'knowledge', query: '', bm25_query_len: 0, results: [], result_count: 1, estimated_tokens: 10,
        snapshot: { text: `Memory for ${invocationId}\n  exact text`, snippets: [], capture_bytes: 30, original_bytes: 30, truncated: false, redacted: false },
      },
    }))
    render(<StudioChat {...defaultProps} initialSessionId="sess-debug" isPlatformMode platformRole="superadmin" />)
    const buttons = await screen.findAllByRole('button', { name: 'Turn debug (Memory and Cache) for this assistant turn' })
    expect(buttons).toHaveLength(2)
    expect(buttons[0]).toHaveAttribute('title', 'Turn debug (Memory and Cache)')
    expect(screen.getByRole('switch', { name: 'Debug cache diagnostics' })).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(buttons[0])
    expect((await screen.findByTestId('injected-memory-text')).textContent).toBe('Memory for inv-1\n  exact text')
    expect(fetchKnowledgeDebug).toHaveBeenLastCalledWith('sess-debug', 'inv-1')
    expect(fetchCacheDiagnostics).not.toHaveBeenCalled()
    fireEvent.click(buttons[0])
    fireEvent.click(buttons[1])
    expect((await screen.findByTestId('injected-memory-text')).textContent).toBe('Memory for inv-2\n  exact text')
    expect(fetchKnowledgeDebug).toHaveBeenLastCalledWith('sess-debug', 'inv-2')
    expect(fetchCacheDiagnostics).not.toHaveBeenCalled()
  })

  it.each([
    { isPlatformMode: true, platformRole: 'member' },
    { isPlatformMode: false, platformRole: 'superadmin' },
  ])('hides turn inspection outside platform superadmin access: %j', async props => {
    localStorage.setItem('astonish-studio-cache-debug', 'true')
    vi.mocked(fetchSessionHistory).mockResolvedValue({
      id: 'sess-debug', title: 'Restored', messages: [{ role: 'assistant', type: 'agent', content: 'Restricted answer', invocationId: 'inv-1' }],
    })
    render(<StudioChat {...defaultProps} {...props} initialSessionId="sess-debug" />)
    await screen.findByText('Restricted answer')
    expect(screen.queryByRole('button', { name: /Turn debug/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('switch', { name: 'Debug cache diagnostics' })).not.toBeInTheDocument()
    expect(fetchKnowledgeDebug).not.toHaveBeenCalled()
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'hello' } })
    fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
    await waitFor(() => expect(connectChat).toHaveBeenCalledWith(expect.objectContaining({ debug: false })))
  })

  it('sends debug false when the superadmin capture toggle is off', async () => {
    render(<StudioChat {...defaultProps} isPlatformMode platformRole="superadmin" />)
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'hello' } })
    fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
    await waitFor(() => expect(connectChat).toHaveBeenCalledWith(expect.objectContaining({ debug: false })))
  })

  it('retains copy, raw view and invocation debug inside restored activity', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    vi.mocked(fetchSessionHistory).mockResolvedValue({
      id: 'nested-debug', title: 'Nested', messages: [
        { type: 'agent', content: '**Intermediate**', invocationId: 'nested-inv' },
        { type: 'tool_call', toolName: 'read_file', toolArgs: {} },
        { type: 'tool_result', toolName: 'read_file', toolResult: 'ok' },
        { type: 'agent', content: 'Final outside', invocationId: 'final-inv' },
      ],
    } as never)
    render(<StudioChat {...defaultProps} initialSessionId="nested-debug" isPlatformMode platformRole="superadmin" />)
    await screen.findByText('Final outside')
    const block = screen.getByTestId('tool-activity-block')
    fireEvent.click(block.querySelector('button')!)
    fireEvent.click(block.querySelector('[title="Copy"]')!)
    expect(writeText).toHaveBeenCalledWith('**Intermediate**')
    fireEvent.click(block.querySelector('[title="Show raw markdown"]')!)
    expect(block.querySelector('pre')).toHaveTextContent('**Intermediate**')
    fireEvent.click(block.querySelector('[title="Turn debug (Memory and Cache)"]')!)
    await waitFor(() => expect(fetchKnowledgeDebug).toHaveBeenLastCalledWith('nested-debug', 'nested-inv'))
    expect(screen.getByText('Final outside').closest('[data-testid="tool-activity-block"]')).toBeNull()
  })

  it.each(['error', 'eof', 'stop', 'session'] as const)('preserves received text and clears progress on %s', async ending => {
    render(<StudioChat {...defaultProps} />)
    fireEvent.change(screen.getByTestId('chat-input'), { target: { value: 'start' } })
    fireEvent.keyDown(screen.getByTestId('chat-input'), { key: 'Enter', code: 'Enter' })
    const options = vi.mocked(connectChat).mock.calls.at(-1)![0]
    await act(async () => {
      options.onEvent('text', { text: 'Received partial', invocationId: 'partial-inv' })
      options.onEvent('thinking', { text: 'Temporary generation status' })
    })
    expect(screen.getByTestId('thinking-indicator')).toHaveTextContent('Temporary generation status')
    await act(async () => {
      if (ending === 'error') options.onError?.(new Error('Disconnected'))
      if (ending === 'eof') options.onDone?.()
      if (ending === 'stop') fireEvent.click(screen.getByTitle('Stop'))
      if (ending === 'session') options.onEvent('new_session', { sessionId: 'next-session' })
    })
    expect(screen.queryByText('Temporary generation status')).not.toBeInTheDocument()
    if (ending !== 'session') expect(screen.getByText('Received partial')).toBeInTheDocument()
    if (ending === 'stop') {
      await act(async () => { options.onEvent('text', { text: 'Stale text' }); options.onDone?.() })
      expect(screen.queryByText(/Stale text/)).not.toBeInTheDocument()
    }
  })

  describe('chat_question rendering', () => {
    beforeEach(() => {
      vi.mocked(fetchSessionHistory).mockReset()
    })

    it('renders the explanatory assistant message outside tools before a question dialog', async () => {
      vi.mocked(fetchSessionHistory).mockResolvedValue({
        messages: [
          { type: 'tool_call', toolName: 'delegate_tasks', toolArgs: {} },
          { type: 'tool_result', toolName: 'delegate_tasks', toolResult: 'research complete' },
          { type: 'agent', content: 'The treatment uses four acts and an open-ended conclusion.' },
          { type: 'tool_call', toolName: 'ask_user', toolArgs: { question: 'How should we proceed?' } },
          { type: 'tool_result', toolName: 'ask_user', toolResult: { pending: true } },
          {
            type: 'chat_question',
            questionId: 'q-context',
            kind: 'select',
            prompt: 'How should we proceed?',
            options: [{ id: 'build', label: 'Approve and build the script' }],
          },
        ],
      } as never)

      await act(async () => {
        render(<StudioChat {...defaultProps} initialSessionId="sess-context" />)
        await new Promise(resolve => setTimeout(resolve, 0))
      })

      const explanation = screen.getByText('The treatment uses four acts and an open-ended conclusion.')
      expect(explanation.closest('[data-testid="tool-activity-block"]')).toBeNull()
      expect(screen.getByText('How should we proceed?')).toBeVisible()
      expect(screen.getByText('Approve and build the script')).toBeVisible()
      expect(screen.getByTestId('tool-activity-block')).toBeInTheDocument()
    })

    it('renders a yesno chat question with Yes/No controls', async () => {
      vi.mocked(fetchSessionHistory).mockResolvedValue({
        messages: [
          {
            type: 'chat_question',
            questionId: 'q1',
            kind: 'yesno',
            prompt: 'Ship it?',
            options: [],
          },
        ],
      } as never)

      await act(async () => {
        render(<StudioChat {...defaultProps} initialSessionId="sess-1" />)
        await new Promise(resolve => setTimeout(resolve, 0))
      })

      expect(screen.getByText('Ship it?')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Yes' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'No' })).toBeInTheDocument()
    })

    it('renders a select chat question with option tiles', async () => {
      vi.mocked(fetchSessionHistory).mockResolvedValue({
        messages: [
          {
            type: 'chat_question',
            questionId: 'q2',
            kind: 'select',
            prompt: 'Pick a layout',
            options: [
              { id: 'a', label: 'Layout A', description: 'First' },
              { id: 'b', label: 'Layout B' },
            ],
          },
        ],
      } as never)

      await act(async () => {
        render(<StudioChat {...defaultProps} initialSessionId="sess-2" />)
        await new Promise(resolve => setTimeout(resolve, 0))
      })

      expect(screen.getByText('Pick a layout')).toBeInTheDocument()
      expect(screen.getByText('Layout A')).toBeInTheDocument()
      expect(screen.getByText('Layout B')).toBeInTheDocument()
      expect(screen.getAllByRole('radio')).toHaveLength(2)
    })

    it('collapses an answered question to the prompt after reload', async () => {
      vi.mocked(fetchSessionHistory).mockResolvedValue({
        messages: [
          {
            type: 'chat_question',
            questionId: 'q1',
            kind: 'select',
            prompt: "Who's the audience?",
            options: [
              { id: 'exec', label: 'Executives' },
              { id: 'eng', label: 'Engineers' },
            ],
          },
          { type: 'user', content: 'Executives' },
        ],
      } as never)

      await act(async () => {
        render(<StudioChat {...defaultProps} initialSessionId="sess-answered" />)
        await new Promise(resolve => setTimeout(resolve, 0))
      })

      await waitFor(() => {
        expect(screen.getByTestId('ask-user-answered')).toHaveTextContent("Who's the audience?")
      })
      expect(screen.getByText('Executives')).toBeInTheDocument()
      expect(screen.queryByRole('radio')).not.toBeInTheDocument()
      expect(screen.queryByText(/You chose/i)).not.toBeInTheDocument()
    })

    it('keeps a later unanswered question interactive after reload', async () => {
      vi.mocked(fetchSessionHistory).mockResolvedValue({
        messages: [
          {
            type: 'chat_question',
            questionId: 'q1',
            kind: 'select',
            prompt: "Who's the audience?",
            options: [
              { id: 'exec', label: 'Executives' },
              { id: 'eng', label: 'Engineers' },
            ],
          },
          { type: 'user', content: 'Executives' },
          {
            type: 'chat_question',
            questionId: 'q2',
            kind: 'select',
            prompt: 'How long should it be?',
            options: [
              { id: 'short', label: '5–8 slides' },
              { id: 'long', label: '12–15 slides' },
            ],
          },
        ],
      } as never)

      await act(async () => {
        render(<StudioChat {...defaultProps} initialSessionId="sess-mixed" />)
        await new Promise(resolve => setTimeout(resolve, 0))
      })

      await waitFor(() => {
        expect(screen.getByTestId('ask-user-answered')).toHaveTextContent("Who's the audience?")
      })
      expect(screen.getByText('How long should it be?')).toBeInTheDocument()
      expect(screen.getByText('5–8 slides')).toBeInTheDocument()
      expect(screen.getAllByRole('radio')).toHaveLength(2)
      expect(screen.queryByText('Engineers')).not.toBeInTheDocument()
    })
  })
})


describe('Bot employee failure boundary', () => {
  it.each(['personal', 'team'] as const)('retains useful committed partial findings live and on replay in %s scope', async scope => {
    for (const readOnly of [false, true]) {
      const send = vi.fn()
      const view = render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ botId: 'bot', scope, readOnly, status: 'paused', onSend: send }} />)
      await waitFor(() => expect(connectBotEvents).toHaveBeenCalled())
      const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
      await act(async () => {
        stream.onEvent('user_message', { text: 'Compare the evidence', eventId: 'owner', _projection: { event_id: 'bot-home/owner/0', session_id: 'bot-home', cursor: 'bot-home:1' } })
        stream.onEvent('text', { text: 'Verified finding: revenue rose 5%. Limitation: the second source could not be read.', invocationId: 'bot-report:partial', eventId: 'partial', _projection: { event_id: 'bot-home/partial/0', session_id: 'bot-home', cursor: 'bot-home:2' } })
        stream.onEvent('turn_complete', {})
        stream.onState?.('reconnecting', 'partial-cursor')
      })
      expect(screen.getByTestId('message-area')).toHaveTextContent('Verified finding: revenue rose 5%.')
      expect(screen.getByTestId('message-area')).toHaveTextContent('second source could not be read')
      await act(async () => { stream.onState?.('live', 'partial-cursor'); stream.onReplayComplete?.() })
      expect(document.querySelectorAll('.chat-bubble-user')).toHaveLength(1)
      expect(screen.getAllByText(/Verified finding:/)).toHaveLength(1)
      expect(send).not.toHaveBeenCalled()
      view.unmount()
    }
  })
  it.each(['error', 'error_info'])('renders %s safely on fresh mount and reload', async kind => {
    for (let replay = 0; replay < 2; replay++) {
      const view = render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn() }} />)
      await screen.findByTestId('chat-input')
      const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
      await act(async () => { stream.onEvent?.(kind, { error: 'secret /api/private HTTP 500', title: 'secret', reason: 'stack trace', originalError: 'secret' }) })
      expect(screen.getByTestId('agent-message-bubble')).toHaveTextContent('may still be running')
      expect(screen.queryByText(/secret|stack trace|HTTP 500/)).not.toBeInTheDocument()
      view.unmount()
    }
  })
  it('retains unsent input and safe access feedback after a rejected send', async () => {
    render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn().mockRejectedValue({ status: 403, message: 'secret /api/private' }) }} />)
    const input = await screen.findByTestId('chat-input')
    fireEvent.change(input, { target: { value: 'Do this' } })
    fireEvent.keyDown(input, { key: 'Enter', code: 'Enter' })
    expect(await screen.findByRole('alert')).toHaveTextContent('can’t access')
    expect(input).toHaveValue('Do this')
    expect(screen.queryByText(/secret/)).not.toBeInTheDocument()
  })
})


it('shows employee uncertainty during Bot reconnect and clears it only when live', async () => {
  render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ botId: 'bot', scope: 'personal', readOnly: false, onSend: vi.fn() }} />)
  await screen.findByTestId('chat-input')
  const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
  await act(async () => { stream.onState?.('reconnecting', 'saved-cursor') })
  expect(screen.getByRole('alert')).toHaveTextContent('may still be running')
  await act(async () => { stream.onError?.(Object.assign(new Error('HTTP secret endpoint'), { status: 403 })) })
  expect(screen.getByRole('alert')).toHaveTextContent('can’t access')
  expect(screen.queryByText(/secret endpoint/)).not.toBeInTheDocument()
  await act(async () => { stream.onState?.('live', 'saved-cursor') })
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('renders canonical file deliveries exactly once in server order on replay', async () => {
  render(<StudioChat theme="dark" initialSessionId="bot-home" hideSidebar botThread={{ botId: 'bot', scope: 'personal', readOnly: true, onSend: vi.fn() }} />)
  await waitFor(() => expect(connectBotEvents).toHaveBeenCalled())
  const stream = vi.mocked(connectBotEvents).mock.calls.at(-1)![0]
  const publication = { schema_version: 1, bot_id: 'bot', run_id: 'r', home_id: 'h', home_session_id: 'bot-home', report_sequence: 1, event_id: 'publication', summary: 'Your files are ready.', artifacts: ['z', 'a'].map(id => ({ id, home_id: 'h', relative_path: id + '.bin', sha256: 'a'.repeat(64), size_bytes: 18000000, media_type: 'application/octet-stream', title: '', preview: 'download' })) }
  await act(async () => { stream.onEvent('bot_file_publication', publication); stream.onEvent('bot_file_publication', publication); stream.onReplayComplete?.() })
  expect(screen.getAllByText('Your files are ready.')).toHaveLength(1)
  expect([...document.querySelectorAll('[data-artifact-id]')].map(e => e.getAttribute('data-artifact-id'))).toEqual(['z', 'a'])
})
