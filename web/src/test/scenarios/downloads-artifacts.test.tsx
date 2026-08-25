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
    it('renders a single compact launcher per deck and auto-opens the harness panel', async () => {
      result = renderChat({
        scenarioEvents: slidesUpdate.events as FixtureEvent[][],
        mockConfig: {
          customHandlers: {
            '/api/docs/slides/cloud-migration?scope=personal': () => new Response(
              JSON.stringify({ deck: { id: 'd1', slug: 'cloud-migration', title: 'Cloud Migration Plan', schemaVersion: 1 }, slides: [] }),
              { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
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
        // Compact launcher is the shared HarnessPlaceholder, not the old SlidesCard.
        expect(cards).toHaveLength(0)
        const launchers = result.container.querySelectorAll('[data-testid="harness-placeholder"][data-harness-kind="slides"]')
        expect(launchers).toHaveLength(1)
        expect(launchers[0]).toHaveTextContent('Cloud Migration Plan')
        expect(launchers[0]).toHaveTextContent('2 / 4')
        // Compact launcher: single Open action, no Present/PPTX/PDF/HTML buttons.
        expect(launchers[0]).toHaveTextContent('Open')
        expect(launchers[0]).not.toHaveTextContent('PPTX')
        // The deck auto-opens the shared harness panel (App parity).
        const panel = result.container.querySelector('[data-testid="harness-panel"][data-harness-kind="slides"]')
        expect(panel).toBeInTheDocument()
      }, { timeout: 10000 })

      await result.sendMessage('Add migration waves and a delivery roadmap')

      await waitFor(() => {
        // Same deck across two turns → still ONE compact launcher, updated to the
        // latest progress (earlier per-turn docs_update renders nothing). The
        // deck itself is shown live in the right-hand harness panel.
        const launchers = result.container.querySelectorAll('[data-testid="harness-placeholder"][data-harness-kind="slides"]')
        expect(launchers).toHaveLength(1)
        expect(launchers[0]).toHaveTextContent('Cloud Migration Plan')
        expect(launchers[0]).toHaveTextContent('4 / 4')
        expect(launchers[0]).not.toHaveTextContent('PPTX')
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
