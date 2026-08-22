/**
 * Chat Renderer — wraps StudioChat with mocked APIs and SSE scenario.
 *
 * This is the main entry point for scenario tests. It:
 * 1. Sets up the fetch mock with a given scenario
 * 2. Renders StudioChat with required props
 * 3. Provides utilities for sending messages and waiting for events
 *
 * IMPORTANT: Tests using this helper must NOT use vi.mock() for studioChat
 * or fleetChat APIs — the mockFetch intercepts fetch() at the network level,
 * so the real API client code runs (including SSE parsing).
 */

import { render, screen, waitFor, act, type RenderResult } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import StudioChat from '../../components/StudioChat'
import { setupMockFetch, type MockFetchConfig } from './mockFetch'
import type { FixtureEvent } from './sseSimulator'

export interface RenderChatOptions {
  /** SSE events to replay when user sends a message.
   *  Single array = same events for every POST.
   *  Array of arrays = queue: first POST gets [0], second gets [1], etc.
   */
  scenarioEvents?: FixtureEvent[] | FixtureEvent[][]
  /** SSE events for reconnection stream */
  reconnectEvents?: FixtureEvent[]
  /** Pre-existing sessions in the sidebar */
  sessions?: MockFetchConfig['sessions']
  /** Session history returned for a non-running session */
  sessionHistory?: MockFetchConfig['sessionHistory']
  /** Session status (whether an active runner exists) */
  sessionStatus?: MockFetchConfig['sessionStatus']
  /** Initial session ID prop */
  initialSessionId?: string | null
  /** Additional mock config overrides */
  mockConfig?: Partial<MockFetchConfig>
  /** Theme prop (defaults to 'dark') */
  theme?: string
}

export interface RenderChatResult extends RenderResult {
  user: ReturnType<typeof userEvent.setup>
  cleanup: () => void

  /**
   * Send a message by typing into the textarea and submitting.
   * Waits for the streaming to complete (done event processed).
   */
  sendMessage: (text: string) => Promise<void>

  /**
   * Wait for the SSE stream to complete (isStreaming becomes false).
   * Uses a heuristic: waits for the stop button to disappear.
   */
  waitForStreamComplete: () => Promise<void>

  /**
   * Wait for a specific text to appear in the document.
   */
  waitForText: (text: string, options?: { timeout?: number }) => Promise<HTMLElement>

  /**
   * Get all rendered message containers.
   */
  getMessageArea: () => HTMLElement | null
}

/**
 * Render StudioChat with mocked APIs.
 *
 * The approach:
 * - We do NOT vi.mock() the API modules. Instead, we intercept globalThis.fetch
 *   so the real connectChat() code runs, including the SSE ReadableStream parsing.
 * - Sub-components that would be hard to render (modals, heavy external deps) are
 *   not mocked — we let them render to test the full integration.
 * - react-markdown and remark-gfm ARE mocked at the vi.mock level since they're
 *   ESM modules that don't play well with jsdom. Tests that need markdown rendering
 *   should use a separate setup.
 */
export function renderChat(options: RenderChatOptions = {}): RenderChatResult {
  const {
    scenarioEvents,
    reconnectEvents,
    sessions = [],
    sessionHistory,
    sessionStatus,
    initialSessionId = null,
    mockConfig = {},
    theme = 'dark',
  } = options

  // Set up fetch mock
  const cleanupFetch = setupMockFetch({
    scenarioEvents,
    reconnectEvents,
    sessions,
    sessionHistory,
    sessionStatus,
    ...mockConfig,
  })

  const user = userEvent.setup()

  // Render StudioChat
  const renderResult = render(
    <StudioChat
      theme={theme}
      initialSessionId={initialSessionId}
      onSessionChange={() => {}}
      onPendingChatMessageConsumed={() => {}}
    />
  )

  const cleanup = () => {
    renderResult.unmount()
    cleanupFetch()
  }

  const sendMessage = async (text: string) => {
    // Find the editable textarea (the input may still be disabled while reconnect settles).
    const textarea = await waitFor(() => {
      const candidate = renderResult.container.querySelector('textarea[data-testid="chat-input"]') as HTMLTextAreaElement | null
        || screen.getByPlaceholderText(/type.*message|ask.*anything/i) as HTMLTextAreaElement
      if (candidate.disabled || candidate.readOnly) throw new Error('Chat input is not editable yet')
      return candidate
    }, { timeout: 10000 })

    // Type the message
    await user.clear(textarea)
    await user.type(textarea, text)

    // Submit via Enter key
    await user.keyboard('{Enter}')

    // Wait briefly for the stream to start, then wait for it to complete.
    // We detect completion by looking for the user's message in the DOM
    // (which appears immediately) and then waiting for the placeholder to
    // change back from "Agent is responding..." to the idle text.
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 50))
    })

    await waitFor(
      () => {
        // Stream has completed when the textarea placeholder is back to idle
        const ta = renderResult.container.querySelector('[data-testid="chat-input"]') as HTMLElement
          || screen.getByPlaceholderText(/type.*message|ask.*anything|agent is responding/i)
        const placeholder = ta.getAttribute('placeholder') || ''
        if (placeholder.toLowerCase().includes('responding')) {
          throw new Error('Still streaming')
        }
      },
      { timeout: 10000 }
    )

    // Extra tick for React to settle state updates
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 100))
    })
  }

  const waitForStreamComplete = async () => {
    await waitFor(
      () => {
        const ta = renderResult.container.querySelector('[data-testid="chat-input"]') as HTMLElement
          || screen.getByPlaceholderText(/type.*message|ask.*anything|agent is responding/i)
        const placeholder = ta.getAttribute('placeholder') || ''
        if (placeholder.toLowerCase().includes('responding')) {
          throw new Error('Still streaming')
        }
      },
      { timeout: 10000 }
    )
    // Let React settle
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 50))
    })
  }

  const waitForText = async (text: string, opts?: { timeout?: number }) => {
    return waitFor(
      () => screen.getByText(text, { exact: false }),
      { timeout: opts?.timeout || 5000 }
    )
  }

  const getMessageArea = (): HTMLElement | null => {
    return (renderResult.container.querySelector('[data-testid="message-area"]') ||
           renderResult.container.querySelector('[class*="messages"]') ||
           renderResult.container.querySelector('[style*="overflow"]')) as HTMLElement | null
  }

  return {
    ...renderResult,
    user,
    cleanup,
    sendMessage,
    waitForStreamComplete,
    waitForText,
    getMessageArea,
  }
}
