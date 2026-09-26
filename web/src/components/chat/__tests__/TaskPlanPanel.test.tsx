import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import TaskPlanPanel from '../TaskPlanPanel'
import type { SubTaskExecutionMessage } from '../chatTypes'

describe('TaskPlanPanel', () => {
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
