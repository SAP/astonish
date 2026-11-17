import { expect, it } from 'vitest'
import { newBotProgressRuntimePolicy, runtimePolicySummary, validRuntimePolicy, type BotRuntimePolicy } from '../botConfiguration'
import type { BotVersion } from '../bots'

it('shares health policy with Bot versions and accepts zero or omitted elapsed timeout', () => {
  const policy = newBotProgressRuntimePolicy()
  const budget: BotVersion['budget'] = { max_runtime_seconds: 900, runtime_policy: policy }
  expect(validRuntimePolicy(budget.runtime_policy)).toBe(true)
  expect(validRuntimePolicy({ ...policy, model_call_timeout_seconds: undefined })).toBe(true)
  expect(policy.generation_health).toEqual({ idle_timeout_seconds: 120, check_interval_seconds: 10 })
  expect(runtimePolicySummary(budget)).toContain('No fixed generation duration; inactivity threshold 120s')
  expect(runtimePolicySummary(budget)).toContain('token, tool and spending limits still apply')
  const other = newBotProgressRuntimePolicy()
  other.generation_health!.idle_timeout_seconds = 20
  expect(policy.generation_health!.idle_timeout_seconds).toBe(120)
})
it.each([0, -1, 0.5, NaN, Infinity, 9223372037, '120', undefined])('rejects invalid health seconds %s', value => {
  for (const key of ['idle_timeout_seconds', 'check_interval_seconds']) {
    const policy = newBotProgressRuntimePolicy()
    expect(validRuntimePolicy({ ...policy, generation_health: { ...policy.generation_health, [key]: value } } as BotRuntimePolicy)).toBe(false)
  }
})
it('rejects conflicting, incomplete, unknown and legacy health policies', () => {
  const policy = newBotProgressRuntimePolicy()
  for (const patch of [
    { model_call_timeout_seconds: 120 },
    { model_call_timeout_seconds: -1 },
    { generation_health: { idle_timeout_seconds: 10, check_interval_seconds: 11 } },
    { generation_health: {} },
    { generation_health: { ...policy.generation_health, disabled: true } },
    { generation_health: undefined },
    { mode: 'legacy' },
  ]) expect(validRuntimePolicy({ ...policy, ...patch } as BotRuntimePolicy)).toBe(false)
  expect(validRuntimePolicy({ schema_version: 1, mode: 'legacy', generation_health: policy.generation_health })).toBe(false)
})
it('never fills defaults into saved elapsed policies', () => {
  const policy: BotRuntimePolicy = { schema_version: 1, mode: 'progress_v1', evaluation_interval_seconds: 300, model_call_timeout_seconds: 120, tool_call_timeout_seconds: 120 }
  const before = JSON.stringify(policy)
  expect(validRuntimePolicy(policy)).toBe(true)
  expect(runtimePolicySummary({ max_runtime_seconds: 900, runtime_policy: policy })).toContain('Model elapsed timeout 120s (saved deadline)')
  expect(JSON.stringify(policy)).toBe(before)
  expect(validRuntimePolicy({ ...policy, model_call_timeout_seconds: 0 })).toBe(false)
})
