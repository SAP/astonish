import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, waitFor, fireEvent } from '@testing-library/react'
import StudioChat from '../StudioChat'
import { connectChat, fetchCacheDiagnostics, fetchKnowledgeDebug, fetchSessionHistory } from '../../api/studioChat'

// Mock all API modules
vi.mock('../../api/studioChat', () => ({
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
vi.mock('../chat/chatTypes', () => ({
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
  const defaultProps = {
    theme: 'dark',
  }

  beforeEach(() => {
    vi.mocked(connectChat).mockClear()
    vi.mocked(fetchKnowledgeDebug).mockReset().mockResolvedValue({ sessionId: 'sess-debug', invocationId: 'inv-1', knowledge: null, tools: null })
    vi.mocked(fetchCacheDiagnostics).mockClear()
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    })
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

  it('renders the sidebar with Conversations title', async () => {
    render(<StudioChat {...defaultProps} />)
    expect(screen.getByText('Conversations')).toBeInTheDocument()
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
