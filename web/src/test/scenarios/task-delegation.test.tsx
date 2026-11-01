/**
 * Task Delegation Scenario Tests (D1-D7)
 *
 * Tests the delegate_tasks flow: delegation start, task lifecycle
 * (start → tool calls → text → complete/failed), retry, and the
 * TaskPlanPanel rendering.
 *
 */

import { describe, it, expect, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
// Shared mocks (react-markdown, remark-gfm, HomePage, FleetStartDialog, FleetTemplatePicker, MermaidBlock)
import './scenarioSetup'
import { renderChat } from '../helpers/renderChat'
import type { RenderChatResult } from '../helpers/renderChat'
import type { FixtureEvent } from '../helpers/sseSimulator'

// Fixtures
import simpleDelegation from '../fixtures/scenarios/delegation/simple-delegation.json'
import multiTaskParallel from '../fixtures/scenarios/delegation/multi-task-parallel.json'
import taskFailure from '../fixtures/scenarios/delegation/task-failure.json'

import taskRetry from '../fixtures/scenarios/delegation/task-retry.json'
import { reduceDelegationProgress, normalizeDelegationHistory, deriveDelegationTasks, groupChronologicalActivity } from '../../components/chat/toolActivity'
import type { ChatMsg, SubTaskEvent } from '../../components/chat/chatTypes'

describe('Task Delegation Scenarios', () => {
  let result: RenderChatResult

  afterEach(() => {
    if (result) result.cleanup()
  })

  it.each(['done', 'error'])('interrupts unfinished delegation on %s', async terminal => {
    const start = simpleDelegation.events.filter(e => e.type === 'session' || (e.type === 'subtask_progress' && ['delegation_start', 'task_start'].includes(e.data.event_type || ''))) as FixtureEvent[]
    result = renderChat({ scenarioEvents: [...start, { type: terminal, data: terminal === 'error' ? { error: 'stream failed' } : { done: true } }] })
    await result.sendMessage('start work')
    await waitFor(() => expect(screen.getByText('Delegation interrupted')).toBeInTheDocument())
    expect(screen.getByText('0/1 task')).toBeInTheDocument()
  })

  it('reuses names across batches with identical live/reconnect/history counts and order', () => {
    const records = taskRetry.events.filter(e => e.type === 'subtask_progress').map(e => ({ ...e.data, type: e.data.event_type })) as SubTaskEvent[]
    const second = records.map(e => ({ ...e, batch_id: 'retry-second', child_session_id: e.child_session_id ? `second-${e.child_session_id}` : undefined }))
    const all = [...records, ...second]
    const live = all.reduce(reduceDelegationProgress, [] as ChatMsg[])
    const replay = all.reduce(reduceDelegationProgress, live)
    const history = normalizeDelegationHistory([...live, ...live])
    expect(replay).toEqual(live)
    expect(history).toEqual(live)
    expect(live).toHaveLength(2)
    for (const panel of live) {
      if (panel.type !== 'subtask_execution') throw new Error('Expected dedicated delegation')
      const [task] = deriveDelegationTasks(panel)
      expect(task.name).toBe('API Call')
      expect(task.attempts.map(a => a.status)).toEqual(['failed', 'complete'])
      expect(task.attempts.map(a => a.child_session_id)).toEqual(panel.batch_id === 'retry' ? ['api-1', 'api-2'] : ['second-api-1', 'second-api-2'])
    }
    const grouped = groupChronologicalActivity([{ type: 'tool_call', toolName: 'read_file', toolArgs: {} }, { type: 'tool_result', toolName: 'read_file', toolResult: 'ok' }, ...live])
    expect(grouped.filter(g => g.kind === 'passthrough')).toHaveLength(2)
    expect(grouped.flatMap(g => g.kind === 'activity' ? g.steps.map(s => s.toolName) : [])).toEqual(['read_file'])
  })

  it.each([simpleDelegation, multiTaskParallel, taskFailure, taskRetry])('fixture $name carries stable identities', fixture => {
    for (const event of fixture.events as FixtureEvent[]) {
      if (event.type !== 'subtask_progress') continue
      expect(event.data.batch_id).toBeTruthy()
      expect(event.data.sequence).toBeTypeOf('number')
      if (event.data.task_name) {
        expect(event.data.task_id).toBeTruthy()
        expect(event.data.child_session_id).toBeTruthy()
        expect(event.data.attempt).toBeTypeOf('number')
      }
    }
  })

  describe('D1: Simple Delegation', () => {
    it('renders task panel with task name', async () => {
      result = renderChat({
        scenarioEvents: simpleDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Research competitors')

      // Task name should appear in the delegation panel
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Research competitors')
      }, { timeout: 5000 })
    })

    it('shows final agent text after delegation completes', async () => {
      result = renderChat({
        scenarioEvents: simpleDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Research competitors')

      // Final summary text
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('top competitors in the market')
      }, { timeout: 10000 })
    })

    it('shows task duration on completion', async () => {
      result = renderChat({
        scenarioEvents: simpleDelegation.events as FixtureEvent[],
      })

      await result.sendMessage('Research competitors')

      // Duration should appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('3.2s')
      }, { timeout: 5000 })
    })
  })

  describe('D2: Multi-Task Parallel Delegation', () => {
    it('renders all three task names', async () => {
      result = renderChat({
        scenarioEvents: multiTaskParallel.events as FixtureEvent[],
      })

      await result.sendMessage('Research from multiple angles')

      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('Market research')
        expect(text).toContain('Competitor analysis')
        expect(text).toContain('Write summary')
      }, { timeout: 5000 })
    })

    it('shows all tasks completed with durations', async () => {
      result = renderChat({
        scenarioEvents: multiTaskParallel.events as FixtureEvent[],
      })

      await result.sendMessage('Research')

      // All durations should appear
      await waitFor(() => {
        const text = result.container.textContent || ''
        expect(text).toContain('5.1s')
        expect(text).toContain('4.3s')
        expect(text).toContain('2.8s')
      }, { timeout: 5000 })
    })
  })

  describe('D4: Task Failure', () => {
    it('renders failed task with error message', async () => {
      result = renderChat({
        scenarioEvents: taskFailure.events as FixtureEvent[],
      })

      await result.sendMessage('Deploy changes')

      // Task name
      await waitFor(() => {
        expect(screen.getByText(/Deploy to staging/)).toBeInTheDocument()
      }, { timeout: 5000 })

      // Error text in the delegation panel or agent message
      await waitFor(() => {
        expect(screen.getByText(/Connection refused/i)).toBeInTheDocument()
      }, { timeout: 5000 })
    })

    it('shows agent explanation after delegation failure', async () => {
      result = renderChat({
        scenarioEvents: taskFailure.events as FixtureEvent[],
      })

      await result.sendMessage('Deploy')

      await waitFor(() => {
        expect(screen.getByText(/deployment failed/)).toBeInTheDocument()
      }, { timeout: 5000 })
    })
  })
})
