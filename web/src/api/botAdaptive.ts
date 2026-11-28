import { BotAPIError, botURL, type Bot, type BotScope } from './bots'
import { teamFetch, getActiveTeam } from './teamContext'

/** Exact DTOs from store.BotAdaptiveReview / store.BotConversionRequest.
 * updated_at is the backend's compare-and-swap review token, not a JS Date.
 */
export interface BotAdaptiveReview {
  source_job_id: string
  updated_at: string
  schedule: { cron: string; timezone?: string }
  instructions: string
}
export interface BotAdaptiveConversionRequest {
  expected_updated_at: string
  name: string
  charter: string
  idempotency_key: string
  reviewed: boolean
  reviewed_for_team: boolean
}
async function adaptiveRequest<T>(id: string, scope: BotScope, method: 'GET' | 'POST', body?: BotAdaptiveConversionRequest, signal?: AbortSignal): Promise<T> {
  const response = await teamFetch(botURL(`/convert-adaptive/${encodeURIComponent(id)}`, scope), {
    method, signal, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
  }, getActiveTeam())
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Adaptive conversion failed (${response.status})`)
  return response.json() as Promise<T>
}
export const reviewAdaptiveJob = (id: string, scope: BotScope, signal?: AbortSignal) => adaptiveRequest<BotAdaptiveReview>(id, scope, 'GET', undefined, signal)
export const convertAdaptiveJob = (id: string, scope: BotScope, request: BotAdaptiveConversionRequest) => adaptiveRequest<Bot>(id, scope, 'POST', request)
