import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import StudioChat from '../StudioChat'
import { fetchSessionHistory } from '../../api/studioChat'

// Mock all API modules
vi.mock('../../api/studioChat', () => ({
  fetchSessions: vi.fn().mockResolvedValue([]),
  fetchSessionHistory: vi.fn().mockResolvedValue([]),
  deleteSession: vi.fn().mockResolvedValue({}),
  connectChat: vi.fn().mockReturnValue(new AbortController()),
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
  })
})
