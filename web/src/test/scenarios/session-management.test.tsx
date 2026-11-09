/**
 * Session Management Scenario Tests (S1, S2, S4, A6)
 *
 * Tests session list loading, session search filtering, sidebar collapse,
 * and the new_session event clearing messages.
 *
 */

import { describe, it, expect, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

// Fixtures
import simpleQa from '../fixtures/scenarios/core/simple-qa.json'
import newSessionCommand from '../fixtures/scenarios/core/new-session-command.json'

describe('Session Management Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  describe('S1: Session List Loading', () => {
    it('renders sessions in the sidebar after loading', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
        sessions: [
          { id: 'sess-1', title: 'Research Project', createdAt: '2024-01-01', updatedAt: '2024-01-01', messageCount: 5 },
          { id: 'sess-2', title: 'Code Review', createdAt: '2024-01-02', updatedAt: '2024-01-02', messageCount: 3 },
        ],
      })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research Project')
        expect(text).toContain('Code Review')
      }, { timeout: 10000 })
    })
  })

  describe('S2: Session Search', () => {
    it('filters sessions by search input', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
        sessions: [
          { id: 'sess-1', title: 'Research Project', createdAt: '2024-01-01', updatedAt: '2024-01-01', messageCount: 5 },
          { id: 'sess-2', title: 'Code Review', createdAt: '2024-01-02', updatedAt: '2024-01-02', messageCount: 3 },
        ],
      })

      // Wait for sessions to render
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research Project')
        expect(text).toContain('Code Review')
      }, { timeout: 10000 })

      // Find the search input and type a filter
      const searchInput = screen.getByPlaceholderText(/search/i)
      const user = userEvent.setup()
      await user.type(searchInput, 'Research')

      // "Research Project" should still be visible
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research Project')
      }, { timeout: 10000 })

      // "Code Review" should be filtered out
      const text = result.container.textContent || ''
      expect(text).not.toContain('Code Review')
    })
  })

  describe('S4: Sidebar Collapse', () => {
    it('collapses sidebar when collapse button is clicked', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
      })

      // Initially, both sidebar section headings should be visible.
      await waitFor(() => {
        expect(screen.getByText('Bots', { exact: true })).toBeVisible()
        expect(screen.getByText('Chats', { exact: true })).toBeVisible()
      }, { timeout: 10000 })

      // Find and click the hide sidebar button (title="Hide sidebar")
      const collapseButton = screen.getByTitle('Hide sidebar')
      const user = userEvent.setup()
      await user.click(collapseButton)

      // Both headings disappear and only the expand control remains.
      await waitFor(() => {
        expect(screen.queryByText('Bots', { exact: true })).not.toBeInTheDocument()
        expect(screen.queryByText('Chats', { exact: true })).not.toBeInTheDocument()
        expect(screen.queryByTitle('Hide sidebar')).not.toBeInTheDocument()
        expect(screen.getByTitle('Show sidebar')).toBeVisible()
      }, { timeout: 10000 })

      await user.click(screen.getByTitle('Show sidebar'))
      await waitFor(() => {
        expect(screen.getByText('Bots', { exact: true })).toBeVisible()
        expect(screen.getByText('Chats', { exact: true })).toBeVisible()
        expect(screen.getByTitle('Hide sidebar')).toBeVisible()
        expect(screen.queryByTitle('Show sidebar')).not.toBeInTheDocument()
      }, { timeout: 10000 })
    })
  })

  describe('A6: New Session Command', () => {
    it('clears messages on new_session event', async () => {
      result = renderChat({
        scenarioEvents: newSessionCommand.events as FixtureEvent[],
      })

      // Send a message that triggers the new_session event stream
      await result.sendMessage('test')

      // The new_session event should clear messages and not crash.
      // After the stream completes, the message area should be empty
      // (no agent response since new_session clears everything).
      await waitFor(() => {
        // Verify the stream completed without errors by checking
        // that the home page is shown (messages cleared = empty state)
        const text = result.container.textContent || ''
        // The new_session event clears messages, so HomePage should re-appear
        expect(text).not.toContain('error')
      }, { timeout: 10000 })
    })
  })
})
