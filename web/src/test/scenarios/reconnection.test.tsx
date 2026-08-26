/**
 * Reconnection Scenario Tests (P1.9)
 *
 * Tests the reconnection flow: when a user selects a session that has an
 * active background runner, the frontend reconnects via GET /api/studio/sessions/:id/stream
 * instead of loading static history. The SSE events from the reconnect stream should
 * render normally — text, tool calls, results, etc.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { waitFor } from '@testing-library/react'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

import './scenarioSetup'

import reconnectSlides from '../fixtures/scenarios/core/reconnect-slides.json'
import reconnectStream from '../fixtures/scenarios/core/reconnect-stream.json'

describe('Reconnection Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  describe('P1.9: Reconnect to active session', () => {
    it('renders events from reconnect stream when session is running', async () => {
      result = renderChat({
        reconnectEvents: reconnectStream.events as FixtureEvent[],
        initialSessionId: 'sess-reconnect',
        sessions: [{ id: 'sess-reconnect', title: 'Active Session' }],
        sessionStatus: { sessionId: 'sess-reconnect', running: true, eventCount: 5 },
      })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Go 1.24 was released')
      }, { timeout: 10000 })

      await waitFor(() => {
        expect(result.container.querySelector('[data-testid="tool-activity-block"]')).toBeTruthy()
        const text = result.container.textContent || ''
        expect(text).toMatch(/1 search|Searching|web_search/i)
      }, { timeout: 10000 })
    })

    it('transitions to idle state after reconnect stream completes', async () => {
      result = renderChat({
        reconnectEvents: reconnectStream.events as FixtureEvent[],
        initialSessionId: 'sess-reconnect',
        sessions: [{ id: 'sess-reconnect', title: 'Active Session' }],
        sessionStatus: { sessionId: 'sess-reconnect', running: true, eventCount: 5 },
      })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Go 1.24 was released')
      }, { timeout: 10000 })

      await waitFor(() => {
        const ta = result.container.querySelector('[data-testid="chat-input"]') as HTMLTextAreaElement
        if (ta) {
          const placeholder = ta.getAttribute('placeholder') || ''
          expect(placeholder.toLowerCase()).not.toContain('responding')
        }
      }, { timeout: 10000 })
    })

    it('folds reconnect slide updates within a turn and retains the next turn snapshot', async () => {
      const boundaryIndex = reconnectSlides.events.findIndex((event, index) => (
        index > 0 && event.type === 'docs_update' && event.data.slideIndex === 6
      ))

      result = renderChat({
        reconnectEvents: reconnectSlides.events.slice(0, boundaryIndex) as FixtureEvent[],
        scenarioEvents: reconnectSlides.events.slice(boundaryIndex) as FixtureEvent[],
        initialSessionId: 'sess-reconnect-slides',
        sessions: [{ id: 'sess-reconnect-slides', title: 'Quarterly Business Review' }],
        sessionStatus: { sessionId: 'sess-reconnect-slides', running: true, eventCount: boundaryIndex },
      })

      await waitFor(() => {
        const launchers = result.container.querySelectorAll('[data-testid="harness-placeholder"][data-harness-kind="slides"]')
        expect(launchers).toHaveLength(1)
        expect(launchers[0]).toHaveTextContent('Quarterly Business Review')
        expect(launchers[0]).toHaveTextContent('4 / 8')
      }, { timeout: 10000 })

      await result.sendMessage('Add the priorities and risks slides')

      await waitFor(() => {
        // Same deck across two turns → still ONE in-chat launcher, now showing
        // the latest progress. The deck lives in the right-hand harness panel.
        const launchers = result.container.querySelectorAll('[data-testid="harness-placeholder"][data-harness-kind="slides"]')
        expect(launchers).toHaveLength(1)
        expect(launchers[0]).toHaveTextContent('6 / 8')
      }, { timeout: 10000 })
    })
  })

  describe('Restored session history', () => {
    it('renders a single slides launcher for a deck edited across turns (latest only)', async () => {
      const docsUpdate = (slideIndex: number, errors: number, warnings: number, native: number, unsupported: number) => ({
        type: 'docs_update',
        docsUpdate: {
          type: 'slides',
          deckSlug: 'quarterly-review',
          action: 'slide_written',
          slideIndex,
          totalSlides: 8,
          deckTitle: 'Quarterly Business Review',
          schemaVersion: 1,
          validation: { errors, warnings },
          pptxCapability: { native, vector: 2, raster: 0, unsupported },
        },
      })

      result = renderChat({
        initialSessionId: 'sess-restored-slides',
        sessions: [{ id: 'sess-restored-slides', title: 'Quarterly Business Review' }],
        sessionStatus: { sessionId: 'sess-restored-slides', running: false },
        sessionHistory: {
          id: 'sess-restored-slides',
          title: 'Quarterly Business Review',
          messages: [
            { type: 'user', content: 'Create the quarterly review' },
            docsUpdate(2, 0, 2, 5, 0),
            docsUpdate(4, 0, 1, 9, 0),
            { type: 'user', content: 'Add the priorities and risks slides' },
            docsUpdate(6, 1, 0, 12, 1),
          ],
        },
      })

      await waitFor(() => {
        expect(result.container).toHaveTextContent('Create the quarterly review')
        // docs_update messages coalesce per-turn (so two survive: 4/8 from turn
        // one, 6/8 from turn two), but the in-chat launcher renders ONCE per deck
        // — only the latest. The deck itself lives in the right-hand harness.
        const launchers = result.container.querySelectorAll('[data-testid="harness-placeholder"][data-harness-kind="slides"]')
        expect(launchers).toHaveLength(1)
        expect(launchers[0]).toHaveTextContent('6 / 8')
      }, { timeout: 10000 })
    })
  })
})
