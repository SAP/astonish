/**
 * Downloads & Artifacts Scenario Tests (L1, L1b, L4)
 *
 * Tests artifact card rendering after write_file tool, agent text after
 * artifact creation, and long agent responses following artifact events.
 *
 */

import { describe, it, expect, afterEach } from 'vitest'
import { waitFor } from '@testing-library/react'

// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'

import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

// Fixtures
import artifactCreated from '../fixtures/scenarios/downloads/artifact-created.json'
import resultWithArtifacts from '../fixtures/scenarios/downloads/result-with-artifacts.json'
import slidesUpdate from '../fixtures/scenarios/downloads/slides-update.json'

describe('Downloads & Artifacts Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  describe('L1: Artifact Event', () => {
    it('renders artifact card with filename', async () => {
      result = renderChat({
        scenarioEvents: artifactCreated.events as FixtureEvent[],
      })

      await result.sendMessage('Create a report')

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('report.md')
      }, { timeout: 10000 })
    })
  })

  describe('L1b: Artifact with tool context', () => {
    it('shows agent text after artifact creation', async () => {
      result = renderChat({
        scenarioEvents: artifactCreated.events as FixtureEvent[],
      })

      await result.sendMessage('Create a report')

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain("I've created the report")
      }, { timeout: 10000 })
    })
  })

  describe('Slides docs_update', () => {
    it('preserves each turn snapshot when the same deck is updated again', async () => {
      result = renderChat({
        scenarioEvents: slidesUpdate.events as FixtureEvent[][],
        mockConfig: {
          customHandlers: {
            '/api/docs/slides/cloud-migration/present?scope=personal': () => new Response('<html>deck</html>', {
              status: 200,
              headers: { 'Content-Type': 'text/html' },
            }),
          },
        },
      })

      await result.sendMessage('Create a migration deck')

      await waitFor(() => {
        const cards = result.container.querySelectorAll('[data-testid="slides-card"]')
        expect(cards).toHaveLength(1)
        expect(cards[0]).toHaveTextContent('Cloud Migration Plan')
        expect(cards[0]).toHaveTextContent('2 / 4')
        expect(cards[0]).toHaveTextContent('0 errors, 1 warnings')
        expect(cards[0]).toHaveTextContent('11 native, 0 unsupported')
        expect(cards[0].querySelector('[role="alert"]')).not.toBeInTheDocument()
      }, { timeout: 10000 })

      await result.sendMessage('Add migration waves and a delivery roadmap')

      await waitFor(() => {
        const cards = result.container.querySelectorAll('[data-testid="slides-card"]')
        expect(cards).toHaveLength(2)

        expect(cards[0]).toHaveTextContent('Cloud Migration Plan')
        expect(cards[0]).toHaveTextContent('2 / 4')
        expect(cards[0]).toHaveTextContent('0 errors, 1 warnings')
        expect(cards[0]).toHaveTextContent('11 native, 0 unsupported')

        expect(cards[1]).toHaveTextContent('Cloud Migration Plan')
        expect(cards[1]).toHaveTextContent('4 / 4')
        expect(cards[1]).toHaveTextContent('0 errors, 2 warnings')
        expect(cards[1]).toHaveTextContent('22 native, 0 unsupported')
        expect(cards[1].querySelectorAll('button')).toHaveLength(4)
        expect(cards[1]).toHaveTextContent('Present')
        expect(cards[1]).toHaveTextContent('PPTX')
        expect(cards[1]).toHaveTextContent('PDF')
        expect(cards[1]).toHaveTextContent('HTML')
      }, { timeout: 10000 })
    })
  })

  describe('L4: Result with Artifacts (long text)', () => {
    it('renders long agent response after artifact', async () => {
      result = renderChat({
        scenarioEvents: resultWithArtifacts.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze')

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('comprehensive analysis')
      }, { timeout: 10000 })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('analysis.md')
      }, { timeout: 10000 })
    })
  })
})
