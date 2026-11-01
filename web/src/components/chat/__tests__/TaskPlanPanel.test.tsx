import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { fetchSubtaskEvents } from '../../../api/studioChat'
vi.mock('../../../api/studioChat', () => ({ fetchSubtaskEvents: vi.fn() }))

import TaskPlanPanel from '../TaskPlanPanel'
import type { SubTaskExecutionMessage } from '../chatTypes'

describe('TaskPlanPanel', () => {
  it('cancels stale detail when switching attempts and shows selected status', async () => {
    let resolveOld!: (events: []) => void
    vi.mocked(fetchSubtaskEvents).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve })).mockResolvedValueOnce([{ type: 'task_text', text: 'new detail' }])
    render(<TaskPlanPanel sessionId="parent" data={{ type: 'subtask_execution', batch_id: 'b', status: 'complete', tasks: [{ task_id: 't', name: 'worker', description: '' }], events: [
      { type: 'task_failed', task_id: 't', attempt: 1, child_session_id: 'old', error: 'old failure', duration: '1s' },
      { type: 'task_complete', task_id: 't', attempt: 2, child_session_id: 'new', duration: '2s' },
    ] }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Attempt 1' }))
    expect(screen.getByText('old failure')).toBeInTheDocument()
    expect(screen.getByText('1/1 task')).toBeInTheDocument()
    const signal = vi.mocked(fetchSubtaskEvents).mock.calls.at(-1)![2]!
    fireEvent.click(screen.getByRole('button', { name: 'Attempt 2' }))
    expect(signal.aborted).toBe(true)
    expect(fetchSubtaskEvents).toHaveBeenLastCalledWith('parent', { child_session_id: 'new' }, expect.any(AbortSignal))
    resolveOld([])
    await waitFor(() => expect(screen.getByText('new detail')).toBeInTheDocument())
    expect(screen.queryByText('old failure')).not.toBeInTheDocument()
  })

  it('keeps retry attempts inside the task row as a spaced selector', () => {
    render(<TaskPlanPanel sessionId="parent" data={{ type: 'subtask_execution', batch_id: 'b', status: 'running', tasks: [{ task_id: 't', name: 'worker', description: 'Research' }], events: [
      { type: 'task_failed', task_id: 't', attempt: 1, child_session_id: 'old', error: 'network error' },
      { type: 'task_retry', task_id: 't', attempt: 2, child_session_id: 'new' },
      { type: 'task_start', task_id: 't', attempt: 2, child_session_id: 'new' },
    ] }} />)

    const row = screen.getByTestId('delegation-task-row-t')
    const selector = screen.getByLabelText('Attempts for worker')
    expect(row).toContainElement(selector)
    expect(screen.getByRole('button', { name: 'Attempt 1' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Attempt 2' })).toBeVisible()
    expect(selector).toHaveClass('gap-1')
  })

  it('shows a restored completed task while sibling retries remain running', () => {
    render(<TaskPlanPanel sessionId="parent" data={{ type: 'subtask_execution', batch_id: 'b', status: 'running', tasks: [
      { task_id: 'done', name: 'completed-worker', description: '' },
      { task_id: 'retry', name: 'retrying-worker', description: '' },
    ], events: [
      { type: 'task_complete', task_id: 'done', attempt: 1, duration: '3s' },
      { type: 'task_failed', task_id: 'retry', attempt: 1, error: 'network error' },
      { type: 'task_retry', task_id: 'retry', attempt: 2 },
      { type: 'task_start', task_id: 'retry', attempt: 2 },
    ] }} />)

    expect(screen.getByText('1/2 tasks')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /completed-worker/ })).toHaveTextContent('3s')
    expect(screen.getByRole('button', { name: /retrying-worker/ })).toHaveTextContent('running')
  })

  it('shows an error instead of successful empty delegation', () => {
    render(<TaskPlanPanel data={{ type: 'subtask_execution', status: 'complete' } as SubTaskExecutionMessage} />)
    expect(screen.getByRole('alert')).toHaveTextContent('missing or invalid')
  })
  it('uses exact attempt identity and exposes retry after ambiguous detail', async () => {
    vi.mocked(fetchSubtaskEvents).mockRejectedValueOnce(new Error('Ambiguous task identity')).mockResolvedValueOnce([])
    render(<TaskPlanPanel sessionId="parent" data={{ type: 'subtask_execution', batch_id: 'batch', status: 'complete', tasks: [{ task_id: 'task', name: 'worker', description: '' }], events: [
      { type: 'task_failed', task_id: 'task', attempt: 1, child_session_id: 'child-one' },
      { type: 'task_complete', task_id: 'task', attempt: 2, child_session_id: 'child-two' },
    ] }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Attempt 1' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Ambiguous'))
    expect(fetchSubtaskEvents).toHaveBeenLastCalledWith('parent', { child_session_id: 'child-one' }, expect.any(AbortSignal))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.getByText('No execution detail recorded.')).toBeInTheDocument())
  })
  it('loads a known child without sending stale parent batch identity', async () => {
    vi.mocked(fetchSubtaskEvents).mockResolvedValueOnce([{ type: 'task_tool_call', tool_name: 'read_file' }])
    render(<TaskPlanPanel sessionId="parent" data={{ type: 'subtask_execution', batch_id: 'provider-call', status: 'complete', tasks: [{ task_id: 'task', child_session_id: 'child', name: 'worker', description: '' }], events: [
      { type: 'task_complete', batch_id: 'runtime-batch', task_id: 'task', child_session_id: 'child', attempt: 1 },
    ] }} />)
    fireEvent.click(screen.getByRole('button', { name: /worker/ }))
    await waitFor(() => expect(fetchSubtaskEvents).toHaveBeenCalled())
    expect(fetchSubtaskEvents).toHaveBeenLastCalledWith('parent', { child_session_id: 'child' }, expect.any(AbortSignal))
  })

  it('renders without crashing when a restored message omits tasks/events', () => {
    // Reproduces the blank-screen bug: a delegate_tasks history message whose
    // buffered plan could not be matched serializes with `tasks` (and possibly
    // `events`) absent. Before the guard, `for (const t of data.tasks)` threw
    // "TypeError: tasks is not iterable" inside the useMemo and blanked the chat.
    const data = {
      type: 'subtask_execution',
      status: 'complete',
    } as unknown as SubTaskExecutionMessage

    expect(() => render(<TaskPlanPanel data={data} />)).not.toThrow()
  })

  it('renders task names from a well-formed message', () => {
    const data: SubTaskExecutionMessage = {
      type: 'subtask_execution',
      status: 'complete',
      tasks: [{ name: 'researcher', description: 'Research the topic' }],
      events: [
        { type: 'delegation_start', tasks: [{ name: 'researcher', description: 'Research the topic' }] },
        { type: 'task_start', task_name: 'researcher' },
        { type: 'task_complete', task_name: 'researcher', duration: '2s' },
        { type: 'delegation_complete', status: 'complete' },
      ],
    }

    const { getByText } = render(<TaskPlanPanel data={data} />)
    expect(getByText('researcher')).toBeInTheDocument()
  })
})
