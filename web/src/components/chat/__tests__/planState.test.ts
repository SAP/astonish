import { describe, expect, it } from 'vitest'
import { normalizePlanHistory, planFromSnapshot, reducePlanProgress, upsertPlanSnapshot } from '../planState'
import type { ChatMsg, PlanSnapshot } from '../chatTypes'
import fixture from '@/test/fixtures/scenarios/planning/plan-revisions.json'

const snapshot = fixture.events[1].data.snapshot as PlanSnapshot

describe('plan authority', () => {
  it('upserts one identity by sequence, retaining transcript position and separate identities', () => {
    const initial: ChatMsg[] = [{ type: 'user', content: 'Build' }, planFromSnapshot(snapshot), { type: 'agent', content: 'Review' }]
    expect(upsertPlanSnapshot(initial, snapshot)).toBe(initial)
    expect(upsertPlanSnapshot(initial, { ...snapshot, sequence: 9 })).toBe(initial)
    const next = { ...snapshot, sequence: 11, revision: 2, pending_revision: undefined }
    const updated = upsertPlanSnapshot(initial, next)
    expect(updated).toHaveLength(3)
    expect(updated[1]).toEqual(planFromSnapshot(next))
    expect(initial[1]).toEqual(planFromSnapshot(snapshot))
    expect(upsertPlanSnapshot(updated, { ...next, plan_id: 'other', sequence: 1 })).toHaveLength(4)
  })
  it('uses identical authority for send, replay and history, including proposal resolution', () => {
    const next = { ...snapshot, sequence: 11, pending_revision: undefined }
    const events = [snapshot, snapshot, next, snapshot]
    const live = events.reduce<ChatMsg[]>((state, snapshot) => reducePlanProgress(state, { event_type: 'plan_snapshot', snapshot }), [])
    expect(normalizePlanHistory(events.map(planFromSnapshot))).toEqual(live)
    expect(live).toHaveLength(1)
    expect(live[0]).toMatchObject({ snapshot: { pending_revision: undefined } })
  })
  it('does not mutate authority on legacy progress or infer completion on done/errors', () => {
    const state = [planFromSnapshot(snapshot)]
    for (const event_type of ['plan_step_update', 'plan_announced', 'done', 'error']) {
      expect(reducePlanProgress(state, { event_type, step_name: 'implement', step_status: 'failed' })).toBe(state)
    }
  })
  it('keeps legacy announcements and explicit statuses readable without auto completion', () => {
    const initial = reducePlanProgress([], { event_type: 'plan_announced', plan_goal: 'Legacy', plan_steps: [{ name: 'a', description: 'A', parallel_group: 'wave' }] })
    expect(initial[0]).toMatchObject({ steps: [{ status: 'pending', parallelGroup: 'wave' }] })
    const running = reducePlanProgress(initial, { event_type: 'plan_step_update', step_name: 'a', step_status: 'running' })
    expect(reducePlanProgress(running, { event_type: 'done' })).toBe(running)
    expect(running[0]).toMatchObject({ steps: [{ status: 'running' }] })
  })
})
