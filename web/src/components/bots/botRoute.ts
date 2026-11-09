import type { BotScope } from '@/api/bots'
export interface BotRoute { botId: string; scope: BotScope; runId?: string }
export function botPath(botId: string, scope: BotScope = 'personal', runId?: string) {
  return `/chat/bots/${scope}/${encodeURIComponent(botId)}${runId ? `/runs/${encodeURIComponent(runId)}` : ''}`
}
