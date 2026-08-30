import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, waitFor, fireEvent } from '@testing-library/react'
import StudioChat from '../StudioChat'
import { connectChat, fetchCacheDiagnostics, fetchSessionHistory } from '../../api/studioChat'

// Mock all API modules
vi.mock('../../api/studioChat', () => ({
  fetchSessions: vi.fn().mockResolvedValue([]),
  fetchSessionHistory: vi.fn().mockResolvedValue([]),
  deleteSession: vi.fn().mockResolvedValue({}),
  connectChat: vi.fn().mockReturnValue(new AbortController()),
  fetchCacheDiagnostics: vi.fn().mockResolvedValue({ sessionId: 'sess-debug', assistantTurn: 1, rounds: [] }),
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
        invocationId: 'inv-1', call: 1, stream: true, provider: 'google', model: 'gemini',
        captureLevel: 'canonical-adk', inputHash: 'request-hash', stablePrefixElements: 3,
        stablePrefixBytes: 512, startedAt: '2026-08-29T00:00:00Z', timeToFirstResponse: 1,
        duration: 2, responseCount: 1, payloadOriginalBytes: 10, payloadCapturedBytes: 10,
        payloadTruncated: false, binaryElisions: 0, payload: { cachedTokens: 120 },
        usage: { reported: true, cacheReported: true, promptTokens: 150, cachedTokens: 120, cacheWriteTokens: 0, candidateTokens: 10, thoughtTokens: 0, toolUseTokens: 0, totalTokens: 160 },
      }],
    })

    render(<StudioChat {...defaultProps} initialSessionId="sess-debug" isPlatformMode platformRole="superadmin" />)
    const button = await screen.findByRole('button', { name: 'Cache diagnostics for this assistant turn' })
    fireEvent.click(button)

    expect(await screen.findByText('Model round 1')).toBeInTheDocument()
    expect(screen.getByText('Provider cache hit')).toBeInTheDocument()
    expect(screen.getByText('3 elements · 512 bytes')).toBeInTheDocument()
    expect(fetchCacheDiagnostics).toHaveBeenCalledWith('sess-debug', 'inv-1')
  })

  describe('chat_question rendering', () => {
    beforeEach(() => {
      vi.mocked(fetchSessionHistory).mockReset()
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
