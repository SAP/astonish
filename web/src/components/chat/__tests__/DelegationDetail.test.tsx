import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import DelegationDetail from '../DelegationDetail'
import { delegationActivity, selectDelegationActivity } from '../delegationActivity'
import FileEditPreview from '../FileEditPreview'
import type { SubTaskEvent } from '../chatTypes'

const handoff = { version: 1, parent_session_id: 'parent', child_session_id: 'child', batch_id: 'batch', task_id: 'task', attempt: 1, name: 'worker', original_task: 'original', effective_task: 'effective', original_instructions: 'instructions', effective_instructions: '<img src="https://example.com/private">', plan_step: 'phase', workdir: '/workspace', preferred_tools: ['edit_file'], initial_tool_names: ['read_file', 'edit_file'], plan: { status: 'matched', plan_id: 'plan', revision: 7, sequence: 9, source_mirror: '/session/PLAN.md', goal: 'Captured goal', document: { context: 'Captured context', what_not_to_do: 'Captured boundaries', verification: 'Captured acceptance' }, step: { name: 'phase', description: 'description', summary: 'summary', details: 'Full phase details', files: [{ path: 'a.go', kind: 'modify' }], outcome: 'outcome', verify: 'go test ./phase', verify_kind: 'unit', parallel_group: 'wave-1' } } }
const events: SubTaskEvent[] = [
  { type: 'handoff', text: JSON.stringify(handoff) },
  { type: 'task_text', text: 'Before edit' },
  { type: 'task_tool_call', call_id: 'a', tool_name: 'edit_file', tool_args: { path: 'a.go', old_string: 'old', new_string: 'requested' } },
  { type: 'task_text', text: 'Between calls' },
  { type: 'task_tool_call', call_id: 'b', tool_name: 'edit_file', tool_args: { path: 'b.go' } },
  { type: 'task_tool_result', call_id: 'b', tool_name: 'edit_file', tool_result: { diff: '-b\n+actual-b' } },
  { type: 'task_tool_result', call_id: 'a', tool_name: 'edit_file', tool_result: { diff: '-old\n+actual-a' } },
  { type: 'task_text', text: 'After edits' },
]

describe('Delegation detail', () => {
  it('separates handoff from activity and displays captured context as inert full text', () => {
    const { container } = render(<DelegationDetail live={[]} persisted={events} identity="child" running={false} loading={false} retry={vi.fn()} />)
    expect(screen.queryByText('Full phase details')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Handoff' }))
    for (const text of ['Full phase details', 'Captured context', 'Captured boundaries', 'Captured acceptance', '/session/PLAN.md', '7', '9', 'go test ./phase', 'modify: a.go', 'read_file, edit_file']) expect(screen.getByText(text)).toBeInTheDocument()
    expect(screen.queryByText('Before edit')).not.toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText(handoff.effective_instructions)).toBeInTheDocument()
  })
  it('shows loading, error and explicit unavailable without inventing context', () => {
    const props = { live: [], identity: 'child', running: false, retry: vi.fn() }
    const { rerender } = render(<DelegationDetail {...props} loading />)
    fireEvent.click(screen.getByRole('button', { name: 'Handoff' }))
    expect(screen.getByRole('status')).toHaveTextContent('Loading handoff')
    rerender(<DelegationDetail {...props} loading={false} error="Forbidden" />)
    expect(screen.getByRole('alert')).toHaveTextContent('Forbidden')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(props.retry).toHaveBeenCalledOnce()
    rerender(<DelegationDetail {...props} loading={false} persisted={[{ type: 'handoff', text: '{bad' }]} />)
    expect(screen.getByRole('status')).toHaveTextContent('Handoff unavailable')
  })
  it('uses the shared main-chat tool renderer, paired identity and actual result diff', () => {
    render(<DelegationDetail live={events} persisted={events} identity="child" running={false} loading={false} retry={vi.fn()} />)
    fireEvent.click(within(screen.getByTestId('tool-activity-block')).getByRole('button'))
    expect(screen.getAllByText('Before edit')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: /edit_file.*actual-a/s }))
    const preview = screen.getByTestId('file-edit-preview')
    expect(preview).toHaveTextContent('+actual-a')
    expect(preview).not.toHaveTextContent('requested')
    expect(preview).not.toHaveTextContent('actual-b')
  })
  it('keeps ordered live and restored activity equal without mixing sequence clocks', () => {
    const live = selectDelegationActivity(events, events.slice(0, 3), true)
    const restored = selectDelegationActivity([], events, false)
    expect(delegationActivity(live)).toEqual(delegationActivity(restored))
    expect(delegationActivity(live).map(e => e.index)).toEqual([0, 1, 2, 3, 6])
    const tools = delegationActivity(live).filter(e => e.kind === 'tool')
    expect(tools.map(e => e.step.result)).toEqual([{ diff: '-old\n+actual-a' }, { diff: '-b\n+actual-b' }])
  })
  it.each([{ success: false }, { ok: false }, { output: { status: 'failed' } }, 'Error: edit failed'])('uses main-chat failure semantics for %j', result => {
    const entries = delegationActivity([
      { type: 'task_tool_call', call_id: 'call', tool_name: 'edit_file' },
      { type: 'task_tool_result', call_id: 'call', tool_name: 'edit_file', tool_result: result },
    ])
    expect(entries[0].kind === 'tool' && entries[0].step.status).toBe('error')
  })
  it('prefers recorded verification context and path over requested content', () => {
    render(<FileEditPreview step={{ toolName: 'write_file', callIndex: 0, status: 'complete', args: { path: 'requested.md', content: 'not actual' }, result: { path: 'actual.md', verification_context: '+persisted content' } }} />)
    expect(screen.getByTestId('file-edit-preview')).toHaveTextContent('actual.md — Recorded edit')
    expect(screen.getByTestId('file-edit-preview')).toHaveTextContent('+persisted content')
    expect(screen.getByTestId('file-edit-preview')).not.toHaveTextContent('not actual')
  })
  it('labels requested edits unverified and suppresses failed edit previews', () => {
    const step = { toolName: 'edit_file', callIndex: 0, status: 'running' as const, args: { path: 'a', old_string: 'old', new_string: 'new' } }
    const { rerender } = render(<FileEditPreview step={step} />)
    expect(screen.getByTestId('file-edit-preview')).toHaveTextContent('not verified')
    rerender(<FileEditPreview step={{ ...step, status: 'error', result: { error: 'failed' } }} />)
    expect(screen.queryByTestId('file-edit-preview')).not.toBeInTheDocument()
  })
})
