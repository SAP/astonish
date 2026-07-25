/**
 * Slash Command Scenario Tests (R1-R3)
 *
 * Tests the slash command popup: activation when "/" is typed,
 * filtering as the user types, and command execution via the popup.
 *
 */

import { describe, it, expect, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

import simpleQa from '../fixtures/scenarios/core/simple-qa.json'
import systemMessage from '../fixtures/scenarios/misc/system-message.json'

describe('Slash Command Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  describe('R1: Slash Popup Activation', () => {
    it('shows slash command popup when / is typed', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
      })

      // Find the textarea and type "/"
      const textarea = screen.getByTestId('chat-input')
      await result.user.type(textarea, '/')

      // The popup should show slash command entries
      // Slash commands include: /help, /status, /new, /compact, /distill, /fleet, etc.
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('/help')
      }, { timeout: 10000 })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('/status')
      }, { timeout: 10000 })
    })

    it('shows command descriptions in the popup', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
      })

      const textarea = screen.getByTestId('chat-input')
      await result.user.type(textarea, '/')

      // Descriptions should appear alongside commands
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Show available commands')
      }, { timeout: 10000 })
    })
  })

  describe('R2: Slash Popup Filtering', () => {
    it('filters commands as user types after /', async () => {
      result = renderChat({
        scenarioEvents: simpleQa.events as FixtureEvent[],
      })

      const textarea = screen.getByTestId('chat-input')

      // Type "/he" to filter to /help
      await result.user.type(textarea, '/he')

      await waitFor(() => {
        const text = result.container.textContent || ''
        // /help should still be visible since it starts with "he"
        expect(text).toContain('/help')
      }, { timeout: 10000 })

      // /status should NOT be visible since it doesn't start with "he"
      await waitFor(() => {
        const popup = result.container.querySelector('.absolute.bottom-full')
        if (popup) {
          expect(popup.textContent || '').not.toContain('/status')
        }
      }, { timeout: 10000 })
    })
  })

  describe('R3: /help Response', () => {
    it('renders system message for /help command', async () => {
      result = renderChat({
        scenarioEvents: systemMessage.events as FixtureEvent[],
      })

      await result.sendMessage('/help')

      // The system message should contain "Available commands"
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Available commands')
      }, { timeout: 10000 })

      // The "System" label should be visible
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('System')
      }, { timeout: 10000 })
    })
  })
})
