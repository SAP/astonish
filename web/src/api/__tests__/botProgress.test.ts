import { expect, it } from 'vitest'
import { ProgressPhase, progressLabels, validBotProgress, type BotProgressProjection } from '../botProgress'
import { botRunStatusLabel, canContinueBotProgress, mergeBotRunState, type BotRunState } from '../botRunState'
import { runtimePolicySummary, validRuntimePolicy, type BotRuntimePolicy } from '../botConfiguration'
const progress: BotProgressProjection = { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', revision: 4, phase: 'evaluating', active_duration_nanos: 21600000000000 }
const state: BotRunState = { schema_version: 1, bot_id: 'b', run_id: 'r', session_id: 's', home_session_id: 'h', presentation: '', status: 'running', state_sequence: 5, progress }
it.each([undefined, 0, 7])('accepts recorded coalesced occurrence count %s', coalesced_occurrences => {
  expect(validBotProgress({ ...progress, coalesced_occurrences }, state)).toBe(true)
})
it.each([-1, 0.5, Infinity, NaN, Number.MAX_SAFE_INTEGER + 1, '3', null])('rejects malformed coalesced count %s', coalesced_occurrences => {
  expect(validBotProgress({ ...progress, coalesced_occurrences } as BotProgressProjection, state)).toBe(false)
})
it.each(Object.values(ProgressPhase))('renders %s as progress never completion or timer failure', phase => {
  expect(botRunStatusLabel({ ...state, progress: { ...progress, phase } })).toBe(progressLabels[phase])
  expect(botRunStatusLabel({ ...state, status: 'succeeded', progress: { ...progress, phase } })).toBe('Completed')
})
it('restores scoped scheduled progress and orders each run independently', () => {
  const first = mergeBotRunState({}, state, 'b', 'h')
  expect(first.r.progress).toEqual(progress)
  expect(mergeBotRunState({}, first.r, 'b', 'h')).toEqual(first)
  expect(mergeBotRunState(first, { ...state, state_sequence: 4 }, 'b', 'h')).toBe(first)
  expect(mergeBotRunState(first, { ...state, run_id: 'r2', state_sequence: 1, progress: { ...progress, run_id: 'r2' } }, 'b', 'h').r2).toBeDefined()
  for (const patch of [{ bot_id: 'foreign' }, { run_id: 'foreign' }, { session_id: 'foreign' }, { schema_version: 99 }]) expect(mergeBotRunState({}, { ...state, progress: { ...progress, ...patch } }, 'b', 'h')).toEqual({})
})
it('keeps cumulative checkpoint progress monotonic while accepting newer execution state', () => {
  const first = mergeBotRunState({}, state, 'b', 'h')
  for (const patch of [{ revision: 3 }, { revision: 5, active_duration_nanos: 1 }]) {
    const next = mergeBotRunState(first, { ...state, state_sequence: 6, progress: { ...progress, ...patch } }, 'b', 'h')
    expect(next.r.state_sequence).toBe(6)
    expect(next.r.progress).toEqual(progress)
  }
  const final = mergeBotRunState(first, { ...state, state_sequence: 7, status: 'succeeded', progress: undefined }, 'b', 'h')
  expect(botRunStatusLabel(final.r)).toBe('Completed')
  const fallback = mergeBotRunState(first, { ...state, state_sequence: 7, progress: { ...progress, schema_version: 99 } }, 'b', 'h')
  expect(botRunStatusLabel(fallback.r)).toBe('Working')
  expect(mergeBotRunState(first, { ...state, state_sequence: 7, session_id: 'foreign', progress: undefined }, 'b', 'h')).toBe(first)
})
it('falls back on unknown optional progress and never offers unsafe continuation', () => {
  const unknown = mergeBotRunState({}, { ...state, presentation: 'home_interactive', progress: { ...progress, schema_version: 99 } }, 'b', 'h')
  expect(botRunStatusLabel(unknown.r)).toBe('Working')
  expect(canContinueBotProgress(state)).toBe(false)
  const paused = { ...state, progress: { ...progress, phase: ProgressPhase.Suspended, reason: 'paused' } }
  expect(canContinueBotProgress(paused)).toBe(true)
  expect(canContinueBotProgress({ ...paused, status: 'cancelled' })).toBe(false)
  expect(canContinueBotProgress({ ...paused, progress: { ...paused.progress, reason: 'uncertain_effect' } })).toBe(false)
})
const policy: BotRuntimePolicy = { schema_version: 1, mode: 'progress_v1', evaluation_interval_seconds: 300, model_call_timeout_seconds: 120, tool_call_timeout_seconds: 120 }
it('validates nested policy separately and preserves legacy semantics', () => {
  expect(validRuntimePolicy()).toBe(true)
  expect(validRuntimePolicy(policy)).toBe(true)
  expect(runtimePolicySummary({ max_runtime_seconds: 900 })).toContain('900s/run')
  expect(runtimePolicySummary({ max_runtime_seconds: 900, runtime_policy: policy })).toContain('No task duration limit; token, tool and spending limits still apply.')
  for (const max_active_seconds of [0, -1, 1.5, Infinity, 9223372037]) expect(validRuntimePolicy({ ...policy, max_active_seconds })).toBe(false)
  expect(validRuntimePolicy({ ...policy, evaluation_interval_seconds: undefined })).toBe(false)
  expect(validRuntimePolicy({ ...policy, schema_version: 2 })).toBe(false)
  expect(validRuntimePolicy({ ...policy, mode: 'legacy' })).toBe(false)
})
