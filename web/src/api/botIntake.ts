import { BotAPIError, botURL, type BotCarryover, type BotScope } from './bots'
import { teamFetch, getActiveTeam } from './teamContext'
import { getMe } from './auth'

export const IntakeMode = { Blank: 'blank', Chat: 'chat', Template: 'template', Adaptive: 'adaptive' } as const
export interface BotIntakeContent {
  mode: typeof IntakeMode[keyof typeof IntakeMode]
  name: string; charter: string; language: string; timezone: string; identity: string
  working_style: string; owner_preferences: string; carryover: BotCarryover
  source_id?: string; template_revision?: number
}
export interface BotIntake { id: string; owner_id: string; scope: BotScope; revision: number; content: BotIntakeContent; updated_at: string }
export interface IntakePointer { id: string; scope: BotScope }
export const intakeNamespace = (owner: string, team: string | null, scope: BotScope) => `astonish:bot-intake:${encodeURIComponent(owner)}:${encodeURIComponent(team || '')}:${scope}`
export async function currentIntakeNamespace(scope: BotScope, team = getActiveTeam()) {
  const me = await getMe()
  return intakeNamespace(me.user.id, team, scope)
}
export function readIntakePointer(namespace: string): IntakePointer | null {
  try {
    const value = JSON.parse(localStorage.getItem(namespace) || 'null') as IntakePointer | null
    return value && /^[0-9a-f-]{36}$/i.test(value.id) && (value.scope === 'personal' || value.scope === 'team') ? value : null
  } catch { return null }
}
function remember(namespace: string, pointer: IntakePointer | null) {
  try { if (pointer) localStorage.setItem(namespace, JSON.stringify(pointer)); else localStorage.removeItem(namespace) } catch { /* storage may be disabled */ }
  window.dispatchEvent(new Event('astonish:intake-updated'))
}

/** One ordered writer per dialog. Discard seals it before waiting for outstanding saves. */
export class BotIntakeWriter {
  revision = 0
  private queue: Promise<unknown> = Promise.resolve()
  private sealed = false
  private saved = ''
  private retry: { body: string; key: string } | null = null
  private discardKey = crypto.randomUUID()
  constructor(readonly id: string, readonly scope: BotScope, readonly namespace: string, readonly team: string | null) {}
  private async request<T>(method: string, body?: unknown): Promise<T> {
    const text = body === undefined ? undefined : JSON.stringify(body)
    if (text && new TextEncoder().encode(text).length > 64 * 1024) throw new Error('Intake exceeds the 64 KiB limit. Shorten the text before saving.')
    const response = await teamFetch(botURL(`/intake/${encodeURIComponent(this.id)}`, this.scope), {
      method, ...(text ? { headers: { 'Content-Type': 'application/json' }, body: text } : {}),
    }, this.team)
    if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || 'Intake request failed')
    return response.status === 204 ? undefined as T : await response.json() as T
  }
  async load(): Promise<BotIntake> {
    await this.queue.catch(() => {})
    const value = await this.request<BotIntake>('GET')
    this.revision = value.revision; this.saved = JSON.stringify(value.content); this.retry = null
    return value
  }
  save(content: BotIntakeContent): Promise<void> {
    if (this.sealed) return Promise.reject(new Error('Intake is closed'))
    const snapshot = JSON.stringify(content)
    const work = this.queue.catch(() => {}).then(async () => {
      if (this.sealed || snapshot === this.saved) return
      const body = JSON.stringify({ expected_revision: this.revision, content: JSON.parse(snapshot) })
      if (this.retry?.body !== body) this.retry = { body, key: crypto.randomUUID() }
      const value = await this.request<BotIntake>('PUT', { ...JSON.parse(body), idempotency_key: this.retry.key })
      this.revision = value.revision; this.saved = snapshot; this.retry = null
      remember(this.namespace, { id: this.id, scope: this.scope })
    })
    this.queue = work
    return work
  }
  forgetPointer() {
    if (readIntakePointer(this.namespace)?.id === this.id) remember(this.namespace, null)
  }
  async discard(): Promise<void> {
    this.sealed = true
    await this.queue.catch(() => {})
    try {
      await this.request<void>('DELETE', { expected_revision: this.revision, idempotency_key: this.discardKey })
    } catch (error) {
      if (!(error instanceof BotAPIError && error.status === 404)) { this.sealed = false; throw error }
    }
    if (readIntakePointer(this.namespace)?.id === this.id) remember(this.namespace, null)
  }
}
