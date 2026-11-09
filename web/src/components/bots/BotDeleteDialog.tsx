import { useEffect, useRef, useState } from 'react'
import { BotAPIError, permanentDeleteBot, type Bot } from '@/api/bots'
import { getActiveTeam } from '@/api/teamContext'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { botPath } from './botRoute'

/** Closing stops observation, not the backend's already accepted cleanup. */
export default function BotDeleteDialog({ bot, onClose, onDeleted }: { bot: Bot; onClose: () => void; onDeleted?: () => void }) {
  const [busy, setBusy] = useState(false)
  const [pending, setPending] = useState(!!bot.permanent_delete)
  const [error, setError] = useState('')
  const target = useRef(bot)
  const accepted = useRef(!!bot.permanent_delete)
  const inFlight = useRef(false)
  const generation = useRef(0)
  const navigated = useRef(false)
  const team = useRef(getActiveTeam())
  const controller = useRef<AbortController | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => {
    const changed = () => { navigated.current = true }
    window.addEventListener('hashchange', changed)
    return () => { generation.current++; controller.current?.abort(); clearTimeout(timer.current); window.removeEventListener('hashchange', changed) }
  }, [])
  const finish = () => {
    const path = `#${botPath(target.current.id, target.current.scope)}`
    if (!navigated.current && getActiveTeam() === team.current && (window.location.hash === path || window.location.hash.startsWith(`${path}/`))) window.location.hash = '/chat'
    window.dispatchEvent(new Event('astonish:bots-updated'))
    onDeleted?.()
    onClose()
  }
  const apply = async () => {
    if (inFlight.current || getActiveTeam() !== team.current) return
    inFlight.current = true; setBusy(true); setError('')
    const current = generation.current
    controller.current = new AbortController()
    try {
      const result = await permanentDeleteBot(target.current, controller.current.signal)
      if (current !== generation.current || getActiveTeam() !== team.current) return
      if (!result) { finish(); return }
      accepted.current = true; setPending(true)
      timer.current = setTimeout(() => { void apply() }, 2000)
    } catch (e) {
      if (current !== generation.current || getActiveTeam() !== team.current) return
      // Absence is meaningful only after this exact target's irreversible intent was accepted.
      if (accepted.current && e instanceof BotAPIError && e.status === 404) { finish(); return }
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (current === generation.current) { inFlight.current = false; setBusy(false) }
    }
  }
  useEffect(() => { if (target.current.permanent_delete) timer.current = setTimeout(() => { void apply() }, 0) }, [])
  const close = () => { if (!inFlight.current || accepted.current) { generation.current++; controller.current?.abort(); clearTimeout(timer.current); onClose() } }
  return <Dialog open onOpenChange={open => { if (!open) close() }}><DialogContent showCloseButton={false}>
    <DialogTitle>Permanently delete “{bot.name}”?</DialogTitle>
    <DialogDescription>This deletes the Bot, its runs, schedules, history, and files. This cannot be undone.</DialogDescription>
    {pending && <p role="status" className="text-sm">Deleting… Backend cleanup is in progress. You can close this dialog; deletion will continue.</p>}
    {error && <p role="alert" className="text-sm text-destructive">{error} {pending ? 'Deletion was accepted; cleanup has not been confirmed. Retry to check progress.' : 'The Bot has not been removed. Refresh if its revision changed, then retry.'}</p>}
    <div className="flex justify-end gap-2">
      <Button variant="outline" disabled={busy && !pending} onClick={close}>{pending ? 'Close' : 'Cancel'}</Button>
      <Button variant="destructive" disabled={busy || (pending && !error)} onClick={() => { clearTimeout(timer.current); void apply() }}>{pending ? (error ? 'Retry' : 'Deleting…') : 'Delete'}</Button>
    </div>
  </DialogContent></Dialog>
}
