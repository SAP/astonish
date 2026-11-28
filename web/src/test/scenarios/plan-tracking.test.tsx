/**
 * Plan Tracking Scenario Tests (E1-E7, F1-F3)
 *
 * Tests the announce_plan flow, plan step status transitions
 * (pending → running → complete/failed), and the PlanPanel + TodoPanel
 *
 * rendering.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { act, waitFor, screen } from '@testing-library/react'
import revisionFixture from '../fixtures/scenarios/planning/plan-revisions.json'
import type { PlanSnapshot } from '../../components/chat/chatTypes'
// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

// Fixtures
import planWithDelegation from '../fixtures/scenarios/planning/plan-with-delegation.json'

describe('Plan Tracking Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  it.each(['live', 'reconnect', 'history'])('renders one authoritative revision from %s without replay rollback', async mode => {
    const snapshot = revisionFixture.events[1].data.snapshot as PlanSnapshot
    const next: PlanSnapshot = { ...snapshot, ...snapshot.pending_revision!.candidate, revision: 2, sequence: 11, pending_revision: undefined, document: { ...snapshot.pending_revision!.candidate.document, lifecycle: 'executing' } }
    const snapshots = [snapshot, next, snapshot, next]
    const events: FixtureEvent[] = snapshots.map(snapshot => ({ type: 'subtask_progress', data: { event_type: 'plan_snapshot', snapshot } }))
    events.push({ type: 'done', data: {} })
    result = renderChat(mode === 'live' ? { scenarioEvents: [revisionFixture.events[0] as FixtureEvent, ...events] } : mode === 'reconnect' ? { initialSessionId: 'revision-session', sessionStatus: { running: true }, reconnectEvents: events } : { initialSessionId: 'revision-session', sessionHistory: { id: 'revision-session', title: 'Revisions', messages: snapshots.map(snapshot => ({ type: 'plan', snapshot })) } })
    if (mode === 'live') await result.sendMessage('Build')
    await waitFor(() => expect(screen.getByText('Verify browser behavior')).toBeTruthy())
    expect(screen.queryByTestId('plan-revision-review')).toBeNull()
    expect(screen.getAllByText('Execution Plan')).toHaveLength(1)
    expect(screen.getByText('Verify browser behavior')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Approve plan|Approve revision|Reject revision|Request changes/ })).toBeNull()
    expect(screen.queryByText('Revision 2')).toBeNull()
  })

  it.each(['live', 'reconnect'])('updates the same DOM card without scrolling or focusing from %s', async mode => {
    let stream!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start(controller) { stream = controller } })
    const snapshot = { ...revisionFixture.events[1].data.snapshot as PlanSnapshot, pending_revision: undefined }
    const route = mode === 'live' ? '/api/studio/chat' : '/api/studio/sessions/revision-session/stream'
    result = renderChat({ initialSessionId: 'revision-session', sessionStatus: { running: mode === 'reconnect' }, mockConfig: { customHandlers: { [route]: () => new Response(body, { headers: { 'Content-Type': 'text/event-stream' } }) } } })
    if (mode === 'live') {
      await waitFor(() => expect(screen.getByTestId('chat-input')).not.toBeDisabled())
      await result.user.type(screen.getByTestId('chat-input'), 'continue{Enter}')
    }
    const emit = async (snapshot: PlanSnapshot) => {
      await act(async () => { stream.enqueue(new TextEncoder().encode(`event: subtask_progress\ndata: ${JSON.stringify({ event_type: 'plan_snapshot', snapshot })}\n\n`)) })
    }
    await emit(snapshot)
    const goal = await screen.findByText(snapshot.goal)
    const card = goal.parentElement!.parentElement!
    const area = screen.getByTestId('message-area')
    Object.defineProperty(area, 'scrollHeight', { configurable: true, value: 1500 })
    area.scrollTop = 37
    area.tabIndex = -1
    area.focus()
    await emit({ ...snapshot, goal: 'Quietly updated goal', revision: 2, sequence: 11 })
    await screen.findByText('Quietly updated goal')
    expect(screen.getByText('Quietly updated goal').parentElement!.parentElement).toBe(card)
    expect(area.scrollTop).toBe(37)
    expect(area).toHaveFocus()
    expect(screen.getAllByText('Execution Plan')).toHaveLength(1)
    expect(screen.queryByTestId('plan-revision-review')).toBeNull()
    await act(async () => { stream.enqueue(new TextEncoder().encode('event: done\ndata: {}\n\n')); stream.close() })
  })

  describe('E1 + F1: Plan Announced with Delegation', () => {
    it('renders plan panel with goal and step names', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze the market')

      // Plan goal should appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research and analyze market competitors')
      }, { timeout: 10000 })

      // Step descriptions should appear in the plan panel
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Find top competitors')
      }, { timeout: 10000 })
    })

    it('shows plan steps from the announced plan', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze the market')

      // Step descriptions should appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Find top competitors')
        expect(text).toContain('Compare features and pricing')
      }, { timeout: 10000 })
    })

    it('shows delegation task names alongside plan', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze the market')

      // Delegation task names should appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research competitors')
        expect(text).toContain('Feature comparison')
      }, { timeout: 10000 })
    })

    it('shows final agent text after plan completes', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze the market')

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('analysis is complete')
      }, { timeout: 10000 })
    })
  })

  describe('E7: TodoPanel', () => {
    it('shows Todo button with plan step progress after plan events', async () => {
      result = renderChat({
        scenarioEvents: planWithDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Analyze the market')

      // Wait for plan to be rendered
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research and analyze market competitors')
      }, { timeout: 10000 })

      // The plan footer shows step progress like "3/3 steps"
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('3/3')
      }, { timeout: 10000 })
    })
  })
})
