import { teamFetch } from './teamContext'
import { BotAPIError, botURL, type Bot, type BotRun } from './bots'

export const StoppableRunStatus = { Queued: 'queued', Running: 'running', WaitingApproval: 'waiting_approval' } as const
export function canStopBotRun(run: Pick<BotRun, 'status'>): boolean {
  return Object.values(StoppableRunStatus).some(status => status === run.status)
}

/** Cancellation records intent; a 202 does not attest physical teardown. No JSON body. */
export async function cancelBotRun(bot: Pick<Bot, 'id' | 'scope'>, runId: string): Promise<BotRun> {
  const response = await teamFetch(botURL(`/${encodeURIComponent(bot.id)}/runs/${encodeURIComponent(runId)}/cancel`, bot.scope), { method: 'POST' })
  const text = await response.text()
  if (!response.ok) throw new BotAPIError(response.status, text.trim() || `Run cancellation failed (${response.status})`)
  return JSON.parse(text) as BotRun
}
