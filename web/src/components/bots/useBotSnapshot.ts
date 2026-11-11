import { botLocalFailure } from '../chat/botEmployeeFeedback'
import { useCallback, useEffect, useSyncExternalStore } from 'react'
import { BotAPIError, type BotActivity, type BotScope } from '@/api/bots'
import { getBotSnapshot, readBotActivityPage, writeBotReadCursor, type BotSnapshot } from '@/api/botSnapshot'
import { getActiveTeam } from '@/api/teamContext'
import { isBotOperation, OperationStatus, type BotOperationView } from '@/api/botOperationResult'

export interface SnapshotState { data?: BotSnapshot; activity: BotActivity[]; loading: boolean; error?: string }
const empty: SnapshotState = { activity: [], loading: true }
const entries = new Map<string, SnapshotEntry>()

/** A disposable, viewer-scoped projection cache, not a transcript store. All
 * consumers of the same Bot share a single poll and identical server counts. */
class SnapshotEntry {
  state: SnapshotState = empty
  listeners = new Set<() => void>()
  activityUsers = 0
  private controller?: AbortController
  private inflight?: Promise<void>
  private timer?: ReturnType<typeof setInterval>
  constructor(readonly id: string, readonly scope: BotScope, readonly team: string | null, readonly viewer: string) {}
  private publish(state: SnapshotState) { this.state = state; this.listeners.forEach(listener => listener()) }
  refresh = (): Promise<void> => {
    if (this.inflight) return this.inflight
    const controller = new AbortController()
    this.controller = controller
    const active = () => !controller.signal.aborted
    this.inflight = (async () => {
      try {
        const data = await getBotSnapshot(this.id, this.scope, this.team, controller.signal)
        if (!active()) return
        if (data.bot.id !== this.id || data.bot.scope !== this.scope || data.read_cursor.viewer_id !== this.viewer) throw new Error('Bot snapshot identity changed. Reload your workspace.')
        const previous = this.state.data
        // Keep the root and its operation evidence together; never graft a new
        // cursor onto a stale root and then use it as message admission authority.
        if (previous && (data.bot.revision < previous.bot.revision || data.bot.version < previous.bot.version || data.latest_sequence < previous.latest_sequence)) return
        // Never let an older replica/cached response regress this viewer's cursor.
        const sequence = Math.max(data.read_cursor.sequence, previous?.read_cursor.sequence || 0)
        const latest = Math.max(data.latest_sequence, previous?.latest_sequence || 0)
        const snapshot = { ...data, latest_sequence: latest, read_cursor: { ...data.read_cursor, sequence }, unread_count: Math.max(0, latest - sequence) }
        // A failed activity page must not hide fresh root/decision state.
        this.publish({ data: snapshot, activity: this.state.activity, loading: false })
        let activity = this.state.activity
        if (this.activityUsers > 0) {
          let after = activity.at(-1)?.sequence || 0
          const merged = new Map(activity.map(event => [event.sequence, event]))
          // Catch up to a bounded high-water mark. New arrivals are picked up by
          // the next poll; a perpetually busy Bot cannot keep this request alive.
          while (after < data.latest_sequence) {
            const page = await readBotActivityPage(this.id, this.scope, this.team, after, controller.signal)
            if (!active()) return
            const next = page.activity.filter(event => event.sequence > after && event.sequence <= data.latest_sequence)
            if (!next.length) throw new Error('Activity catch-up stalled. Retry to reload activity.')
            next.forEach(event => merged.set(event.sequence, event))
            after = Math.max(...next.map(event => event.sequence))
          }
          activity = [...merged.values()].sort((a, b) => a.sequence - b.sequence)
        }
        if (active()) this.publish({ data: snapshot, activity, loading: false })
      } catch (error) {
        if (active()) this.publish({ ...(error instanceof BotAPIError && [401, 403, 404].includes(error.status) ? empty : this.state), loading: false, error: botLocalFailure(error).message })
      } finally { if (active()) this.inflight = undefined }
    })()
    return this.inflight
  }
  // Operation revisions are not Bot revisions. A committed head only tells us
  // that the existing root projection needs to catch up; it never supplies CAS.
  syncOperations = async (operations: BotOperationView[]) => {
    const home = this.state.data?.bot.home_session_id
    const committed = operations.filter(o => isBotOperation(o) && o.bot_id === this.id && o.home_session_id === home && o.status === OperationStatus.Applied)
    const caughtUp = () => committed.every(o => this.state.data?.operations?.some(s => s.operation_id === o.operation_id && s.revision >= o.revision && s.state_sequence >= o.state_sequence))
    const alive = () => this.listeners.size > 0 && this.state.data?.bot.home_session_id === home
    if (!caughtUp()) {
      // An in-flight read may have started before this commit. Drain it, then
      // issue one new read if its projection still lacks the committed head.
      await this.inflight
      if (alive() && !caughtUp()) await this.refresh()
    }
    if (!alive() || !this.state.data || !caughtUp() || (committed.length > 0 && this.state.error)) throw new Error('Bot updates unavailable. Refresh the conversation before sending.')
    return this.state.data.bot
  }
  subscribe = (listener: () => void) => {
    entries.set(snapshotKey(this.id, this.scope, this.team, this.viewer), this)
    this.listeners.add(listener)
    if (this.listeners.size === 1) {
      void this.refresh()
      this.timer = setInterval(() => { void this.refresh() }, 15000)
      window.addEventListener('astonish:bots-updated', this.onRefresh)
    }
    return () => {
      this.listeners.delete(listener)
      if (!this.listeners.size) {
        this.controller?.abort(); this.inflight = undefined
        clearInterval(this.timer)
        window.removeEventListener('astonish:bots-updated', this.onRefresh)
        this.state = empty
        entries.delete(snapshotKey(this.id, this.scope, this.team, this.viewer))
      }
    }
  }
  private onRefresh = () => { void this.refresh() }
  getSnapshot = () => this.state
  markRead = async (sequence: number) => {
    // Do not send a mutable/global team or viewer cursor captured after a switch.
    const controller = this.controller
    await writeBotReadCursor(this.id, this.scope, this.team, sequence, controller?.signal)
    if (controller?.signal.aborted) return
    await this.inflight
    await this.refresh()
    window.dispatchEvent(new Event('astonish:bots-updated'))
  }
}
const snapshotKey = (id: string, scope: BotScope, team: string | null, viewer: string) => JSON.stringify([viewer, team, scope, id])
const noopSubscribe = () => () => {}
const getEmpty = () => empty

export function useBotSnapshot(id: string, scope: BotScope, viewer?: string, withActivity = false) {
  const team = getActiveTeam()
  const key = snapshotKey(id, scope, team, viewer || '')
  let entry = viewer ? entries.get(key) : undefined
  if (viewer && !entry) { entry = new SnapshotEntry(id, scope, team, viewer); entries.set(key, entry) }
  const state = useSyncExternalStore(entry?.subscribe || noopSubscribe, entry?.getSnapshot || getEmpty, getEmpty)
  useEffect(() => {
    if (!entry || !withActivity) return
    entry.activityUsers++
    void entry.refresh().then(() => { if (entry.listeners.size && entry.activityUsers > 0 && !entry.state.activity.length && (entry.state.data?.latest_sequence || 0) > 0) return entry.refresh() })
    return () => { entry.activityUsers-- }
  }, [entry, withActivity])
  const retry = useCallback(() => { void entry?.refresh() }, [entry])
  const markRead = useCallback(async (sequence: number) => { await entry?.markRead(sequence) }, [entry])
  const syncOperations = useCallback(async (operations: BotOperationView[]) => {
    if (!entry) throw new Error('Bot updates unavailable. Refresh the conversation before sending.')
    return entry.syncOperations(operations)
  }, [entry])
  return { ...state, retry, markRead, syncOperations }
}
