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
export interface BotBudget {
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
