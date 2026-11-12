/**
 * Plan Interaction Scenario Tests (E6, E2-E3, D5)
 *
 * Tests plan step status transitions, partial completion display,
 * step descriptions, and task retry flow.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { waitFor, screen } from '@testing-library/react'
import revisionFixture from '../fixtures/scenarios/planning/plan-revisions.json'
import type { PlanSnapshot } from '../../components/chat/chatTypes'

// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'

import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

// Fixtures
import planStepTransitions from '../fixtures/scenarios/planning/plan-step-transitions.json'
import taskRetry from '../fixtures/scenarios/delegation/task-retry.json'

describe('Plan Interaction Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  it.each(['live', 'reconnect', 'history'])('ignores legacy pending review from %s and keeps Normal usable', async mode => {
    const snapshot = revisionFixture.events[1].data.snapshot as PlanSnapshot
    const requests: Record<string, unknown>[] = []
    const continuation: FixtureEvent[] = [{ type: 'text', data: { text: 'Normal continuation ready' } }, { type: 'done', data: {} }]
    result = renderChat({
      ...(mode === 'live' ? { scenarioEvents: [revisionFixture.events as FixtureEvent[], continuation] }
        : mode === 'reconnect' ? { initialSessionId: 'revision-session', sessionStatus: { running: true }, reconnectEvents: revisionFixture.events as FixtureEvent[], scenarioEvents: continuation }
        : { initialSessionId: 'revision-session', sessionHistory: { id: 'revision-session', title: 'Legacy plan', messages: [{ type: 'plan', snapshot }] }, scenarioEvents: continuation }),
      mockConfig: { onChatRequest: body => requests.push(body) },
    })
    if (mode === 'live') await result.sendMessage('Build it')
    await screen.findByText('Ship revision support')
    expect(screen.queryByTestId('plan-revision-review')).toBeNull()
    expect(screen.queryByRole('button', { name: /Approve plan|Approve revision|Reject revision|Request changes/ })).toBeNull()
    expect(screen.queryByText('Verify browser behavior')).toBeNull()
    await waitFor(() => expect(screen.getByTestId('chat-input')).not.toBeDisabled())
    await result.sendMessage('continue normally')
    await screen.findByText('Normal continuation ready')
    expect(requests.at(-1)).toMatchObject({ message: 'continue normally' })
    expect(requests.at(-1)).not.toHaveProperty('planRevisionDecision')
  })

  it('sends initial approval as explicit execution metadata without prose', async () => {
    const snapshot: PlanSnapshot = { ...revisionFixture.events[1].data.snapshot as PlanSnapshot, pending_revision: undefined, document: {}, steps: [{ name: 'build', description: 'Build feature' }] }
    const requests: Record<string, unknown>[] = []
    result = renderChat({ scenarioEvents: [[revisionFixture.events[0] as FixtureEvent, { type: 'subtask_progress', data: { event_type: 'plan_snapshot', snapshot } }, { type: 'done', data: {} }], [{ type: 'subtask_progress', data: { event_type: 'plan_snapshot', snapshot: { ...snapshot, sequence: 11, document: { lifecycle: 'executing' } } } }, { type: 'done', data: {} }]], mockConfig: { onChatRequest: body => requests.push(body) } })
    await result.sendMessage('Plan it')
    await result.user.click(screen.getByRole('button', { name: 'Approve plan' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Approve plan' })).toBeNull())
    expect(requests[1]).toMatchObject({ message: '', sessionId: 'revision-session', approvedPlanExecution: true })
    expect(requests[1]).not.toHaveProperty('planRevisionDecision')
  })

  describe('E6: Plan Partial Completion', () => {
    it('shows Partial status when a step fails', async () => {
      result = renderChat({
        scenarioEvents: planStepTransitions.events as FixtureEvent[],
      })

      await result.sendMessage('Build it')

      // Wait for the plan goal to appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Build feature X')
      }, { timeout: 10000 })

      // Should show "Partial" status badge (because one step failed)
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Partial')
      }, { timeout: 10000 })

      // Should show "2/3" (2 complete out of 3 steps)
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('2/3')
      }, { timeout: 10000 })
    })
  })

  describe('E2-E3: Plan Step Status', () => {
    it('shows step descriptions in the plan panel', async () => {
      result = renderChat({
        scenarioEvents: planStepTransitions.events as FixtureEvent[],
      })

      await result.sendMessage('Build it')

      // Wait for step descriptions to appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Design the architecture')
      }, { timeout: 10000 })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Write the code')
      }, { timeout: 10000 })

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Run tests')
      }, { timeout: 10000 })
    })
  })

  describe('D5: Task Retry', () => {
    it('shows task completing after retry', async () => {
      result = renderChat({
        scenarioEvents: taskRetry.events as FixtureEvent[],
      })

      await result.sendMessage('Call API')

      // Wait for task name to appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('API Call')
      }, { timeout: 10000 })

      // Should show the final duration after retry
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('2.0s')
      }, { timeout: 10000 })

      // Should show the final text about successful retry
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('API call succeeded after retry')
      }, { timeout: 10000 })
    })
  })
})
