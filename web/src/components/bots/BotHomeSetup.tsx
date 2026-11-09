import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { BotAPIError, BotStatus, setupBotHome, type Bot } from '@/api/bots'

export function hasBotHome(bot: Bot) {
  // Every draft has a conversation ID before retained storage is provisioned.
  return !!bot.home?.id && bot.home.id !== '00000000-0000-0000-0000-000000000000'
}
export default function BotHomeSetup({ bot, disabled, onPrepared, onRefresh }: { bot: Bot; disabled: boolean; onPrepared: (bot: Bot) => void; onRefresh: () => void }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState('')
  const [conflictRevision, setConflictRevision] = useState<number>()
  const alive = useRef(true), submitting = useRef(false)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const conflict = conflictRevision === bot.revision
  if (bot.status !== BotStatus.Draft || hasBotHome(bot)) return null
  async function prepare() {
    if (disabled || submitting.current || conflict) return
    submitting.current = true; setBusy(true); setError('')
    try {
      const saved = await setupBotHome(bot)
      if (alive.current) onPrepared(saved)
    } catch (e) {
      if (!alive.current) return
      if (e instanceof BotAPIError && e.status === 409) {
        setConflictRevision(bot.revision)
        setError('The Bot changed or is no longer eligible for setup. Refresh Bot and review its latest revision before retrying.')
      } else if (e instanceof BotAPIError && e.status === 503) {
        setError('Retained-home preparation is unavailable on this server (503). Ask the server administrator to configure retained-home storage, then retry. No activation or run was requested.')
      } else setError(`Retained-home preparation failed: ${(e as Error).message}. You can retry explicitly.`)
    } finally { submitting.current = false; if (alive.current) setBusy(false) }
  }
  return <section aria-label="Retained home setup" className="space-y-2 rounded border border-border p-3">
    <p className="text-sm">Prepare server-selected retained storage before reviewing Test once or activation. Choosing a conversation model does not prepare storage. This does not activate the Bot or start a run.</p>
    <Button disabled={disabled || busy || conflict} onClick={prepare}>{busy ? 'Preparing retained home…' : 'Prepare retained home'}</Button>
    {error && <p role="alert" className="text-destructive">{error}</p>}
    {conflict && <Button variant="outline" disabled={busy} onClick={onRefresh}>Refresh Bot before retrying</Button>}
  </section>
}
