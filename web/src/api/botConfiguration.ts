import { BotAPIError, botURL, type Bot, type BotScope, type BotVersion } from './bots'
import { teamFetch } from './teamContext'

export const AutonomyLevel = { Ask: 'ask', Supervised: 'supervised', Reversible: 'autonomous-reversible', Scoped: 'autonomous-scoped' } as const
export type AutonomyLevelValue = typeof AutonomyLevel[keyof typeof AutonomyLevel]
export const NotifyPolicy = { Never: 'never', Change: 'on_change', Decision: 'on_decision_needed', Always: 'always' } as const
export type NotifyPolicyValue = typeof NotifyPolicy[keyof typeof NotifyPolicy]
export interface BotAutonomy {
  level: AutonomyLevelValue
  allowed_tools: string[] | null; denied_tools: string[] | null
  allowed_paths: string[] | null; allowed_hosts: string[] | null
  can_contact_people: boolean; can_publish: boolean; spend_currency?: string
  max_spend_per_run_minor: number; max_spend_per_month_minor: number
}
export const RuntimeMode = { Legacy: 'legacy', Progress: 'progress_v1' } as const
export interface BotRuntimePolicy {
  schema_version: number; mode: typeof RuntimeMode[keyof typeof RuntimeMode]
  evaluation_interval_seconds?: number; max_active_seconds?: number
  model_call_timeout_seconds?: number; tool_call_timeout_seconds?: number
}
export const runtimeSecondsMax = 9223372036
export function validRuntimePolicy(policy?: BotRuntimePolicy): boolean {
  if (!policy) return true
  if (policy.schema_version !== 1) return false
  if (policy.mode === RuntimeMode.Legacy) return !policy.evaluation_interval_seconds && policy.max_active_seconds == null && !policy.model_call_timeout_seconds && !policy.tool_call_timeout_seconds
  if (policy.mode !== RuntimeMode.Progress) return false
  return [policy.evaluation_interval_seconds, policy.model_call_timeout_seconds, policy.tool_call_timeout_seconds, ...(policy.max_active_seconds == null ? [] : [policy.max_active_seconds])].every(value => typeof value === 'number' && Number.isSafeInteger(value) && value > 0 && value <= runtimeSecondsMax)
}
export function runtimePolicySummary(budget: BotBudget): string {
  const policy = budget.runtime_policy
  if (!validRuntimePolicy(policy)) return 'Runtime policy unavailable'
  if (policy?.mode !== RuntimeMode.Progress) return `${budget.max_runtime_seconds}s/run (saved legacy limit)`
  return `${policy.max_active_seconds ? `Maximum ${policy.max_active_seconds}s active time` : 'No task duration limit; token, tool and spending limits still apply.'} · progress checks every ${policy.evaluation_interval_seconds}s · operator policy remains authoritative`
}
export interface BotBudget {
  runtime_policy?: BotRuntimePolicy
  max_runtime_seconds: number; max_tool_calls?: number; max_tokens?: number
  max_runs_per_day?: number; max_model_cost_micros?: number
}
export interface BotDeliveryBinding { id: string; channel: string; chat_id: string; thread_id?: string }
export interface BotDelivery { notify: NotifyPolicyValue; preferences: string; bindings: BotDeliveryBinding[] | null }
export interface BotConfiguration { autonomy: BotAutonomy; budget: BotBudget; delivery: BotDelivery }
export type BotConfigurationVersion = Omit<BotVersion, keyof BotConfiguration> & BotConfiguration
export interface BotConfigurationRequest extends BotConfiguration {
  expected_revision: number; expected_version: number; reviewed: boolean; reason: string
}
export async function configureBot(id: string, scope: BotScope, body: BotConfigurationRequest): Promise<Bot> {
  const response = await teamFetch(botURL(`/${encodeURIComponent(id)}/configuration`, scope), { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Configuration request failed (${response.status})`)
  return response.json() as Promise<Bot>
}
export async function getBotConfigurationVersion(bot: Bot, signal?: AbortSignal): Promise<BotConfigurationVersion> {
  const response = await teamFetch(botURL(`/${encodeURIComponent(bot.id)}/versions/${bot.version}`, bot.scope), { signal })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Configuration load failed (${response.status})`)
  return response.json() as Promise<BotConfigurationVersion>
}
