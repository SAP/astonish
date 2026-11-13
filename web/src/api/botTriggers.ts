import { BotAPIError, botURL, saveBotTrigger, type Bot, type BotTrigger } from './bots'
import { teamFetch } from './teamContext'

export const TriggerKind = { Cron: 'cron', Hook: 'hook', Webhook: 'webhook', Channel: 'channel', Deadline: 'goal_deadline' } as const
export const HookStatus = { Disabled: 'disabled', Passed: 'dry_run_passed', Enabled: 'enabled' } as const
export interface ChannelBinding { id: string; channel: string; chat_id: string; thread_id?: string }
export interface TriggerSpec extends BotTrigger {
  schedule_id?: string; goal_id?: string; credential_ref?: string; fingerprint?: string
  hook_watermark?: string; poll?: { url: string; allowed_host: string }; channel_binding?: ChannelBinding
}
export interface TriggerProof {
  bot_id: string; trigger_id: string; version: number; event_id: string
  timestamp: number; signature: string; payload?: string
}
export const isHook = (t: BotTrigger) => t.kind === TriggerKind.Hook || t.kind === TriggerKind.Webhook
export const canEnableTrigger = (bot: Bot, t: BotTrigger) => bot.status === 'active' && !t.enabled && (!isHook(t) || t.hook_status === HookStatus.Passed)
// Whitelist configuration fields: server-owned schedule IDs, fingerprints and dry-run
// status must never be replayed on PUT. Every edit is saved disabled and reviewed anew.
export function triggerDefinition(t: TriggerSpec): TriggerSpec {
  return { id: t.id, kind: t.kind, instructions: t.instructions, enabled: false, catch_up_once: t.catch_up_once,
    ...(t.skill_id ? { skill_id: t.skill_id, invocation: t.invocation, instructions: '' } : {}),
    ...(t.kind === TriggerKind.Cron || t.poll ? { cron: t.cron, timezone: t.timezone } : {}),
    ...(t.kind === TriggerKind.Deadline ? { goal_id: t.goal_id } : {}),
    ...(isHook(t) ? { credential_ref: t.credential_ref } : {}),
    ...(t.kind === TriggerKind.Hook && t.poll ? { poll: t.poll } : {}),
    ...(t.kind === TriggerKind.Channel ? { channel_binding: t.channel_binding } : {}),
  }
}
export const saveTrigger = (bot: Bot, t: TriggerSpec, reason: string) => saveBotTrigger(bot, triggerDefinition(t), reason)
export async function actOnTrigger(bot: Bot, t: TriggerSpec, action: 'enable' | 'disable' | 'remove' | 'dry-run', reason: string, proof?: TriggerProof): Promise<Bot> {
  if (action === 'enable' && !canEnableTrigger(bot, t)) throw new Error('Activate the Bot and pass the saved hook dry-run before enabling.')
  if (action === 'dry-run' && (!isHook(t) || t.enabled || t.hook_status !== HookStatus.Disabled || (!t.poll && !proof))) throw new Error('A disabled hook and signed dry-run proof are required.')
  const path = `/${encodeURIComponent(bot.id)}/triggers/${encodeURIComponent(t.id)}${action === 'remove' ? '' : `/${action}`}`
  const response = await teamFetch(botURL(path, bot.scope), { method: action === 'remove' ? 'DELETE' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: bot.revision, reason, ...(proof ? { proof } : {}) }) })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Trigger request failed (${response.status})`)
  return response.json()
}
