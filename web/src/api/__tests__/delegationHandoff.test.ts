import { describe, expect, it } from 'vitest'
import { parseDelegationHandoff, type DelegationHandoff } from '../delegationHandoff'

export const capturedHandoff: DelegationHandoff = {
  version: 1, parent_session_id: 'parent', child_session_id: 'child', batch_id: 'batch', task_id: 'task', attempt: 1,
  name: 'worker', original_task: 'Original task text', effective_task: 'Effective task text', original_instructions: 'Original instructions text', effective_instructions: '<script>do not execute</script>',
  plan_step: 'phase', workdir: '/workspace', preferred_tools: ['edit_file'], initial_tool_names: ['edit_file', 'read_file'],
  plan: { status: 'matched', plan_id: 'plan', revision: 7, sequence: 9, source_mirror: '/session/PLAN.md', goal: 'Captured goal', document: { context: 'Captured context', what_not_to_do: 'Captured boundaries', verification: 'Captured acceptance' }, step: { name: 'phase', description: 'Phase description text', summary: 'Phase summary text', details: 'Full phase details', files: [{ kind: 'modify', path: 'a.go' }], outcome: 'Phase outcome text', verify: 'go test ./phase', verify_kind: 'unit', parallel_group: 'wave-1' } },
}

describe('parseDelegationHandoff', () => {
  it('accepts captured v1 records and validates identity', () => {
    expect(parseDelegationHandoff({ type: 'handoff', text: JSON.stringify(capturedHandoff), child_session_id: 'child' })).toEqual(capturedHandoff)
    expect(parseDelegationHandoff({ type: 'handoff', text: JSON.stringify(capturedHandoff), child_session_id: 'foreign' })).toBeNull()
  })
  it('fails closed for missing, malformed, unsupported and ill-typed values', () => {
    for (const h of [null, {}, { ...capturedHandoff, version: 2 }, { ...capturedHandoff, plan: { status: 'matched', step: { details: {} } } }, { ...capturedHandoff, effective_task: {} }]) {
      expect(parseDelegationHandoff({ type: 'handoff', text: JSON.stringify(h) })).toBeNull()
    }
    expect(parseDelegationHandoff({ type: 'handoff', text: '{bad' })).toBeNull()
    expect(parseDelegationHandoff()).toBeNull()
  })
})
